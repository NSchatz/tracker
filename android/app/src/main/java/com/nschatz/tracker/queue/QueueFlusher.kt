package com.nschatz.tracker.queue

import com.nschatz.tracker.protocol.FixReporter
import com.nschatz.tracker.protocol.ReportOutcome

/**
 * Drains a [FixQueue] into `POST /v1/fixes`, oldest first.
 *
 * This is the whole delivery decision — what is sent, what is kept, what is thrown away, and when
 * to stop — and it is **pure**: a queue and a reporter go in, a [FlushReport] comes out. Nothing
 * here touches Android, so every branch below is exercised by JVM unit tests against a real
 * temp-directory queue and a real HTTP server on a loopback socket. [FixUploadWorker] is the thin
 * framework wrapper that supplies the two inputs and translates the report into a WorkManager
 * `Result`; it contains no decisions of its own, on purpose.
 *
 * ### The four ways a flush ends, and why they are not the same
 *
 * Getting these confused is how a durable queue stops being durable.
 *
 * - **Drained** — the queue is empty. The only outcome that means "nothing is owed".
 * - **MoreQueued** — [batchLimit] was reached with progress made. A worker gets ten minutes; a
 *   50,000-fix backlog does not fit in one, so the run is bounded and the caller schedules another
 *   immediately rather than letting one job be killed mid-flush.
 * - **Deferred** — a transient failure (no radio, a 5xx, a 429). The fix is **still queued**. This
 *   never becomes a drop, no matter how many times it happens: that is the entire point of C2, and
 *   the reason there is no attempt counter anywhere in this class. A fix leaves the queue when the
 *   server has it, when the server permanently refuses it, or when it ages out — never because the
 *   client got tired.
 * - **CredentialRejected** — a `401`/`403`. Distinguished from Deferred because the queue stops
 *   *whole*: every fix carries the same token, so trying the next one just spends radio to be told
 *   the same thing. Distinguished from a permanent per-fix refusal because the fixes are **kept** —
 *   the token is what is wrong, and it can be re-issued, so discarding a family's trail over a
 *   credential would be the worst possible reading of "permanent".
 *
 * ### The server decides what is undeliverable, not this class
 *
 * There is deliberately no local "this fix is too old to bother sending" rule. `ts` older than 90
 * days is refused by the server with a `400` (`SPEC.md`, "The timestamp window") and lands in the
 * permanent-refusal branch below, which removes and counts it — identical behaviour, one round trip
 * more. Deciding it here instead would mean holding a client-side copy of a server-side constant and
 * judging it against the *phone's* clock, so a device whose clock ran fast would silently delete a
 * queue of fixes the server would have accepted. In a phase whose whole promise is that nothing is
 * lost, one saved request is not worth owning that failure mode.
 *
 * @param queue the durable queue to drain.
 * @param reporter the HTTP client for `/v1/fixes`.
 * @param batchLimit the most fixes one flush will attempt.
 */
class QueueFlusher(
    private val queue: FixQueue,
    private val reporter: FixReporter,
    private val batchLimit: Int = DEFAULT_BATCH_LIMIT,
) {
    init {
        require(batchLimit >= 1) { "batchLimit must be at least 1" }
    }

    /** Sends queued fixes, oldest first, until the queue is empty or something says stop. */
    fun flush(): FlushReport {
        val batch = queue.oldest(batchLimit)
        var delivered = 0
        var discarded = batch.discarded
        var lastReason: String? = if (batch.discarded > 0) UNREADABLE_ENTRY_REASON else null

        for (entry in batch.entries) {
            val outcome = reporter.post(entry.body)
            when {
                // `isDelivered` covers 201 stored AND 200 — the server already had this instant and
                // absorbed the replay. Both mean the fix is on the server, so both retire the
                // entry; treating the 200 as a failure would leave a permanently undeliverable head
                // on the queue. The classification itself is [ReportOutcome]'s, not restated here.
                outcome.isDelivered -> {
                    queue.remove(entry)
                    delivered += 1
                }

                outcome is ReportOutcome.Rejected && (outcome.status == 401 || outcome.status == 403) ->
                    return FlushReport(
                        delivered = delivered,
                        discarded = discarded,
                        outcome = FlushOutcome.CredentialRejected(outcome.describe()),
                    )

                // Permanent and specific to this payload — a `400 invalid_fix`, or a status that
                // says we are not talking to the tracker API at all. Removing it is what stops one
                // bad fix from blocking the queue behind it; counting it is what stops that removal
                // from being invisible.
                !outcome.isRetryable -> {
                    queue.remove(entry)
                    discarded += 1
                    lastReason = (outcome as? ReportOutcome.Rejected)?.describe()
                        ?: "The server refused a buffered fix."
                }

                // Transient — no radio, a 5xx, a 429. The entry stays exactly where it is. There is
                // deliberately no attempt counter: C1's bounded retry is what turned an outage into
                // permanent loss, and never giving up is the whole of C2's promise.
                else -> return FlushReport(
                    delivered = delivered,
                    discarded = discarded,
                    outcome = FlushOutcome.Deferred(
                        (outcome as? ReportOutcome.Retryable)?.reason ?: "the fix could not be sent yet",
                    ),
                )
            }
        }

        // Every entry in the batch was either delivered or discarded — the loop returns early
        // otherwise — so anything still queued is either past the batch cap or was enqueued while
        // this flush ran. Both mean "there is more to send, and the network is working", which is a
        // reason to run again now rather than after a backoff.
        val outcome = if (queue.size() > 0) FlushOutcome.MoreQueued else FlushOutcome.Drained
        return FlushReport(delivered = delivered, discarded = discarded, outcome = outcome, reason = lastReason)
    }

    companion object {
        /**
         * 250 fixes per run — roughly four hours of backlog at C1's cadence, and a few seconds of
         * network on a working connection. Bounded so one flush cannot outlive the ten minutes
         * WorkManager allows a worker; the caller reschedules for the rest.
         */
        const val DEFAULT_BATCH_LIMIT: Int = 250

        internal const val UNREADABLE_ENTRY_REASON: String =
            "A buffered fix could not be read back from disk and was discarded."
    }
}

/**
 * What one flush achieved.
 *
 * @param delivered fixes the server now has (stored, or absorbed as an idempotent replay).
 * @param discarded fixes removed **without** being stored: permanently refused by the
 *   server, or unreadable on disk. Every one of these is a hole in the trail and belongs in front
 *   of the user.
 * @param reason the last thing that went wrong, if anything, in words a person can act on.
 */
data class FlushReport(
    val delivered: Int,
    val discarded: Int,
    val outcome: FlushOutcome,
    val reason: String? = null,
)

/** Why a flush stopped. */
sealed interface FlushOutcome {

    /** The queue is empty. */
    data object Drained : FlushOutcome

    /** Fixes remain and can be sent now — schedule another run without waiting on a backoff. */
    data object MoreQueued : FlushOutcome

    /** A transient failure. The remaining fixes are still queued; try again later. */
    data class Deferred(val reason: String) : FlushOutcome

    /** The server rejected this device's token. Fixes are **kept**; the credential needs fixing. */
    data class CredentialRejected(val reason: String) : FlushOutcome
}

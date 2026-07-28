package com.nschatz.tracker.queue

import com.nschatz.tracker.protocol.Fix
import com.nschatz.tracker.protocol.FixReporter
import com.nschatz.tracker.protocol.FixTrigger
import com.nschatz.tracker.protocol.RecordedRequest
import com.nschatz.tracker.protocol.TestHttpServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.util.Collections

/**
 * The flush loop, end to end: a **real queue** on a real directory, a **real HTTP client**, and a
 * fake `/v1/fixes` on a **real loopback socket** that behaves the way `SPEC.md` says the server does.
 *
 * ### Why the fake server dedups
 *
 * C2's acceptance criterion is "airplane-mode→online flush delivers every buffered fix **exactly
 * once**". A server that answered `201` to everything could not falsify that: a client that sent
 * every fix twice would look identical. So [FixesEndpoint] keeps the set of `ts` values it has
 * stored and answers a replay with `200 duplicate`, exactly as the real handler does — which makes
 * a duplicate *visible* as a second request for a `ts` already stored, and lets the tests assert
 * that no fix was ever stored twice.
 *
 * ### What this does not prove
 *
 * That WorkManager schedules the flush when connectivity returns. That is platform behaviour
 * requiring a device; it is an operator check in `android/README.md`. What is proved here is the
 * part that is ours: given a chance to run, the flush loses nothing, duplicates nothing, reorders
 * nothing, and never blocks itself forever on a fix that cannot be sent.
 */
class QueueFlusherTest {

    @get:Rule
    val temp = TemporaryFolder()

    private lateinit var server: TestHttpServer
    private lateinit var endpoint: FixesEndpoint

    /** A fixed base instant, so the buffered timestamps are stable across runs. */
    private val now = 1_752_566_400L

    @Before
    fun startServer() {
        server = TestHttpServer()
        endpoint = FixesEndpoint()
        server.responder = endpoint::respondTo
    }

    @After
    fun stopServer() {
        server.close()
    }

    private fun queue(capacity: Int = FixQueue.DEFAULT_CAPACITY) =
        FixQueue(File(temp.root, "fix-queue"), capacity)

    private fun flusher(
        queue: FixQueue,
        batchLimit: Int = QueueFlusher.DEFAULT_BATCH_LIMIT,
        baseUrl: String = server.baseUrl,
    ) = QueueFlusher(
        queue = queue,
        reporter = FixReporter(baseUrl, "EXAMPLE-DEVICE-TOKEN-abc123"),
        batchLimit = batchLimit,
    )

    private fun fix(ts: Long) = Fix(
        lat = 41.9028,
        lon = 12.4964,
        tsEpochSeconds = ts,
        accuracyM = 5.0,
        trigger = FixTrigger.PERIODIC,
        msgId = "m-$ts",
    )

    /** Buffers [count] fixes one minute apart, ending just before "now" — an offline stretch. */
    private fun bufferOfflineFixes(queue: FixQueue, count: Int): List<Long> {
        val stamps = (1..count).map { now - (count - it + 1) * 60L }
        for (ts in stamps) queue.enqueue(fix(ts))
        return stamps
    }

    // --- the acceptance criterion -----------------------------------------------------------------

    /**
     * **Airplane mode → online: every buffered fix delivered exactly once.**
     *
     * The outage is modelled the way it actually presents to the client — the POST fails — and the
     * assertions are the two halves of "exactly once": nothing missing (every `ts` stored, the queue
     * empty) and nothing twice (no `ts` stored more than once, and no replay ever needed).
     */
    @Test
    fun anOfflineBacklogIsDeliveredExactlyOnceWhenTheNetworkReturns() {
        val q = queue()
        val buffered = bufferOfflineFixes(q, count = 25)

        // Offline. Every attempt fails; nothing may be lost.
        endpoint.offline = true
        val whileOffline = flusher(q).flush()
        assertTrue(whileOffline.outcome is FlushOutcome.Deferred)
        assertEquals(0, whileOffline.delivered)
        assertEquals(0, whileOffline.discarded)
        assertEquals("no fix may leave the queue while the server is unreachable", 25, q.size())

        // Online.
        endpoint.offline = false
        val whenOnline = flusher(q).flush()

        assertEquals(FlushOutcome.Drained, whenOnline.outcome)
        assertEquals(25, whenOnline.delivered)
        assertEquals(0, whenOnline.discarded)
        assertEquals(0, q.size())
        assertEquals("every buffered fix reached the server", buffered, endpoint.storedOrder)
        assertEquals("no fix was stored twice", 0, endpoint.duplicateCount)
    }

    /** Oldest first, as it appears on the wire — a backlog must not replay someone's day backwards. */
    @Test
    fun theServerSeesTheBacklogOldestFirst() {
        val q = queue()
        // Enqueued in a scrambled order on purpose: the flush order is the queue's, not the
        // caller's, and a wire order that merely echoed the enqueue order would prove nothing.
        val buffered = (1..10).map { now - it * 60L }.shuffled()
        for (ts in buffered) q.enqueue(fix(ts))

        flusher(q).flush()
        assertEquals(buffered.sorted(), endpoint.storedOrder)
    }

    /**
     * A fix whose response was lost is re-sent, and the server absorbs it as a `200 duplicate`.
     *
     * This is the seam that makes the queue's at-least-once delivery safe: the entry is deleted only
     * *after* the server answers, so a crash in between replays the fix. The client must treat that
     * `200` as delivered — anything else leaves a permanently undeliverable entry at the head of the
     * queue, blocking everything behind it.
     */
    @Test
    fun aReplayAfterALostResponseIsAbsorbedRatherThanStuck() {
        val q = queue()
        val ts = now - 60
        q.enqueue(fix(ts))
        // The server already has this instant — exactly the state after a crash between the 201 and
        // the local delete.
        endpoint.preStore(ts)

        val report = flusher(q).flush()

        assertEquals(FlushOutcome.Drained, report.outcome)
        assertEquals(1, report.delivered)
        assertEquals(0, report.discarded)
        assertEquals(0, q.size())
        assertEquals(1, endpoint.duplicateCount)
    }

    // --- what stops a flush, and what it does to the queue -----------------------------------------

    /**
     * A transient failure keeps **everything**.
     *
     * There is no attempt counter in the flusher on purpose: C1's bounded retry is what turned an
     * outage into permanent loss, and C2's entire promise is that it cannot any more.
     */
    @Test
    fun aTransientServerErrorLeavesEveryFixQueued() {
        val q = queue()
        bufferOfflineFixes(q, count = 5)
        endpoint.statusOverride = 503

        val report = flusher(q).flush()

        assertTrue(report.outcome is FlushOutcome.Deferred)
        assertEquals(0, report.delivered)
        assertEquals(5, q.size())
    }

    /**
     * A `401` stops the flush **whole** and keeps every fix.
     *
     * Whole, because every fix carries the same token: trying the next one only spends radio to be
     * told the same thing. Kept, because the credential is what is wrong and it can be re-issued —
     * discarding a family's trail over a token would be the worst possible reading of "permanent".
     */
    @Test
    fun aRejectedCredentialStopsTheFlushWithoutDiscardingAnything() {
        val q = queue()
        bufferOfflineFixes(q, count = 5)
        endpoint.statusOverride = 401

        val report = flusher(q).flush()

        assertTrue(report.outcome is FlushOutcome.CredentialRejected)
        assertEquals(0, report.delivered)
        assertEquals(0, report.discarded)
        assertEquals(5, q.size())
        // One request, not five: the flush stopped rather than walking the queue.
        assertEquals(1, server.requests.size)
        assertTrue(
            "the reason should name the fix",
            (report.outcome as FlushOutcome.CredentialRejected).reason.contains("token"),
        )
    }

    /**
     * A permanently refused fix is dropped and the queue **keeps moving**.
     *
     * A `400` means this exact payload will be refused every time. Leaving it at the head of a FIFO
     * would block every later fix forever — a single bad fix would silently end reporting — so it
     * goes, and it is counted as loss rather than success.
     */
    @Test
    fun aPermanentlyRefusedFixIsDroppedAndTheRestStillFlush() {
        val q = queue()
        val buffered = bufferOfflineFixes(q, count = 4)
        endpoint.refuse(buffered[1])

        val report = flusher(q).flush()

        assertEquals(FlushOutcome.Drained, report.outcome)
        assertEquals(3, report.delivered)
        assertEquals(1, report.discarded)
        assertEquals(0, q.size())
        assertEquals(buffered - buffered[1], endpoint.storedOrder)
        assertTrue(report.reason?.contains("400") == true)
    }

    // --- bounding one run --------------------------------------------------------------------------

    /**
     * One flush is bounded, and says there is more to do.
     *
     * WorkManager gives a worker ten minutes; a large backlog does not fit. Bounding the run and
     * reporting [FlushOutcome.MoreQueued] is what lets the caller schedule another immediately
     * instead of having one job killed mid-flush.
     */
    @Test
    fun oneFlushIsBoundedByTheBatchLimitAndReportsThatMoreRemain() {
        val q = queue()
        bufferOfflineFixes(q, count = 10)

        val first = flusher(q, batchLimit = 4).flush()
        assertEquals(FlushOutcome.MoreQueued, first.outcome)
        assertEquals(4, first.delivered)
        assertEquals(6, q.size())

        // Draining the rest still delivers each fix exactly once overall.
        while (q.size() > 0) flusher(q, batchLimit = 4).flush()
        assertEquals(10, endpoint.storedOrder.size)
        assertEquals(0, endpoint.duplicateCount)
    }

    /** An empty queue is a clean, cheap no-op — not a request, and not a failure. */
    @Test
    fun flushingAnEmptyQueueSendsNothing() {
        val report = flusher(queue()).flush()
        assertEquals(FlushOutcome.Drained, report.outcome)
        assertEquals(0, report.delivered)
        assertTrue(server.requests.isEmpty())
    }

    /**
     * An unreadable entry is reported as a discard, so disk corruption shows up as loss rather than
     * as a queue that quietly gets shorter.
     */
    @Test
    fun anUnreadableEntryIsReportedAsLoss() {
        val q = queue()
        q.enqueue(fix(now - 60))
        File(File(temp.root, "fix-queue"), FixQueue.entryName(now - 120)).writeText("")

        val report = flusher(q).flush()

        assertEquals(1, report.delivered)
        assertEquals(1, report.discarded)
        assertEquals(QueueFlusher.UNREADABLE_ENTRY_REASON, report.reason)
    }

    /** No network at all (nothing listening) is transient, not a reason to discard. */
    @Test
    fun anUnreachableServerIsDeferredNotDiscarded() {
        val q = queue()
        bufferOfflineFixes(q, count = 3)
        // A port with nothing on it: the same IOException a phone with no radio produces.
        val dead = "http://127.0.0.1:${findClosedPort()}"

        val report = flusher(q, baseUrl = dead).flush()

        assertTrue(report.outcome is FlushOutcome.Deferred)
        assertEquals(3, q.size())
    }

    private fun findClosedPort(): Int {
        val socket = java.net.ServerSocket(0, 1, java.net.InetAddress.getLoopbackAddress())
        val port = socket.localPort
        socket.close()
        return port
    }
}

/**
 * A stand-in for `POST /v1/fixes` that keeps the server's actual idempotency contract.
 *
 * It stores the set of `ts` values it has seen and answers `201 stored` the first time and
 * `200 duplicate` on a replay (`SPEC.md`, "Idempotency"). That is the whole reason it exists: it
 * makes a duplicated delivery *observable*, which a constant-status fake cannot.
 */
private class FixesEndpoint {

    // Written on the server's accept thread and read from the test thread, so the collections are
    // synchronized and the counter is volatile — the same care TestHttpServer takes with `requests`.

    /** Every `ts` stored, in the order it arrived. */
    val storedOrder: MutableList<Long> = Collections.synchronizedList(mutableListOf())

    /** How many replays were absorbed. Zero is what "exactly once on the wire" looks like. */
    @Volatile
    var duplicateCount: Int = 0
        private set

    /** When true, every request fails with a 503 — the client-visible shape of no connectivity. */
    @Volatile
    var offline: Boolean = false

    /** Forces a status on every request, for the credential and transient-error cases. */
    @Volatile
    var statusOverride: Int? = null

    private val refused: MutableSet<Long> = Collections.synchronizedSet(mutableSetOf())

    /** Marks [ts] as one the server permanently refuses — a `400 invalid_fix`. */
    fun refuse(ts: Long) {
        refused.add(ts)
    }

    /** Seeds a `ts` as already stored, the state left by a crash between the 201 and the delete. */
    fun preStore(ts: Long) {
        storedOrder.add(ts)
    }

    fun respondTo(request: RecordedRequest): Pair<Int, String> {
        if (offline) return 503 to """{"error":"internal","message":"unavailable"}"""
        statusOverride?.let { status ->
            val body = if (status == 401) {
                """{"error":"unauthorized","message":"unknown device token"}"""
            } else {
                """{"error":"internal","message":"try again"}"""
            }
            return status to body
        }

        // `ts` is a JSON *number*, so this reads it out of the body directly rather than reusing the
        // client's string-field helper. Deliberately parsing the real bytes: it is the same field
        // the real server dedups on, so a client that stopped sending it would fail here too.
        val ts = TS_FIELD.find(request.body)?.groupValues?.get(1)?.toLongOrNull()
            ?: return 400 to """{"error":"malformed","message":"no ts"}"""

        if (ts in refused) return 400 to """{"error":"invalid_fix","message":"refused by test"}"""

        return if (storedOrder.contains(ts)) {
            duplicateCount += 1
            200 to """{"status":"duplicate","deduped":true}"""
        } else {
            storedOrder.add(ts)
            201 to """{"status":"stored","deduped":false}"""
        }
    }

    private companion object {
        val TS_FIELD = Regex("\"ts\"\\s*:\\s*(-?\\d+)")
    }
}

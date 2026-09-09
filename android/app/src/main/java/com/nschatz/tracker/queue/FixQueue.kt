package com.nschatz.tracker.queue

import com.nschatz.tracker.protocol.Fix
import com.nschatz.tracker.protocol.toJsonBody
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.nio.charset.StandardCharsets

/**
 * The **offline-durable report queue** (roadmap C2): every measured fix is written to disk before
 * anything tries to send it, and it stays there until the server has it.
 *
 * ### Why this exists
 *
 * C1 reported straight from the foreground service and held nothing. A fix that could not be
 * delivered was retried a bounded number of times and then **lost** — which is risk path #3, silent
 * fix loss, the failure mode a location product cannot have. Airplane mode for ten minutes left a
 * ten-minute hole in someone's trail. This class is what closes that: the fix is durable the instant
 * it is measured, and delivery is a separate, resumable concern
 * ([QueueFlusher] driven by [FixUploadWorker]).
 *
 * ### Why files and not Room/SQLite
 *
 * Two reasons, in order of importance.
 *
 * 1. **The gate can prove this.** `java.io.File` is plain JDK, so the whole durability story —
 *    survives a new process, oldest-first order, dedup, eviction, a torn write — is exercised by
 *    real JVM unit tests against a real temp directory. Room needs a `Context` and a device (or
 *    Robolectric, which is a simulation of one), so the queue's correctness would have moved behind
 *    the device boundary this module deliberately keeps things out of. See `android/README.md`,
 *    "What the gate proves and what it cannot".
 * 2. **The workload is a FIFO of ~200-byte records with no queries.** A relational engine, its
 *    annotation processor and its schema migrations would all be paid for a table that is only ever
 *    read in one order and deleted from one end.
 *
 * ### The layout, and why every part of it is load-bearing
 *
 * One file per fix, in [directory], named `<19-digit zero-padded ts>.fix`, whose **contents are the
 * exact JSON body that will be POSTed**.
 *
 * - **The name is the `ts`.** `ts` is not a convenience key: `SPEC.md` says the identity of a fix is
 *   `(device_id, ts)`, and the server dedups on exactly that. Making the filename the `ts` therefore
 *   makes the filesystem enforce the *same* identity the server does — enqueueing the same instant
 *   twice cannot create two rows because it cannot create two files. Zero-padded to 19 digits (the
 *   width of `Long.MAX_VALUE`) so that **lexicographic order is numeric order**, which is what makes
 *   "oldest first" a plain directory listing sort rather than a parse-everything-then-sort.
 * - **The contents are the wire bytes.** The body is encoded and validated once, at enqueue, and the
 *   bytes that were validated are the bytes that are sent — there is no re-encode on the delivery
 *   path that could drift from the one the payload tests pin. It also means the `msg_id` generated
 *   for a fix is **persisted with it**, so a replay after a lost response carries the same
 *   correlator rather than inventing a new one.
 * - **Writes are atomic.** Content goes to `<name>.tmp`, is `fsync`ed, and is then `rename`d into
 *   place. POSIX rename is atomic, so a crash mid-write leaves a `.tmp` that no reader can see (the
 *   listing only accepts [ENTRY_SUFFIX]) and that [sweepPartialWrites] removes. The alternative —
 *   writing in place — can leave a half-written body that would be sent as a `400 malformed`.
 *
 *   **Be honest about how much of that the gate holds.** The *reader* half is tested: a `.tmp` is
 *   never returned as a queued fix, it is swept, and a zero-length entry is discarded and counted.
 *   The *writer* half — that this code goes temp → `fsync` → `rename` rather than opening the final
 *   name directly — is **review-only**: a JVM unit test cannot interrupt a write mid-syscall or cut
 *   the power, so replacing this block with an in-place write would keep the suite green. It is
 *   called out here because an untested invariant that nobody has written down is the one that
 *   quietly disappears in a refactor.
 *
 * ### Exactly once, and where it actually comes from
 *
 * This queue guarantees **at-least-once on the wire**: an entry is deleted only *after* the server
 * has acknowledged it, so a crash between the `201` and the delete replays that fix. **Exactly once
 * in the database** is the server's `(device_id, ts)` dedup, which answers the replay with a `200`
 * (`SPEC.md`, "Idempotency"). The two halves are deliberate — a client that deleted first would be
 * at-most-once, and would silently lose exactly the fixes whose responses went missing.
 *
 * ### Thread safety
 *
 * The collection service enqueues on the main thread while the WorkManager worker flushes on a
 * background one, so every mutator is `@Synchronized` and callers share one instance
 * ([FixQueues.of]). The atomic-rename layout means even an unsynchronised reader can only ever see
 * a whole entry or none.
 *
 * @param directory where entries live. Created on demand.
 * @param capacity the maximum number of queued fixes; see [enqueue] for what happens at the cap.
 */
class FixQueue(
    private val directory: File,
    private val capacity: Int = DEFAULT_CAPACITY,
) {
    init {
        require(capacity >= 1) { "capacity must be at least 1" }
    }

    /**
     * Persists [fix] and returns what happened.
     *
     * The fix must already be valid ([com.nschatz.tracker.protocol.FixValidation]); this stores what
     * it is given and does not re-judge it. It must also carry a `msg_id` — see [Fix.msgId] — so the
     * correlator is fixed at enqueue and survives every replay.
     */
    @Synchronized
    fun enqueue(fix: Fix): EnqueueResult {
        if (fix.tsEpochSeconds < 0) {
            // A negative ts cannot be zero-padded into a lexicographically ordered name, and it is
            // 25 years outside the server's 90-day ingest window besides. Refusing loudly beats
            // storing an entry whose position in the queue would be wrong.
            return EnqueueResult.Failed("a fix timestamped before 1970 cannot be queued")
        }
        if (!ensureDirectory()) {
            return EnqueueResult.Failed("the fix queue directory could not be created")
        }
        sweepPartialWrites()

        val target = File(directory, entryName(fix.tsEpochSeconds))
        // Dedup, client-side, on the SAME identity the server uses. A second fix for an instant the
        // queue already holds is not new information — the server would answer the second one with a
        // `200 duplicate` — so absorbing it here saves the round trip rather than changing any
        // outcome.
        if (target.exists()) return EnqueueResult.AlreadyQueued

        val temp = File(directory, target.name + TEMP_SUFFIX)
        return try {
            FileOutputStream(temp).use { out ->
                out.write(fix.toJsonBody().toByteArray(StandardCharsets.UTF_8))
                out.flush()
                // fsync before the rename. Without it the rename can be durable while the content
                // is still only in the page cache, and a power loss then leaves a zero-length entry
                // that looks like a valid queued fix and is a `400 malformed` on delivery. One
                // sync per fix at a one-minute cadence is not a battery consideration.
                out.fd.sync()
            }
            if (!temp.renameTo(target)) {
                temp.delete()
                EnqueueResult.Failed("the fix could not be committed to the queue directory")
            } else {
                // Trim AFTER the new entry is safely on disk, never before.
                //
                // Evicting first would destroy older fixes to make room for one that then fails to
                // write — and the failure that makes room-making necessary at all is a full queue on
                // a phone that has been offline for days, which is exactly the phone most likely to
                // be out of disk. That ordering loses N real fixes and reports one. Trimming
                // afterwards means a failed write costs nothing but the fix that failed.
                EnqueueResult.Queued(evicted = trimToCapacity())
            }
        } catch (e: IOException) {
            temp.delete()
            EnqueueResult.Failed("the fix could not be written to disk (${e.javaClass.simpleName})")
        }
    }

    /**
     * The [limit] oldest queued fixes, oldest first.
     *
     * Oldest-first is the roadmap's requirement and it is also the only order that is safe: the
     * server's history is ordered by `ts`, and a client that flushed newest-first would, during a
     * long backlog, show a family member jumping backwards through their own afternoon.
     *
     * An entry whose contents cannot be read is **discarded here**, not returned. That is the one
     * place this class throws data away on its own initiative, and it is deliberate: an unreadable
     * entry can never be delivered, and leaving it at the head of a FIFO would block every fix
     * behind it forever. The count comes back in [BatchResult.discarded] so the loss is reported
     * rather than silent.
     */
    @Synchronized
    fun oldest(limit: Int): BatchResult {
        require(limit >= 1) { "limit must be at least 1" }
        sweepPartialWrites()
        val names = entryNames()
        val entries = mutableListOf<QueuedFix>()
        var discarded = 0
        for (name in names) {
            if (entries.size >= limit) break
            val ts = timestampOf(name)
            val file = File(directory, name)
            val body = try {
                file.readText(StandardCharsets.UTF_8)
            } catch (_: IOException) {
                null
            }
            if (ts == null || body.isNullOrBlank()) {
                file.delete()
                discarded += 1
                continue
            }
            entries.add(QueuedFix(tsEpochSeconds = ts, body = body))
        }
        return BatchResult(entries = entries, discarded = discarded)
    }

    /**
     * Removes a delivered (or permanently refused) entry.
     *
     * Called only *after* the server has answered, which is what makes the queue at-least-once
     * rather than at-most-once. Returns whether an entry was actually removed; `false` means it was
     * already gone, which is not an error — an eviction or a concurrent flush can legitimately have
     * beaten this call.
     */
    @Synchronized
    fun remove(entry: QueuedFix): Boolean = File(directory, entryName(entry.tsEpochSeconds)).delete()

    /** How many fixes are waiting. Counts committed entries only, never a partial write. */
    @Synchronized
    fun size(): Int = entryNames().size

    /**
     * Drops the oldest entries until the queue is back within [capacity], returning how many were
     * lost.
     *
     * A cap has to exist — an unbounded queue on a phone that is offline for a month is a disk-full
     * bug — so the only real question is which end to sacrifice, and it is the **oldest**. Three
     * reasons: the recent trail is what answers "where are they now", which is the product's job;
     * the oldest entries are the ones nearest the server's 90-day ingest floor, past which it would
     * refuse them anyway; and eviction from the head is the one choice that cannot reorder what is
     * left.
     *
     * The count is returned rather than swallowed so [com.nschatz.tracker.collect.CollectionStatus]
     * can show it — a queue that quietly forgets is the same silent loss C1 had, just slower.
     */
    private fun trimToCapacity(): Int {
        val names = entryNames()
        if (names.size <= capacity) return 0
        var evicted = 0
        for (name in names) {
            if (names.size - evicted <= capacity) break
            if (File(directory, name).delete()) evicted += 1
        }
        return evicted
    }

    /**
     * Removes `.tmp` files left by a crash mid-write.
     *
     * They are already invisible to every reader (the listing takes only [ENTRY_SUFFIX]), so this is
     * housekeeping, not correctness — but without it a device that crashes repeatedly accumulates
     * junk in the app's data directory forever.
     */
    private fun sweepPartialWrites() {
        val stale = directory.listFiles { _, name -> name.endsWith(TEMP_SUFFIX) } ?: return
        for (file in stale) file.delete()
    }

    /**
     * The queue depth, or **null when the directory could not be read at all**.
     *
     * [size] cannot express that: an unreadable directory and an empty one both come back as `0`,
     * and `0` on the screen is a claim that nothing is waiting. That is precisely the confusion the
     * frontend conventions' F3 forbids — a measurement that was not taken must render as "not
     * recorded", never as a zero a reader will believe. So the UI reads this, and shows the figure
     * as unavailable rather than inventing one.
     *
     * [size] is left exactly as it was, because the collection path's decisions are counted on it.
     */
    fun depth(): Int? {
        if (!ensureDirectory()) return null
        val names = directory.list { _, name -> name.endsWith(ENTRY_SUFFIX) } ?: return null
        return names.size
    }

    /** Committed entry names, in oldest-first order. */
    private fun entryNames(): List<String> =
        (directory.list { _, name -> name.endsWith(ENTRY_SUFFIX) } ?: emptyArray())
            .sorted()

    private fun ensureDirectory(): Boolean = directory.isDirectory || directory.mkdirs()

    companion object {
        /**
         * 5,000 fixes — about three and a half days at C1's one-a-minute cadence, and a few hundred
         * KiB of JSON. Chosen to cover a realistic outage (a weekend in a valley, a phone left in
         * airplane mode) while staying far away from the disk pressure an unbounded queue would
         * eventually create.
         */
        const val DEFAULT_CAPACITY: Int = 5_000

        /** Committed entries. */
        const val ENTRY_SUFFIX: String = ".fix"

        /** In-progress writes, never visible to a reader. */
        const val TEMP_SUFFIX: String = ".tmp"

        /** 19 digits — the width of `Long.MAX_VALUE`, so padded names sort numerically. */
        private const val NAME_DIGITS = 19

        internal fun entryName(tsEpochSeconds: Long): String =
            tsEpochSeconds.toString().padStart(NAME_DIGITS, '0') + ENTRY_SUFFIX

        internal fun timestampOf(entryName: String): Long? =
            entryName.removeSuffix(ENTRY_SUFFIX).toLongOrNull()
    }
}

/** One fix waiting to be delivered: its server identity, and the exact bytes to POST. */
data class QueuedFix(
    /** The `ts` this fix will be deduped on, server-side. Also its position in the queue. */
    val tsEpochSeconds: Long,
    /** The `POST /v1/fixes` body, exactly as it was encoded and validated at enqueue. */
    val body: String,
)

/** A page of queued fixes, plus any entries that had to be thrown away to produce it. */
data class BatchResult(
    val entries: List<QueuedFix>,
    /** Entries discarded as unreadable. Non-zero means the trail has a hole; report it. */
    val discarded: Int,
)

/** What became of an [FixQueue.enqueue]. */
sealed interface EnqueueResult {

    /**
     * The fix is on disk.
     *
     * @param evicted how many older fixes were dropped to make room. Normally 0; anything else is
     *   real data loss and must be surfaced, not counted as a clean success.
     */
    data class Queued(val evicted: Int) : EnqueueResult

    /** An entry for this exact instant was already queued. Idempotent no-op, nothing lost. */
    data object AlreadyQueued : EnqueueResult

    /** The fix could not be persisted, so it is gone. @param reason a sentence for the user. */
    data class Failed(val reason: String) : EnqueueResult
}

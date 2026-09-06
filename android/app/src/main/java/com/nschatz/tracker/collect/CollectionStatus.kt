package com.nschatz.tracker.collect

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

/**
 * What the collection service is doing right now, observable by the UI.
 *
 * A process-scoped singleton rather than a bound-service connection: the screen needs a handful of
 * counters, not a lifecycle, and binding a service to read four numbers would be more machinery
 * than the thing it reports on.
 *
 * ### Why the counters exist at all
 *
 * Because the alternative is an app that looks identical whether it is working or not. The roadmap's
 * fail-safe for this phase is that a denied permission or a failed report produces "a clear disabled
 * state ... never a silent no-op that looks like it's working". A running notification proves the
 * *service* is alive; it proves nothing about whether a single fix ever reached the server. These
 * counters — and especially [dropped] and [lastError] — are what make the difference visible on the
 * device, which is also the only place C1's real behaviour can be observed at all (see
 * `android/README.md`).
 *
 * The counters are intentionally **not** persisted. They describe this process's run; after a
 * restart the honest answer is "nothing yet", not a stale number recovered from disk.
 *
 * [queued] is the exception, and necessarily so: it reports the depth of the on-disk queue, which
 * genuinely does survive the process. It is read back from the queue rather than reset, because
 * showing `0` while fixes are sitting on disk waiting to be sent would misrepresent the one thing
 * C2 exists to guarantee.
 */
object CollectionStatus {

    /** Whether the foreground service is currently running. */
    var running: Boolean by mutableStateOf(false)
        internal set

    /** Fixes the server accepted — `201 Created` or a `200` idempotent replay. Both arrived. */
    var delivered: Int by mutableIntStateOf(0)
        internal set

    /**
     * Fixes measured and written to the durable queue but not yet delivered.
     *
     * A non-zero value is **normal, not an error** — it is what the queue looks like while the phone
     * is offline, and it is the number that says "nothing has been lost, it is waiting". It is shown
     * next to [dropped] precisely so the two are not confused: queued fixes are still owed to the
     * server, dropped ones never will be.
     */
    var queued: Int by mutableIntStateOf(0)
        internal set

    /**
     * Fixes that were measured and will never reach the server: the server refused them
     * permanently, they aged past its 90-day ingest window, they were evicted when the queue hit its
     * cap, or they could not be written to (or read back from) disk.
     *
     * Since **C2** a transient failure can no longer move this counter — that is the whole point of
     * the durable queue, and it is why the offline case now shows up in [queued] instead. What is
     * left here is genuine, permanent loss, and it stays as prominent as [delivered] so it cannot be
     * silent.
     */
    var dropped: Int by mutableIntStateOf(0)
        internal set

    /** Epoch millis of the last fix the service handed to the reporter, or 0 if none yet. */
    var lastFixAtMillis: Long by mutableLongStateOf(0L)
        internal set

    /** The last failure, in the words the server or the network used. Null once something works. */
    var lastError: String? by mutableStateOf(null)
        internal set

    /**
     * The single **recorded reason** held right now, or null when none is.
     *
     * The one piece of state here that is a mirror of something durable rather than a fact about
     * this process's run, and necessarily so: it describes a case that may have happened in a
     * process that no longer exists - a boot receiver that refused to start collection and was then
     * reclaimed - and it has to survive to be shown the next time the app is opened.
     * [CollectionState] owns the disk copy and keeps this one in step; it is here only so the screen
     * can observe it.
     *
     * Distinct from [lastError], which is this run's delivery trouble in the server's or the
     * network's own words. A recorded reason is about collection not running (or not having a
     * position to prove it restarted), which is a different question from whether the last flush
     * worked, and merging them would let a resolved outage clear a live refusal.
     */
    var recordedReason: ReasonCase? by mutableStateOf(null)
        internal set

    // The mutators are @Synchronized because they are genuinely called from more than one thread:
    // `recordFlush` runs on the WorkManager worker's thread, while `recordQueued`, `recordDropped`
    // and the service lifecycle callbacks run on the main thread. `delivered += 1` is a
    // read-modify-write, so without this a concurrent drop and delivery can lose an increment — on
    // the one counter whose entire purpose is to be trustworthy about whether the trail has holes
    // in it. Compose's snapshot state makes the *visibility* safe; it does not make the increment
    // atomic.

    /**
     * Clears this process's counters at the start of a collection run.
     *
     * [queued] is deliberately untouched — see the class note. The caller sets it from the queue
     * itself, which is the only source that can tell the truth about it.
     */
    @Synchronized
    internal fun reset() {
        delivered = 0
        dropped = 0
        lastFixAtMillis = 0L
        lastError = null
    }

    /**
     * Records a fix accepted into the durable queue.
     *
     * Deliberately does **not** clear [lastError]: a fix landing on disk says nothing about whether
     * the last delivery attempt worked, and wiping the error here would make an ongoing outage look
     * resolved every time a new fix was measured.
     *
     * @param queueDepth how many fixes are now waiting.
     */
    @Synchronized
    internal fun recordQueued(queueDepth: Int) {
        queued = queueDepth
    }

    /**
     * Records the result of one flush of the durable queue.
     *
     * [lastError] is cleared only when something was delivered and nothing was lost — a flush that
     * delivered nine fixes and discarded one is not a clean bill of health.
     */
    @Synchronized
    internal fun recordFlush(delivered: Int, discarded: Int, queued: Int, reason: String?) {
        this.delivered += delivered
        this.dropped += discarded
        this.queued = queued
        when {
            reason != null -> lastError = reason
            delivered > 0 && discarded == 0 -> lastError = null
        }
    }

    @Synchronized
    internal fun recordDropped(reason: String) {
        dropped += 1
        lastError = reason
    }

    /** Records [count] fixes lost at once — evicted from a full queue, say. */
    @Synchronized
    internal fun recordDroppedBatch(count: Int, reason: String) {
        if (count <= 0) return
        dropped += count
        lastError = reason
    }

    /**
     * Records a reason collection cannot run, **without** touching [dropped].
     *
     * The distinction is worth the extra method. [dropped] means "a real position was measured and
     * then lost" — it is the number that says the trail has a hole in it. A missing server URL or a
     * revoked permission means no fix was ever collected, so counting it as a drop would inflate
     * the one number whose whole job is to be trustworthy about data loss.
     */
    @Synchronized
    internal fun recordBlocked(reason: String) {
        lastError = reason
    }
}

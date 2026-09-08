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

    /**
     * How much of this readout has actually been measured.
     *
     * The screen needs three states, and it cannot render them from the counters alone: `0` after a
     * failed read and `0` after a successful one look identical. [UNKNOWN] is the state before the
     * first read has happened, [READ] once one has, and [UNREADABLE] when the queue directory could
     * not be listed - which costs that ONE figure and nothing else on the screen.
     */
    enum class ReadState { UNKNOWN, READ, UNREADABLE }

    /** Whether the queue depth has been read, and whether the attempt worked. */
    var readState: ReadState by mutableStateOf(ReadState.UNKNOWN)
        internal set

    /** Whether the foreground service is currently running. */
    var running: Boolean by mutableStateOf(false)
        internal set

    /**
     * When the current run started, or null if no run has started in this process.
     *
     * The counters below are counted over THIS RUN, and a figure whose set is unnamed is a figure a
     * reader will take for a lifetime total. This is the "from when" the screen prints beside them.
     */
    var runStartedAtMillis: Long? by mutableStateOf(null)
        internal set

    /** When any counter last moved, so the screen can say whether it is current or last known. */
    var countersAsOfMillis: Long? by mutableStateOf(null)
        internal set

    /**
     * Fixes the server accepted — `201 Created` or a `200` idempotent replay. Both arrived.
     *
     * **Null means NOT RECORDED**, and that is different from zero. Before a run has started, or
     * after a process restart, nothing has been measured, and rendering `0` would tell the person
     * carrying the phone that nothing has been delivered when the truth is that nobody counted. A
     * genuine measured zero — a run that has started and delivered nothing yet — is `0`.
     */
    var delivered: Int? by mutableStateOf<Int?>(null)
        internal set

    /**
     * Fixes measured and written to the durable queue but not yet delivered.
     *
     * A non-zero value is **normal, not an error** — it is what the queue looks like while the phone
     * is offline, and it is the number that says "nothing has been lost, it is waiting". It is shown
     * next to [dropped] precisely so the two are not confused: queued fixes are still owed to the
     * server, dropped ones never will be.
     */
    var queued: Int? by mutableStateOf<Int?>(null)
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
    var dropped: Int? by mutableStateOf<Int?>(null)
        internal set

    /** Epoch millis of the last fix the service handed to the reporter, or 0 if none yet. */
    var lastFixAtMillis: Long by mutableLongStateOf(0L)
        internal set

    /**
     * Puts the readout back where a freshly started process finds it.
     *
     * Test-only in effect: the object is a process-scoped singleton, so an instrumented case that
     * drove it would otherwise leak its state into the next one and the "not recorded" branch would
     * be unprovable after any case that recorded something.
     */
    @Synchronized
    internal fun clearForTest() {
        readState = ReadState.UNKNOWN
        running = false
        delivered = null
        queued = null
        dropped = null
        runStartedAtMillis = null
        countersAsOfMillis = null
        lastFixAtMillis = 0L
        lastError = null
    }

    /** The last failure, in the words the server or the network used. Null once something works. */
    var lastError: String? by mutableStateOf(null)
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
    internal fun reset(now: Long = System.currentTimeMillis()) {
        // A run has STARTED, so these are now measured and their measured value is zero. That is a
        // different fact from "nobody has counted", which is what null means, and the screen renders
        // them differently on purpose.
        delivered = 0
        dropped = 0
        runStartedAtMillis = now
        countersAsOfMillis = now
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
     * @param queueDepth how many fixes are now waiting, or **null when the queue could not be
     *   read**. A queue that could not be listed is reported as unreadable rather than as empty:
     *   showing `0` while fixes sit on disk misrepresents the one thing the durable queue exists to
     *   guarantee, and showing `0` when nothing could be counted at all is worse still.
     */
    @Synchronized
    internal fun recordQueued(queueDepth: Int?, now: Long = System.currentTimeMillis()) {
        if (queueDepth == null) {
            readState = ReadState.UNREADABLE
            queued = null
            return
        }
        readState = ReadState.READ
        queued = queueDepth
        countersAsOfMillis = now
    }

    /**
     * Records the result of one flush of the durable queue.
     *
     * [lastError] is cleared only when something was delivered and nothing was lost — a flush that
     * delivered nine fixes and discarded one is not a clean bill of health.
     */
    @Synchronized
    internal fun recordFlush(
        delivered: Int,
        discarded: Int,
        queued: Int,
        reason: String?,
        now: Long = System.currentTimeMillis(),
    ) {
        this.delivered = (this.delivered ?: 0) + delivered
        this.dropped = (this.dropped ?: 0) + discarded
        this.queued = queued
        this.readState = ReadState.READ
        this.countersAsOfMillis = now
        if (this.runStartedAtMillis == null) this.runStartedAtMillis = now
        when {
            reason != null -> lastError = reason
            delivered > 0 && discarded == 0 -> lastError = null
        }
    }

    @Synchronized
    internal fun recordDropped(reason: String, now: Long = System.currentTimeMillis()) {
        dropped = (dropped ?: 0) + 1
        countersAsOfMillis = now
        lastError = reason
    }

    /** Records [count] fixes lost at once — evicted from a full queue, say. */
    @Synchronized
    internal fun recordDroppedBatch(count: Int, reason: String, now: Long = System.currentTimeMillis()) {
        if (count <= 0) return
        dropped = (dropped ?: 0) + count
        countersAsOfMillis = now
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

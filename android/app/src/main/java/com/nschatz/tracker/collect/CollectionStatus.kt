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
 * State is intentionally **not** persisted. It describes this process's run; after a restart the
 * honest answer is "nothing yet", not a stale number recovered from disk.
 */
object CollectionStatus {

    /** Whether the foreground service is currently running. */
    var running: Boolean by mutableStateOf(false)
        internal set

    /** Fixes the server accepted — `201 Created` or a `200` idempotent replay. Both arrived. */
    var delivered: Int by mutableIntStateOf(0)
        internal set

    /**
     * Fixes this process gave up on: refused outright, or still failing when the retry budget ran
     * out. **These are lost** — C1 has no durable queue, and pretending otherwise would be the
     * silent-loss failure (risk path #3) rather than a documented limitation. C2 is what makes this
     * counter stop being able to move for a transient outage.
     */
    var dropped: Int by mutableIntStateOf(0)
        internal set

    /** Epoch millis of the last fix the service handed to the reporter, or 0 if none yet. */
    var lastFixAtMillis: Long by mutableLongStateOf(0L)
        internal set

    /** The last failure, in the words the server or the network used. Null once something works. */
    var lastError: String? by mutableStateOf(null)
        internal set

    // The mutators are @Synchronized because they are genuinely called from more than one thread:
    // `recordDelivered` and `recordDropped` run on the reporter's background thread, while the
    // executor's rejection handler and the service lifecycle callbacks run on the main thread.
    // `delivered += 1` is a read-modify-write, so without this a concurrent drop and delivery can
    // lose an increment — on the one counter whose entire purpose is to be trustworthy about
    // whether the trail has holes in it. Compose's snapshot state makes the *visibility* safe; it
    // does not make the increment atomic.

    @Synchronized
    internal fun reset() {
        delivered = 0
        dropped = 0
        lastFixAtMillis = 0L
        lastError = null
    }

    @Synchronized
    internal fun recordDelivered() {
        delivered += 1
        lastError = null
    }

    @Synchronized
    internal fun recordDropped(reason: String) {
        dropped += 1
        lastError = reason
    }

    /** Records [count] fixes lost at once — a queue abandoned on shutdown, say. */
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

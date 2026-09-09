package com.nschatz.tracker.alert

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

/**
 * Everything the app shows about crossings, in one process-scoped place the screen observes.
 *
 * The same shape, and the same reasoning, as `collect/CollectionStatus`: the screen needs a handful
 * of facts, not a lifecycle, and the alternative is an app that looks identical whether alerts are
 * working or not.
 *
 * ### The privacy boundary this object holds
 *
 * It holds [Crossing]s, and a [Crossing] has nowhere to put a coordinate. That is the enforcement:
 * the alert surface cannot hold a latitude because there is no field for one, all the way from the
 * push payload and the crossings row through to what is rendered. Nothing here is persisted either -
 * this is what THIS RUN has seen, and after a restart the honest answer is "read it again from the
 * server", not a stale trail recovered from disk.
 *
 * The viewer credential is not here and must never be. It lives in `ClientPreferences`, is read at
 * the moment a call is made, and appears in no field of this object, no rendered string and no log
 * line.
 */
object AlertSurface {

    /**
     * The single alert-delivery state to show, or null while the first registration attempt is still
     * in flight. Null renders as "checking", NEVER as armed.
     */
    var status: AlertDeliveryStatus? by mutableStateOf(null)
        internal set

    /**
     * Crossings that arrived on this phone as pushes, this run.
     *
     * Kept separately from the read-back so the merge can be recomputed whenever either side changes,
     * and so a failed read never erases a crossing the phone already received.
     */
    internal val pushed = mutableStateListOf<Crossing>()

    /** The last crossings read, or null when none has resolved yet. */
    internal var lastRead: AlertClient.CrossingsResult? by mutableStateOf(null)

    /**
     * Pushes discarded because they did not carry a complete crossing.
     *
     * It is shown, not just counted. A push the app threw away is an alert a person did not get, and
     * a client that silently discards malformed messages is indistinguishable from one that is not
     * receiving any - which is exactly the ambiguity the rest of this surface exists to remove. It is
     * also observable off-device: [AlertIntake.receive] increments it, and the JVM tests drive that
     * with no Android in the loop.
     */
    var discardedPushes: Int by mutableIntStateOf(0)
        internal set

    /** The merged, deduplicated list and what to say about it. */
    internal val listView: CrossingListView
        get() = CrossingListView.of(read = lastRead, pushed = pushed.toList())

    @Synchronized
    internal fun recordPush(crossing: Crossing) {
        // Replace rather than append when the same crossing arrives twice (a push redelivered, say):
        // identity is (device, Place, instant), so this cannot collapse two real crossings.
        val existing = pushed.indexOfFirst { it.identity == crossing.identity }
        if (existing >= 0) {
            pushed[existing] = crossing
        } else {
            pushed.add(crossing)
        }
    }

    @Synchronized
    internal fun recordDiscardedPush() {
        discardedPushes += 1
    }

    @Synchronized
    internal fun recordRead(result: AlertClient.CrossingsResult) {
        lastRead = result
    }

    @Synchronized
    internal fun recordStatus(status: AlertDeliveryStatus?) {
        this.status = status
    }

    /** Clears this run's alert state. Used when the configuration changes under the app. */
    @Synchronized
    internal fun reset() {
        status = null
        pushed.clear()
        lastRead = null
        discardedPushes = 0
    }
}

/**
 * The one door a received push comes through.
 *
 * Pure, and separate from the Android `FirebaseMessagingService` that calls it, because this is where
 * the decision lives: parse, and either render or discard-and-count. The service around it does
 * nothing but hand over the title and the data map, which is what makes A8's discard count provable
 * by a test that never touches a device.
 */
object AlertIntake {

    /**
     * Handles one received push.
     *
     * @return the crossing to render a notification for, or null when the payload was incomplete -
     *   in which case the discard has been counted and the caller must render NOTHING. Not a partial
     *   notification, not a guessed name: a message about a child's movements that the app had to
     *   invent half of is worse than the silence the crossing list can recover from.
     */
    fun receive(title: String?, data: Map<String, String?>): Crossing? {
        val crossing = PushPayload.parse(title, data)
        if (crossing == null) {
            AlertSurface.recordDiscardedPush()
            return null
        }
        AlertSurface.recordPush(crossing)
        return crossing
    }
}

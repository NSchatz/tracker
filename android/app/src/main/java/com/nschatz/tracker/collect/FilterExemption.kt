package com.nschatz.tracker.collect

/** What to do with a position offered to an open [FilterExemption]. */
enum class RestartOffer {

    /** This is the restart position. Send it, unfiltered. The exemption is now closed. */
    ACCEPT,

    /** Not the restart position. Nothing is sent, and the exemption is left as it was. */
    IGNORE,
}

/**
 * The permission to deliver **one** position without applying the 25 m displacement filter.
 *
 * ### Why one position matters this much
 *
 * A phone under the displacement filter emits nothing while it sits still, which is correct and is
 * the single biggest battery lever this client has. It also means "collection restarted after the
 * reboot" and "collection did not restart" look identical from the server: both are silence. One
 * position, taken after the restart and delivered without waiting for the device to move, is what
 * tells them apart. It is the reason the reboot behaviour is verifiable at all.
 *
 * ### The rules, and they are exact
 *
 * - It **opens** at each transition into collecting - a boot-driven start, an operator start, a
 *   platform-driven recreation of the service; anything that goes from not collecting to collecting.
 * - It **closes** on whichever comes first: the restart position for that transition being taken,
 *   collection stopping, or the next transition (which opens a fresh one).
 * - At most one is open at a time, and an open one never covers more than one position.
 * - A position the provider took **before** the transition is never the restart position, however
 *   convenient it would be. The entire purpose is to show the device is alive NOW; a cached fix from
 *   before the reboot shows the opposite and would be indistinguishable from the stale position the
 *   family map is already drawing.
 * - [reasonDue] marks five minutes without one. That mark changes **only what the operator is
 *   told**: it does not close the exemption, does not cancel the obligation, and does not subject
 *   the position eventually obtained to the displacement filter. A phone in a car park for twenty
 *   minutes still delivers its one exempt position when it finally sees the sky.
 *
 * ### What it is not
 *
 * It is not a cadence change and it is not a second filter. Every position that follows the accepted
 * one goes through the unchanged steady-state stream - see [CollectionRequests] - so a stationary
 * device falls silent again immediately afterwards, exactly as it does at the pin.
 *
 * Mutators are `@Synchronized`: the exemption is opened on the service's start path and offered
 * positions from a location callback, and although both run on the main looper today, a
 * read-modify-write on the one field that decides whether an unfiltered position goes out is not
 * something to leave to that remaining true.
 */
class FilterExemption {

    /** When the transition this exemption belongs to happened, or null when it is closed. */
    private var openedAtMillis: Long? = null

    /** Whether an exemption is open right now. */
    val isOpen: Boolean
        @Synchronized get() = openedAtMillis != null

    /**
     * Opens an exemption for a transition into collecting that happened at [transitionAtMillis].
     *
     * Opening replaces any exemption already open, which is the "next transition closes it" rule:
     * the previous transition's obligation lapses with it and nothing is delivered retrospectively
     * for it.
     */
    @Synchronized
    fun open(transitionAtMillis: Long) {
        openedAtMillis = transitionAtMillis
    }

    /**
     * Offers a position taken at [positionTakenAtMillis] (wall-clock epoch millis, as the platform
     * timestamps a location) as this transition's restart position.
     *
     * Returns [RestartOffer.ACCEPT] exactly once per open exemption, and closes it in the same step
     * so a burst of positions from the provider cannot produce two.
     *
     * A position stamped before the transition is IGNOREd and the exemption **stays open**, so the
     * next one is still considered. That is what makes "the first position it subsequently obtains"
     * mean the first qualifying one, rather than the first one the provider happens to hand over.
     */
    @Synchronized
    fun offer(positionTakenAtMillis: Long): RestartOffer {
        val openedAt = openedAtMillis ?: return RestartOffer.IGNORE
        if (positionTakenAtMillis < openedAt) return RestartOffer.IGNORE
        openedAtMillis = null
        return RestartOffer.ACCEPT
    }

    /**
     * Closes the exemption without delivering anything.
     *
     * Called when collection stops. The obligation lapses with that transition: nothing is delivered
     * retrospectively for it, and the next transition starts afresh.
     */
    @Synchronized
    fun close() {
        openedAtMillis = null
    }

    /**
     * Whether the operator should now be told that no restart position could be taken.
     *
     * True only while the exemption is still open and [REASON_AFTER_MILLIS] has passed since the
     * transition. Deliberately a query rather than a state change: nothing about this mark alters
     * what the exemption does.
     */
    @Synchronized
    fun reasonDue(nowMillis: Long): Boolean {
        val openedAt = openedAtMillis ?: return false
        return nowMillis - openedAt >= REASON_AFTER_MILLIS
    }

    companion object {

        /**
         * Five minutes.
         *
         * Chosen against the ten-minute window the reboot check allows, so that a device which
         * cannot get a position reports **that** rather than looking like one that never restarted.
         * It is not a claim about how long a position provider takes.
         */
        const val REASON_AFTER_MILLIS: Long = 5 * 60 * 1000L
    }
}

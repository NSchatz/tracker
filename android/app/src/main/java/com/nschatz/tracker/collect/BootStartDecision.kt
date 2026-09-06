package com.nschatz.tracker.collect

import com.nschatz.tracker.permission.CollectionCapability

/** What the boot receiver should do with this boot. */
sealed interface BootAction {

    /** Start the location foreground service. No human action is involved or required. */
    data object Start : BootAction

    /**
     * Do not start collection.
     *
     * @param reason the case to record, or null when there is nothing to explain. Intent OFF is the
     *   only null: a device whose operator turned collection off is behaving exactly as asked, and
     *   writing a reason for it would put a complaint in front of somebody who made a choice.
     */
    data class DoNotStart(val reason: ReasonCase?) : BootAction
}

/**
 * Whether a boot restarts collection, and if not, which case is recorded.
 *
 * Pure, and it is the whole of the boot receiver's judgement: the receiver reads the intent off
 * disk, reads the grants off the OS, calls this, and does what it says. That split is what lets the
 * boot behaviour - the part of this app a headless build has the least chance of exercising - be
 * asserted by the gate rather than only by rebooting a phone.
 */
object BootStartDecision {

    /**
     * @param reading what was found where the collection intent is stored.
     * @param capability what location this app can actually collect right now, from
     *   [com.nschatz.tracker.permission.LocationPermissionFlow.capability].
     *
     * **The intent is checked before the permission, deliberately.** Both a missing permission and
     * an unreadable intent stop the start, only one reason is held, and the intent is the more
     * fundamental of the two: if the app cannot tell whether the operator wanted collection at all,
     * saying that a permission is missing answers a question nobody asked. A device with intent OFF
     * and no location permission is therefore silent rather than complaining about a permission it
     * does not need.
     *
     * `FOREGROUND_ONLY` refuses for the same reason `NONE` does. Android will not create a
     * `location` foreground service from the background without `ACCESS_BACKGROUND_LOCATION`, and a
     * boot receiver is the background - so a foreground-only grant is exactly as unable to restart
     * collection as no grant at all, and the reason names the permission that is actually missing.
     */
    fun decide(reading: IntentReading, capability: CollectionCapability): BootAction = when (reading) {
        is IntentReading.Missing -> BootAction.DoNotStart(ReasonCase.COLLECTION_INTENT_MISSING)
        is IntentReading.Unreadable -> BootAction.DoNotStart(ReasonCase.COLLECTION_INTENT_UNREADABLE)
        is IntentReading.Known -> when (reading.intent) {
            CollectionIntent.OFF -> BootAction.DoNotStart(reason = null)
            CollectionIntent.ON -> when (capability) {
                CollectionCapability.CONTINUOUS -> BootAction.Start
                CollectionCapability.FOREGROUND_ONLY, CollectionCapability.NONE ->
                    BootAction.DoNotStart(ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED)
            }
        }
    }
}

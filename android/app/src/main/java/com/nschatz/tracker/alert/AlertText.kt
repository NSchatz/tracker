package com.nschatz.tracker.alert

/**
 * The words the alert surface uses, decided in pure Kotlin so they are checkable in the gate.
 *
 * Everything here is built from the family's OWN labels and fixed English connectives. There is no
 * branch that can reach a coordinate, because [Crossing] has no coordinate to reach.
 */
internal object AlertText {

    /**
     * The placeholder shown where a crossing carries no device name.
     *
     * Chosen to be visibly NOT a name a family would pick: square brackets and lower case, so nobody
     * reads it as a device called "Unnamed device". A server older than this phase's crossings-row
     * addition does not carry the name, and the honest thing is to show the Place, the direction and
     * the time and say plainly that the name is missing - not to invent one.
     */
    const val UNNAMED_DEVICE: String = "[device name not sent by this server]"

    /** The device's name, or the placeholder. Never a guess. */
    fun deviceLabel(crossing: Crossing): String = crossing.deviceName ?: UNNAMED_DEVICE

    /** The notification's title: the device's name, which is what a push carries it as. */
    fun notificationTitle(crossing: Crossing): String = deviceLabel(crossing)

    /**
     * The notification's body: the device, the direction and the Place, in the family's words.
     *
     * It matches the sentence the server builds for the same crossing ("Alice's phone arrived at
     * School"), so a notification rendered by this app and one rendered by a backend's own fallback
     * read the same way round.
     */
    fun notificationBody(crossing: Crossing): String =
        deviceLabel(crossing) + " " + verb(crossing.transition) + " " + crossing.placeName

    /** One row of the in-app list: the same sentence the notification uses. */
    fun listRow(crossing: Crossing): String = notificationBody(crossing)

    private fun verb(transition: Transition): String = when (transition) {
        Transition.ENTER -> "arrived at"
        Transition.EXIT -> "left"
    }
}

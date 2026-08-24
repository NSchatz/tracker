package com.nschatz.tracker.alert

import java.time.DateTimeException
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

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

    /**
     * One row of the in-app list: the notification's sentence, plus WHEN it happened.
     *
     * The time is not decoration and the row is not the notification. A notification arrives at the
     * moment of the crossing, so a person reading one already knows when; the list is read later and
     * exists to make a crossing the push backend dropped findable, which it cannot do if every row
     * for one device at one Place reads the same. The identity that keeps two such crossings two
     * rows is (device, Place, instant) - so without the instant on the row, "Alice's phone left
     * School" this morning and "Alice's phone left School" this afternoon are two rows a person
     * cannot tell apart. A28 requires it in as many words: the Place, the direction AND the time.
     *
     * @param zone which clock to render in. Defaults to the phone's, which is the one the person
     *   holding it reasons in; a test pins it so the rendering is deterministic wherever it runs.
     */
    fun listRow(crossing: Crossing, zone: ZoneId = ZoneId.systemDefault()): String =
        notificationBody(crossing) + " (" + timeLabel(crossing, zone) + ")"

    /**
     * The crossing's instant, as a person reads a clock.
     *
     * Total by construction: a crossing whose ts is outside the range a calendar can render still
     * gets its time shown, as the raw epoch seconds it arrived as. That is a server sending
     * nonsense, and the honest response is to show the nonsense rather than to throw while drawing a
     * list - a row that crashes the surface would take every OTHER crossing off the screen with it.
     */
    fun timeLabel(crossing: Crossing, zone: ZoneId = ZoneId.systemDefault()): String = try {
        ROW_TIME.format(Instant.ofEpochSecond(crossing.instantSeconds).atZone(zone))
    } catch (e: DateTimeException) {
        "epoch " + crossing.instantSeconds
    } catch (e: ArithmeticException) {
        "epoch " + crossing.instantSeconds
    }

    /**
     * `Locale.ROOT` so the digits are the same everywhere the app runs: the pattern is entirely
     * numeric, and a locale-dependent numbering system would make the row unreadable to a test and
     * inconsistent with the `ts` an operator sees on the wire.
     */
    private val ROW_TIME: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm", Locale.ROOT)

    private fun verb(transition: Transition): String = when (transition) {
        Transition.ENTER -> "arrived at"
        Transition.EXIT -> "left"
    }
}

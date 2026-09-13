package com.nschatz.tracker.collect

import com.nschatz.tracker.permission.CollectionCapability

/**
 * Why a boot did not restart collection, in a closed vocabulary.
 *
 * Each member carries the two things the surface needs and nothing else: the [TroubleKind] the
 * collection card names in a few words, and the sentence the explanation destination reads. Both are
 * committed literals here.
 *
 * That shape is what makes the privacy bar structural rather than a rule somebody has to remember.
 * A reason is chosen from this set, so the only text that can ever reach the record is text
 * committed in this file - a coordinate, a device token or a viewer token has no route in, even when
 * the caller is holding one. A design that passed the failure's own sentence through would have had
 * exactly that route, because [ConfigStatus.Incomplete.reason] is a string the caller supplies.
 *
 * @param kind what the card draws, from the closed set the screen's exhaustive `when` maps.
 * @param sentence what the explanation destination reads. It names the missing prerequisite and what
 *   to do about it, and it names nothing about where the phone is or what it authenticates with.
 */
enum class BootRestartReason(val kind: TroubleKind, val sentence: String) {

    /**
     * `ACCESS_BACKGROUND_LOCATION` is not granted, so the platform will not create a `location`
     * foreground service from a boot receiver - which is the background.
     */
    BACKGROUND_LOCATION_NOT_GRANTED(
        TroubleKind.BACKGROUND_LOCATION_MISSING,
        "Collection did not restart after the phone rebooted: \"Allow all the time\" location " +
            "access is not granted, and Android will not start location collection in the " +
            "background without it. Grant it on this app's settings page, then start collection " +
            "again from this screen.",
    ),

    /**
     * The stored server URL or device token is missing or unusable, so there is nowhere to report to.
     */
    SERVER_CONFIG_UNUSABLE(
        TroubleKind.NOT_CONFIGURED,
        "Collection did not restart after the phone rebooted: the server settings on this phone " +
            "are incomplete, so there is nowhere to report to. Check them under server settings, " +
            "then start collection again from this screen.",
    ),
    ;

    companion object {
        /** The member with this name, or null for an unrecognised or absent one. */
        fun named(name: String?): BootRestartReason? = entries.firstOrNull { it.name == name }
    }
}

/** What the boot path does with this boot. */
sealed interface BootRestartAction {

    /** Start the location foreground service. Nobody touches the phone for this to happen. */
    data object Start : BootRestartAction

    /**
     * Leave collection stopped.
     *
     * @param reason what to record, or null when there is nothing to explain. A person who turned
     *   collection off is the only null: the phone is doing exactly what was asked of it, and
     *   putting a complaint on the screen for it would report a choice as a fault.
     */
    data class DoNotStart(val reason: BootRestartReason?) : BootRestartAction
}

/**
 * The boot-restart decision, as a pure function of three inputs.
 *
 * ### Why it is pure, and why that is the only shape available
 *
 * A `BroadcastReceiver` is not something a headless build can construct, deliver an intent to, or
 * observe. The JVM unit-test classpath here is `junit` alone - no Robolectric, no mocking framework -
 * so the choice is not "test the receiver somehow" but "move the judgement out of the receiver".
 * Everything below reasons about plain data, exactly as `permission/LocationPermissionFlow` does, and
 * the receiver is left with reading three values and doing what it is told.
 *
 * ### The three inputs are the whole input set
 *
 * The persisted ask, the grants and the stored configuration. Nothing else is read: no build flag, no
 * intent extra, no system property, no clock. That is asserted rather than asserted-by-comment -
 * `BootRestartDecisionTest` reads this function's parameter list by reflection and covers the cross
 * product of the three - because a fourth input by which a grading route could turn collection on
 * would be a way to start reporting a family's location without anyone asking.
 */
object BootRestartDecision {

    /**
     * @param collectionEnabled the persisted ask, from `ClientPreferences.collectionEnabled`.
     * @param capability what location can actually be collected now, from
     *   `LocationPermissionFlow.capability`.
     * @param config what the stored server settings validate to, from
     *   `ClientPreferences.readConfig`.
     *
     * **The ask is read before anything else, deliberately.** A phone whose owner turned collection
     * off is not owed a complaint about a permission it does not need, and a reason recorded in that
     * state would be an answer to a question nobody asked.
     *
     * **`FOREGROUND_ONLY` refuses exactly as `NONE` does.** Android will not create a `location`
     * foreground service from the background without `ACCESS_BACKGROUND_LOCATION`, and a boot
     * receiver is the background, so a foreground-only grant is as unable to restart collection as
     * no grant at all. Attempting it and swallowing the exception would be the same outcome with the
     * reason lost.
     *
     * **The configuration is checked before the service is asked for, not after.** The service's own
     * fail-safe already refuses to run on an unusable config - but it refuses by posting a
     * notification and stopping itself, which on a boot nobody is watching is a notification for a
     * phone that was never going to collect. Deciding it here means the reason is recorded and
     * nothing is started.
     */
    fun decide(
        collectionEnabled: Boolean,
        capability: CollectionCapability,
        config: ConfigStatus,
    ): BootRestartAction {
        if (!collectionEnabled) return BootRestartAction.DoNotStart(reason = null)
        if (capability != CollectionCapability.CONTINUOUS) {
            return BootRestartAction.DoNotStart(BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED)
        }
        if (config !is ConfigStatus.Configured) {
            return BootRestartAction.DoNotStart(BootRestartReason.SERVER_CONFIG_UNUSABLE)
        }
        return BootRestartAction.Start
    }
}

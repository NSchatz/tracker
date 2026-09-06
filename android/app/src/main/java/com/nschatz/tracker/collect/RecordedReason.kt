package com.nschatz.tracker.collect

/**
 * The case a **recorded reason** names.
 *
 * A recorded reason is a durable, human-readable statement of the most recent case in which an
 * automatic start did not happen, did not succeed, or a restart position could not be taken while
 * collection runs. **At most one is held.** It is written when the case occurs, replaced by a later
 * case, and cleared only when the condition it names stops holding - see [RecordedReasons.clearedBy].
 *
 * The CASE is what is persisted, never the sentence. Two reasons: the sentence can be reworded
 * without a migration, and a stored sentence cannot be reasoned about (the clearing rules below are
 * a function of the case, and a free-text blob would have to be pattern-matched back into one).
 */
enum class ReasonCase {

    /**
     * Collection was not started after the boot because background location is not granted.
     *
     * Android does not permit creating a `location` foreground service from the background without
     * `ACCESS_BACKGROUND_LOCATION`, so a boot receiver that tried anyway would throw. Refusing
     * before the attempt, and saying why, is the required behaviour rather than a nicety.
     */
    BACKGROUND_LOCATION_NOT_GRANTED,

    /** The platform refused the start: the foreground service could not be entered or driven. */
    PLATFORM_REFUSED_START,

    /** The automatic start failed for a reason that is not a platform refusal. */
    AUTOMATIC_START_FAILED,

    /**
     * The start stopped on the client's own fail-safe: there is no usable server URL or token.
     *
     * The service has always refused to run in this state rather than post to nowhere. What is new
     * is that the refusal is DURABLE, because an automatic start after a boot can hit it in a
     * process that is reclaimed long before anybody opens the app - and a silence whose only
     * explanation died with that process is read as a force-stop, which is a confident wrong
     * diagnosis of a problem the operator could fix in ten seconds.
     */
    SERVER_NOT_CONFIGURED,

    /** Nothing was stored where the collection intent lives, so collection was not started. */
    COLLECTION_INTENT_MISSING,

    /** Something other than the two intent values was stored there, so collection was not started. */
    COLLECTION_INTENT_UNREADABLE,

    /**
     * Collection is running but no position taken at or after the transition has been obtained yet,
     * five minutes on.
     *
     * The one case that coexists with collection **running**. It exists so that a phone which
     * restarted correctly but cannot see the sky reports that, instead of being indistinguishable
     * from one that never restarted at all.
     */
    NO_RESTART_POSITION_YET,
}

/** Something that has just happened, which may mean a held reason no longer holds. */
enum class ReasonClearingEvent {

    /** A collection session actually started collecting. */
    START_SUCCEEDED,

    /** The restart position for the current transition was taken. */
    RESTART_POSITION_OBTAINED,

    /** Collection stopped, whatever stopped it. */
    COLLECTION_STOPPED,

    /** The operator set the collection intent to OFF. */
    INTENT_TURNED_OFF,
}

/**
 * The sentences a recorded reason is shown as, and the rule for when one stops holding.
 *
 * Pure, and therefore provable: the clearing rule is the part with an actual invariant in it, and
 * getting it wrong is how an app ends up showing a week-old complaint about a reboot that has since
 * been fixed - or, worse, clearing a live complaint and looking healthy while it is not.
 */
object RecordedReasons {

    /**
     * The sentence shown to the operator, naming which case occurred.
     *
     * These live in Kotlin rather than `strings.xml` for the same reason `ConfigValidation`'s do:
     * they are produced by pure code the gate can assert, and a string resource needs a `Context`,
     * which would push the one part of this that is testable behind the device boundary.
     */
    fun message(case: ReasonCase): String = when (case) {
        ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED ->
            "Collection did not restart after the reboot: background location (\"Allow all the " +
                "time\") is not granted, and Android does not allow starting location collection " +
                "from a boot receiver without it. Grant it in this app's settings, then start " +
                "collection here."

        ReasonCase.PLATFORM_REFUSED_START ->
            "Collection did not start: Android refused the location foreground service, or the " +
                "location permission was withdrawn while it was starting. Check that location " +
                "permission is still granted, then start collection here."

        ReasonCase.AUTOMATIC_START_FAILED ->
            "Collection did not restart after the reboot: the automatic start failed. Start " +
                "collection here to try again."

        ReasonCase.SERVER_NOT_CONFIGURED ->
            "Collection did not start: this phone has no server address or no device token, so " +
                "there is nowhere to report to. Enter them above and start collection here."

        ReasonCase.COLLECTION_INTENT_MISSING ->
            "Collection did not restart after the reboot: no saved collection setting was found " +
                "on this phone, so there was nothing to resume. Start collection here and the " +
                "setting is saved for the next reboot."

        ReasonCase.COLLECTION_INTENT_UNREADABLE ->
            "Collection did not restart after the reboot: the saved collection setting could not " +
                "be read, and it is not guessed. Start or stop collection here to write it again."

        ReasonCase.NO_RESTART_POSITION_YET ->
            "Collection is running, but no position has been obtained since it started - five " +
                "minutes and counting. This phone may have no view of the sky. The position will " +
                "be sent as soon as one is available; nothing else is waiting on it."
    }

    /**
     * Whether [event] means the condition [case] names has stopped holding.
     *
     * The general rule is exactly that sentence; the table below is its application, case by case,
     * and each row has a reason:
     *
     * - A start that succeeds clears every start-side case - a refusal, a failure, and both intent
     *   cases, because a start can only have succeeded if the intent read as ON.
     * - A start that succeeds does **not** clear [ReasonCase.NO_RESTART_POSITION_YET]. That case
     *   exists precisely while collection runs, so clearing it on the event that starts collection
     *   would erase it the moment it could first be true.
     * - Obtaining the restart position clears [ReasonCase.NO_RESTART_POSITION_YET] and nothing else.
     * - Collection stopping clears [ReasonCase.NO_RESTART_POSITION_YET]: the obligation to deliver a
     *   restart position lapses with that transition, so the condition it names is over. It leaves
     *   a start-side case alone - stopping collection does not grant a permission.
     * - The operator turning the intent OFF clears everything. A device whose collection intent is
     *   simply off holds no recorded reason at all, so there is nothing left to present.
     */
    fun clearedBy(case: ReasonCase, event: ReasonClearingEvent): Boolean = when (event) {
        ReasonClearingEvent.INTENT_TURNED_OFF -> true
        ReasonClearingEvent.START_SUCCEEDED -> case != ReasonCase.NO_RESTART_POSITION_YET
        ReasonClearingEvent.RESTART_POSITION_OBTAINED -> case == ReasonCase.NO_RESTART_POSITION_YET
        ReasonClearingEvent.COLLECTION_STOPPED -> case == ReasonCase.NO_RESTART_POSITION_YET
    }

    /**
     * Which case a throwable raised by an automatic start names.
     *
     * `startForegroundService` and `startForeground` refuse in several unrelated ways that share no
     * common subclass below `RuntimeException`:
     *
     * - `SecurityException` - a location runtime permission is missing or was just revoked.
     * - `ForegroundServiceStartNotAllowedException` (API 31+, extends `IllegalStateException`) - the
     *   service was started from the background in a state the platform does not permit.
     * - `MissingForegroundServiceTypeException` / `InvalidForegroundServiceTypeException` (API 34,
     *   both extend `IllegalArgumentException`) - the declared type is absent or not permitted.
     *
     * The first two are the platform saying no; anything else is a failure whose shape this code
     * does not recognise, and calling that a refusal would be a guess. Both are caught, neither
     * crashes, and the two are told apart here - in plain JDK types, so the classification is
     * provable by the gate rather than only on a handset.
     */
    fun refusalReason(error: Throwable): ReasonCase = when (error) {
        is SecurityException -> ReasonCase.PLATFORM_REFUSED_START
        is IllegalStateException -> ReasonCase.PLATFORM_REFUSED_START
        else -> ReasonCase.AUTOMATIC_START_FAILED
    }
}

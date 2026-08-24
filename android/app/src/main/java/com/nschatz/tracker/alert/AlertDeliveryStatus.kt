package com.nschatz.tracker.alert

/**
 * The one thing the app says about whether alerts are working.
 *
 * Exactly six states, chosen by the ordered procedure in [AlertStatusPolicy.evaluate]. They exist
 * because "no alerts have arrived" has at least six different causes and a person cannot act on any
 * of them without being told which one it is - and because the failure this whole phase exists to
 * prevent is an app that looks armed while nothing will ever reach it.
 *
 * Every one of them is distinguishable from an EMPTY CROSSING LIST, which is a seventh, unrelated
 * fact: a family that genuinely has not crossed anything today.
 */
sealed interface AlertDeliveryStatus {

    /** No viewer credential has been entered yet, so nothing has been attempted. */
    data object NotConfigured : AlertDeliveryStatus

    /** Permission to post notifications is not granted, so an alert cannot be shown even if it arrives. */
    data object CannotShow : AlertDeliveryStatus

    /** The server refused the registration - the credential is wrong or revoked. */
    data object NotRegisteredRefused : AlertDeliveryStatus

    /**
     * The registration never reached the server. Says nothing about the credential.
     *
     * Two ways to get here and they are the same fact: the request was made and no answer came
     * back, or the stored server URL is not one a request can be made to at all (unset, no host, or
     * plaintext `http://` in a build that refuses it). Both are "this phone did not reach the
     * server", and both are fixed at the same place on the screen.
     */
    data object NotRegisteredUnreachable : AlertDeliveryStatus

    /**
     * Registration succeeded, and alerts still will not arrive. [reason] says which of the four ways
     * that can be true applies - they are four different things to go and fix.
     */
    data class NotReceivable(val reason: NotReceivableReason) : AlertDeliveryStatus

    /** Everything needed is in place. Reached only by the last step of the procedure. */
    data object Armed : AlertDeliveryStatus
}

/** The four distinct reasons an accepted registration still cannot receive. */
enum class NotReceivableReason {
    /**
     * **R1** - this phone has no usable push configuration, so there is no routing address to
     * register at all. The ordinary cause is an app built with no Firebase project configuration
     * (which is exactly how this repo's gate builds it), or a device without the Play services this
     * client's push path needs.
     */
    NO_ROUTING_ADDRESS,

    /** **R2** - the server accepted the registration and reports that it has configured no push backend. */
    NO_CONFIGURED_PROVIDER,

    /**
     * **R3** - the server named a backend this app cannot receive from. A deployment configured for
     * UnifiedPush is the live case: the server will send, and this client has no distributor path,
     * so the alert would vanish between them. It is named rather than hidden.
     */
    PROVIDER_NOT_RECEIVABLE,

    /**
     * **R4** - the accepted response carried no provider field at all, so this server predates the
     * addition that reports one. The app cannot tell whether a backend is configured, and says so
     * instead of assuming the favourable answer.
     */
    SERVER_DOES_NOT_REPORT,
}

/** What the server said when the app tried to register this phone's endpoint. */
sealed interface RegistrationOutcome {

    /** The request never reached the server. */
    data object Unreachable : RegistrationOutcome

    /** The server answered, and refused. */
    data object Refused : RegistrationOutcome

    /** The server accepted and stored the registration, reporting [configuredProvider]. */
    data class Accepted(val configuredProvider: ConfiguredProvider) : RegistrationOutcome
}

/** What an accepted registration said about the DEPLOYMENT's own push backend. */
sealed interface ConfiguredProvider {

    /** The response carried no provider field: a server older than this phase's addition (R4). */
    data object Absent : ConfiguredProvider

    /** The response reported, explicitly, that the deployment has configured no backend (R2). */
    data object None : ConfiguredProvider

    /** The response named a backend. */
    data class Named(val provider: String) : ConfiguredProvider
}

/**
 * Everything the ordered procedure needs, and nothing else.
 *
 * @param viewerCredentialPresent whether a viewer credential has been entered at all.
 *
 *   Strictly the CREDENTIAL, and not "the app has a usable server configuration". The two used to be
 *   read off one validation, and the cost was a lie on the screen: a viewer token pasted beside a
 *   blank or plaintext-`http://` server URL reported "no viewer token yet", pointing the person at
 *   the one field they had already filled in. What a person is told to fix has to be what is
 *   actually broken.
 * @param serverUrlUsable whether the stored server URL is one a request can be made to at all. When
 *   it is not, no registration can be attempted and none is: see [AlertStatusPolicy.evaluate] for
 *   which of A23's six states that produces and why it is not a seventh.
 * @param notificationsPermitted whether this app may post a notification.
 * @param routingAddress the address this phone can be reached at, or **null** when none can be
 *   obtained here.
 * @param registration the outcome of the registration attempt, or **null** when no attempt has
 *   resolved yet. See [AlertStatusPolicy.evaluate] for what null produces, and what it must not.
 */
data class AlertStatusInputs(
    val viewerCredentialPresent: Boolean,
    val serverUrlUsable: Boolean,
    val notificationsPermitted: Boolean,
    val routingAddress: String?,
    val registration: RegistrationOutcome?,
)

/**
 * The ordered procedure that picks exactly one [AlertDeliveryStatus].
 *
 * Pure, and therefore actually testable: every branch below is reachable from a JVM unit test with no
 * device, no permission dialog and no push backend, which is the only way this many states get
 * checked at all rather than three of them being checked and the rest being hoped for.
 */
object AlertStatusPolicy {

    /** The one push backend this app has a receive path for. */
    const val RECEIVABLE_PROVIDER: String = "fcm"

    /**
     * Runs the procedure.
     *
     * The ORDER is the contract, not an implementation detail, and it is ordered by what a person can
     * act on first: a missing credential before a missing permission, a missing permission before a
     * missing routing address, and every failure before `armed`.
     *
     * @return the single state to show, or **null** when no registration attempt has resolved yet.
     *
     *   Null is not a seventh state and is never rendered as one - it is the absence of an answer,
     *   and the screen says it is still checking. It exists because the alternative is worse: with a
     *   registration still in flight, nothing has been refused, nothing was unreachable and no
     *   accepted response has been read, so a total function would fall through to `armed` and the
     *   app would claim alerts work before it had any evidence they do. Erring toward "no answer
     *   yet" is the fail-safe direction; erring toward `armed` is the exact failure this phase
     *   exists to close.
     */
    fun evaluate(inputs: AlertStatusInputs): AlertDeliveryStatus? {
        // Step 0. No credential: nothing has been attempted, and nothing should be.
        if (!inputs.viewerCredentialPresent) return AlertDeliveryStatus.NotConfigured

        // Step 1. An alert that cannot be shown is not an alert, whatever the rest of the chain says.
        if (!inputs.notificationsPermitted) return AlertDeliveryStatus.CannotShow

        // Step 2. Nothing to register: this phone has no usable push configuration.
        if (inputs.routingAddress.isNullOrBlank()) {
            return AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_ROUTING_ADDRESS)
        }

        // Step 3, first case. A server URL that cannot be requested against - unset, no host, or
        // plaintext http:// in a build that refuses it - is a registration that could not reach the
        // server, which is exactly what A23's step 3 names. It is deliberately NOT `not-configured`:
        // the credential IS entered, and a screen that says otherwise sends the person to the wrong
        // field. It is deliberately not a seventh state either - A23 fixes the six, and this is a
        // way of being the fourth of them, not a new one. The direction is safe by construction: it
        // returns before anything that could read `armed`.
        if (!inputs.serverUrlUsable) return AlertDeliveryStatus.NotRegisteredUnreachable

        // Steps 3 to 6 all depend on what the registration attempt found out.
        return when (val outcome = inputs.registration) {
            null -> null
            RegistrationOutcome.Unreachable -> AlertDeliveryStatus.NotRegisteredUnreachable
            RegistrationOutcome.Refused -> AlertDeliveryStatus.NotRegisteredRefused
            is RegistrationOutcome.Accepted -> when (val configured = outcome.configuredProvider) {
                ConfiguredProvider.Absent ->
                    AlertDeliveryStatus.NotReceivable(NotReceivableReason.SERVER_DOES_NOT_REPORT)

                ConfiguredProvider.None ->
                    AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_CONFIGURED_PROVIDER)

                is ConfiguredProvider.Named ->
                    if (configured.provider == RECEIVABLE_PROVIDER) {
                        AlertDeliveryStatus.Armed
                    } else {
                        AlertDeliveryStatus.NotReceivable(NotReceivableReason.PROVIDER_NOT_RECEIVABLE)
                    }
            }
        }
    }
}

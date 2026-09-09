package com.nschatz.tracker.alert

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * The ordered procedure that picks exactly one alert delivery state.
 *
 * This is the whole reason the decision is pure. Nine distinguishable answers with an ORDER between
 * them cannot be checked by opening the app: reaching most of them needs a server that answers a
 * particular way, a permission in a particular state, or a phone with no push configuration, and a
 * human would check two of them and hope about the rest.
 */
class AlertStatusPolicyTest {

    private fun inputs(
        viewerCredentialPresent: Boolean = true,
        serverUrlUsable: Boolean = true,
        notificationsPermitted: Boolean = true,
        routingAddress: String? = "phone-address",
        registration: RegistrationOutcome? = RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")),
    ) = AlertStatusInputs(
        viewerCredentialPresent,
        serverUrlUsable,
        notificationsPermitted,
        routingAddress,
        registration,
    )

    @Test
    fun noViewerCredentialIsNotConfiguredAndNothingIsAttempted() {
        // Every LATER step is set to a state that would produce a different answer, so this proves
        // the ORDER and not just the mapping.
        val status = AlertStatusPolicy.evaluate(
            inputs(
                viewerCredentialPresent = false,
                notificationsPermitted = false,
                routingAddress = null,
                registration = RegistrationOutcome.Refused,
            ),
        )
        assertEquals(AlertDeliveryStatus.NotConfigured, status)
    }

    @Test
    fun aBlockedNotificationPermissionOutranksEverythingBelowIt() {
        val status = AlertStatusPolicy.evaluate(
            inputs(notificationsPermitted = false, routingAddress = null, registration = RegistrationOutcome.Refused),
        )
        assertEquals(AlertDeliveryStatus.CannotShow, status)
    }

    @Test
    fun noRoutingAddressIsReasonR1AndOutranksTheRegistrationOutcome() {
        for (address in listOf(null, "", "   ")) {
            val status = AlertStatusPolicy.evaluate(
                inputs(routingAddress = address, registration = RegistrationOutcome.Refused),
            )
            assertEquals(
                "[address=$address]",
                AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_ROUTING_ADDRESS),
                status,
            )
        }
    }

    @Test
    fun anUnreachableServerAndARefusedCredentialAreDifferentStates() {
        assertEquals(
            AlertDeliveryStatus.NotRegisteredUnreachable,
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Unreachable)),
        )
        assertEquals(
            AlertDeliveryStatus.NotRegisteredRefused,
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Refused)),
        )
        assertNotEquals(
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Unreachable)),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Refused)),
        )
    }

    /**
     * The four ways an ACCEPTED registration still cannot receive. Each is a different thing to go
     * and fix, which is why they are four reasons and not one message.
     */
    @Test
    fun anAcceptedRegistrationHasFourDistinctNotReceivableReasons() {
        assertEquals(
            "R2: the deployment configured no backend",
            AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_CONFIGURED_PROVIDER),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.None))),
        )
        assertEquals(
            "R3: the deployment sends through a backend this app cannot receive from",
            AlertDeliveryStatus.NotReceivable(NotReceivableReason.PROVIDER_NOT_RECEIVABLE),
            AlertStatusPolicy.evaluate(
                inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Named("unifiedpush"))),
            ),
        )
        assertEquals(
            "R4: the server does not report which backend it configured",
            AlertDeliveryStatus.NotReceivable(NotReceivableReason.SERVER_DOES_NOT_REPORT),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Absent))),
        )
        assertEquals(
            "R1: this phone has no address to register",
            AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_ROUTING_ADDRESS),
            AlertStatusPolicy.evaluate(inputs(routingAddress = null)),
        )
    }

    /**
     * The credential is entered; the SERVER URL is the thing that is wrong. That must not report
     * "no viewer token yet" - it points the person at the one field they already filled in - and it
     * must not be a seventh state either. It is "this phone did not reach the server", which is one
     * of A23's six and is where the URL is fixed.
     */
    @Test
    fun aTokenBesideAnUnusableServerUrlIsNotReportedAsNoTokenAtAll() {
        val status = AlertStatusPolicy.evaluate(
            inputs(viewerCredentialPresent = true, serverUrlUsable = false, registration = null),
        )
        assertNotEquals(
            "the token IS entered; saying it is not sends the person to the wrong field",
            AlertDeliveryStatus.NotConfigured,
            status,
        )
        assertEquals(AlertDeliveryStatus.NotRegisteredUnreachable, status)
    }

    /**
     * ...and it still cannot outrank the two steps above it, nor reach `armed`. An unusable URL is
     * step 3, so a blocked permission and a missing routing address are both still reported first.
     */
    @Test
    fun anUnusableServerUrlKeepsItsPlaceInTheOrderAndNeverReachesArmed() {
        assertEquals(
            AlertDeliveryStatus.CannotShow,
            AlertStatusPolicy.evaluate(inputs(serverUrlUsable = false, notificationsPermitted = false)),
        )
        assertEquals(
            AlertDeliveryStatus.NotReceivable(NotReceivableReason.NO_ROUTING_ADDRESS),
            AlertStatusPolicy.evaluate(inputs(serverUrlUsable = false, routingAddress = null)),
        )
        // Even handed a registration outcome that would otherwise arm it - which cannot happen for
        // real, since no request can be made - the unusable URL is answered first.
        assertNotEquals(
            AlertDeliveryStatus.Armed,
            AlertStatusPolicy.evaluate(
                inputs(
                    serverUrlUsable = false,
                    registration = RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")),
                ),
            ),
        )
        // And with no credential at all, step 0 still wins: a bad URL does not mask a missing token.
        assertEquals(
            AlertDeliveryStatus.NotConfigured,
            AlertStatusPolicy.evaluate(inputs(viewerCredentialPresent = false, serverUrlUsable = false)),
        )
    }

    @Test
    fun armedIsReachedOnlyWhenEverythingHolds() {
        assertEquals(
            AlertDeliveryStatus.Armed,
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")))),
        )
    }

    /**
     * The one that would be easy to get wrong and impossible to notice: with a registration still in
     * flight, nothing has been refused, nothing was unreachable and no accepted response has been
     * read - so a procedure that falls through would say ARMED before it had any evidence at all.
     */
    @Test
    fun aRegistrationThatHasNotResolvedYetIsNotArmed() {
        val status = AlertStatusPolicy.evaluate(inputs(registration = null))
        assertNull("an unresolved registration must produce no answer, and certainly not armed", status)
        assertNotEquals(AlertDeliveryStatus.Armed, status)
    }

    /**
     * Every state the procedure can produce is distinct from every other, and none of them is the
     * empty crossing list (which is a different fact entirely, and lives in [CrossingListState]).
     */
    @Test
    fun allNineOutcomesAreDistinguishable() {
        val produced = listOf(
            AlertStatusPolicy.evaluate(inputs(viewerCredentialPresent = false)),
            AlertStatusPolicy.evaluate(inputs(notificationsPermitted = false)),
            AlertStatusPolicy.evaluate(inputs(routingAddress = null)),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Unreachable)),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Refused)),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.None))),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Named("unifiedpush")))),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Absent))),
            AlertStatusPolicy.evaluate(inputs(registration = RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")))),
        )
        assertEquals("every outcome must be distinguishable: $produced", produced.size, produced.toSet().size)
    }
}

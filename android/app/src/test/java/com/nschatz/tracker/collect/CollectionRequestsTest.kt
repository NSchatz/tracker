package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/**
 * The two requests collection makes of the provider, and the boundary between them.
 *
 * These are the cases that hold the steady state still. The reboot work is allowed exactly ONE
 * exemption from the displacement filter - a single position per transition into collecting - and
 * the way that could go wrong without anybody noticing is a build that relaxes the filter and never
 * puts it back, turning a stationary phone into a continuous reporter. There is no device in CI to
 * catch that, so it is caught here.
 */
class CollectionRequestsTest {

    /**
     * The steady-state stream carries the pin's numbers, unchanged.
     *
     * The reporting interval is asserted against both the literal and the constant so that moving
     * the constant cannot quietly move the gate with it.
     */
    @Test
    fun theSteadyStateStreamKeepsThePinsIntervalAndDisplacementFilter() {
        val spec = CollectionRequests.steadyState()
        assertEquals("the reporting interval is unchanged from the pin", 60_000L, spec.intervalMillis)
        assertEquals(CollectionPolicy.DEFAULT_INTERVAL_MILLIS, spec.intervalMillis)
        assertEquals("the 25 m displacement filter is unchanged", 25f, spec.minUpdateDistanceMeters, 0f)
        assertEquals(CollectionPolicy.DEFAULT_MIN_DISTANCE_METERS, spec.minUpdateDistanceMeters, 0f)
        assertEquals(CollectionPolicy.DEFAULT_MIN_UPDATE_INTERVAL_MILLIS, spec.minUpdateIntervalMillis)
        assertEquals(CollectionPolicy.DEFAULT_MAX_UPDATE_DELAY_MILLIS, spec.maxUpdateDelayMillis)
    }

    /** The exemption, and it is exactly one number: a zero displacement filter on one request. */
    @Test
    fun theRestartRequestIsTheOneThingExemptFromTheDisplacementFilter() {
        val spec = CollectionRequests.restartPosition()
        assertEquals(
            "the restart position must not wait for the device to move",
            0f,
            spec.minUpdateDistanceMeters,
            0f,
        )
        assertEquals("no batching on the position that proves the device is alive now", 0L, spec.maxUpdateDelayMillis)
        assertEquals("no floor on how soon the first position may arrive", 0L, spec.minUpdateIntervalMillis)
        assertEquals(
            "the target cadence is not a lever this exemption touches",
            CollectionPolicy.DEFAULT_INTERVAL_MILLIS,
            spec.intervalMillis,
        )
    }

    /**
     * A position AFTER the restart position is subject to the displacement filter.
     *
     * The two halves of that sentence, asserted together because neither is worth much alone: once
     * the exemption has taken its one position it is closed, so nothing further is exempt; and the
     * only stream still running is the steady-state one, which carries the pin's 25 m filter. A
     * stationary device therefore falls silent again the moment its restart position is sent, which
     * is the behaviour the reboot work is required to leave exactly as it found it.
     */
    @Test
    fun positionsAfterTheRestartPositionAreGovernedByTheUnchangedDisplacementFilter() {
        val exemption = FilterExemption()
        val transitionAt = 1_700_000_000_000L
        exemption.open(transitionAt)

        assertEquals(RestartOffer.ACCEPT, exemption.offer(transitionAt + 1_000))

        // Everything after it: not exempt, so it goes through the steady-state request.
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt + 61_000))
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt + 121_000))
        assertFalse(exemption.isOpen)
        assertEquals(
            "and that request still filters at 25 m",
            25f,
            CollectionRequests.steadyState().minUpdateDistanceMeters,
            0f,
        )
    }

    /**
     * The exemption is not achieved by mutating the collection policy.
     *
     * Stated as a case because the cheap implementation - relax the shared policy, restore it later
     * - is the one that leaves a phone unfiltered whenever a restore path is missed. The policy
     * this app collects with has to be the pin's, before and after.
     */
    @Test
    fun theCollectionPolicyItselfIsNeverRelaxedToProduceTheExemption() {
        val before = CollectionPolicy()
        val restart = CollectionRequests.restartPosition(before)
        val after = CollectionPolicy()
        assertEquals(25f, before.minUpdateDistanceMeters, 0f)
        assertEquals(25f, after.minUpdateDistanceMeters, 0f)
        assertEquals(0f, restart.minUpdateDistanceMeters, 0f)
        assertEquals(25f, CollectionRequests.steadyState(after).minUpdateDistanceMeters, 0f)
    }
}

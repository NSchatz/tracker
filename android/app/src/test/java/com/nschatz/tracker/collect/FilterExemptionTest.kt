package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The one unfiltered position per transition into collecting.
 *
 * Everything the reboot behaviour can be *observed* by rests on this class: a stationary phone under
 * the 25 m filter emits nothing, so "it restarted" and "it did not restart" are the same silence
 * until one position arrives to tell them apart. The rules below are therefore not conveniences,
 * they are the difference between a verifiable claim and an untestable one.
 */
class FilterExemptionTest {

    private val transitionAt = 1_700_000_000_000L

    @Test
    fun opensAtATransitionAndAcceptsAPositionTakenAtOrAfterIt() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)
        assertTrue(exemption.isOpen)
        assertEquals(RestartOffer.ACCEPT, exemption.offer(transitionAt))
        assertFalse("the exemption closes on the position it covers", exemption.isOpen)
    }

    /**
     * A position the provider took BEFORE the transition is never the restart position, and
     * rejecting it must not close the exemption - otherwise a phone handed one cached fix would
     * spend its whole exemption on the position it must not send and never deliver a real one.
     */
    @Test
    fun ignoresAPositionTakenBeforeTheTransitionAndStaysOpen() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt - 1))
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt - 60_000))
        assertTrue("a cached position must not spend the exemption", exemption.isOpen)
        assertEquals(RestartOffer.ACCEPT, exemption.offer(transitionAt + 1))
    }

    /** One position per transition, never two, however many the provider delivers in a burst. */
    @Test
    fun coversExactlyOnePosition() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)
        assertEquals(RestartOffer.ACCEPT, exemption.offer(transitionAt + 1_000))
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt + 2_000))
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt + 3_000))
    }

    /**
     * The five-minute mark changes only what the operator is told. It does not close the exemption,
     * does not cancel the obligation, and does not put the eventually-obtained position back under
     * the displacement filter. A phone in a car park for twenty minutes still delivers its one
     * exempt position when it finally sees the sky.
     */
    @Test
    fun theFiveMinuteMarkReportsAndChangesNothingElse() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)

        assertFalse(exemption.reasonDue(transitionAt))
        assertFalse(exemption.reasonDue(transitionAt + FilterExemption.REASON_AFTER_MILLIS - 1))
        assertTrue(exemption.reasonDue(transitionAt + FilterExemption.REASON_AFTER_MILLIS))
        assertTrue(exemption.reasonDue(transitionAt + 20 * 60 * 1000L))

        assertTrue("the mark must not close the exemption", exemption.isOpen)
        assertEquals(
            "a position obtained long after the mark is still the exempt restart position",
            RestartOffer.ACCEPT,
            exemption.offer(transitionAt + 20 * 60 * 1000L),
        )
        assertFalse(
            "and once taken there is nothing left to report",
            exemption.reasonDue(transitionAt + 25 * 60 * 1000L),
        )
    }

    /**
     * Collection stopping closes it. The obligation lapses with the transition it belonged to:
     * nothing is delivered retrospectively, so a position that arrives after the stop is not sent
     * unfiltered on behalf of a session that is over.
     */
    @Test
    fun collectionStoppingClosesItAndNothingIsDeliveredRetrospectively() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)
        exemption.close()
        assertFalse(exemption.isOpen)
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt + 1_000))
        assertFalse(exemption.reasonDue(transitionAt + FilterExemption.REASON_AFTER_MILLIS))
    }

    /**
     * The next transition opens a FRESH exemption - the case a per-boot latch gets wrong.
     *
     * An operator who turns collection off and on again inside one boot has made a second
     * transition, and it is owed its own restart position even though the phone has not moved and
     * has not rebooted. A build that remembered "this boot already sent one" would pass every
     * duplicate-signal check and fail exactly here.
     */
    @Test
    fun aLaterTransitionGetsItsOwnExemptionWithNoPerBootLatch() {
        val exemption = FilterExemption()

        exemption.open(transitionAt)
        assertEquals(RestartOffer.ACCEPT, exemption.offer(transitionAt + 1_000))

        val secondTransitionAt = transitionAt + 60_000
        exemption.open(secondTransitionAt)
        assertTrue(exemption.isOpen)
        assertEquals(
            "a second transition in the same boot is owed its own restart position",
            RestartOffer.ACCEPT,
            exemption.offer(secondTransitionAt + 500),
        )
    }

    /**
     * A new transition replaces an open exemption rather than adding one. At most one is open at a
     * time, and the previous transition's obligation lapses with it: a position stamped before the
     * NEW transition is no longer acceptable, even though the old exemption would have taken it.
     */
    @Test
    fun openingAgainReplacesAnOpenExemptionRatherThanStackingOne() {
        val exemption = FilterExemption()
        exemption.open(transitionAt)
        val secondTransitionAt = transitionAt + 60_000
        exemption.open(secondTransitionAt)

        assertEquals(RestartOffer.IGNORE, exemption.offer(secondTransitionAt - 1))
        assertEquals(RestartOffer.ACCEPT, exemption.offer(secondTransitionAt))
        assertFalse("still exactly one position", exemption.isOpen)
    }

    /** A closed exemption is inert: nothing is exempt when no transition is outstanding. */
    @Test
    fun aFreshExemptionIsClosedAndAcceptsNothing() {
        val exemption = FilterExemption()
        assertFalse(exemption.isOpen)
        assertEquals(RestartOffer.IGNORE, exemption.offer(transitionAt))
        assertFalse(exemption.reasonDue(Long.MAX_VALUE))
    }
}

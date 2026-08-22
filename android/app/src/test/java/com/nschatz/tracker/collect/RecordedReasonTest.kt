package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The single recorded reason: when it stops holding, and what a refusal is called.
 *
 * Both halves are places where the cheap answer is wrong in a way nobody would notice on a happy
 * path. Clearing too eagerly makes a live problem invisible; clearing too little leaves a week-old
 * complaint on the screen of a phone that is working perfectly. And reporting an unrecognised
 * failure as "the platform refused" would be this repo's least favourite thing: a confident wrong
 * answer about why something did not happen.
 */
class RecordedReasonTest {

    /**
     * A start that succeeds clears every start-side case - and does NOT clear the one that exists
     * precisely while collection runs. Clearing that one on the event that starts collection would
     * erase it at the only moment it could first be true.
     */
    @Test
    fun aSuccessfulStartClearsTheStartSideCasesAndNotTheRunningOne() {
        for (case in ReasonCase.values()) {
            val cleared = RecordedReasons.clearedBy(case, ReasonClearingEvent.START_SUCCEEDED)
            if (case == ReasonCase.NO_RESTART_POSITION_YET) {
                assertFalse("collection starting must not clear $case", cleared)
            } else {
                assertTrue("a start that succeeds clears $case", cleared)
            }
        }
    }

    /** Obtaining the restart position clears its own case and nothing else. */
    @Test
    fun obtainingTheRestartPositionClearsOnlyItsOwnCase() {
        for (case in ReasonCase.values()) {
            assertEquals(
                "restart position obtained, holding $case",
                case == ReasonCase.NO_RESTART_POSITION_YET,
                RecordedReasons.clearedBy(case, ReasonClearingEvent.RESTART_POSITION_OBTAINED),
            )
        }
    }

    /**
     * Collection stopping ends the obligation to deliver a restart position, so the case that names
     * it stops holding. It does not grant a permission, so a refusal survives - the operator still
     * needs to see it the next time they open the app.
     */
    @Test
    fun collectionStoppingClearsTheRestartCaseAndLeavesARefusalStanding() {
        assertTrue(
            RecordedReasons.clearedBy(
                ReasonCase.NO_RESTART_POSITION_YET,
                ReasonClearingEvent.COLLECTION_STOPPED,
            ),
        )
        assertFalse(
            RecordedReasons.clearedBy(
                ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED,
                ReasonClearingEvent.COLLECTION_STOPPED,
            ),
        )
    }

    /** Intent OFF clears everything: that device holds no recorded reason at all. */
    @Test
    fun turningTheIntentOffClearsEveryCase() {
        for (case in ReasonCase.values()) {
            assertTrue(case.name, RecordedReasons.clearedBy(case, ReasonClearingEvent.INTENT_TURNED_OFF))
        }
    }

    /**
     * The refusals Android actually raises, told apart from a failure this code does not recognise.
     *
     * `SecurityException` (a missing or withdrawn location permission) and
     * `IllegalStateException` (`ForegroundServiceStartNotAllowedException` extends it) are the
     * platform saying no. Anything else is a failure whose shape is unknown here, and calling that
     * a refusal would be a guess dressed as a diagnosis.
     */
    @Test
    fun aPlatformRefusalIsToldApartFromAnUnrecognisedFailure() {
        assertEquals(
            ReasonCase.PLATFORM_REFUSED_START,
            RecordedReasons.refusalReason(SecurityException("permission")),
        )
        assertEquals(
            ReasonCase.PLATFORM_REFUSED_START,
            RecordedReasons.refusalReason(IllegalStateException("not allowed to start")),
        )
        assertEquals(
            ReasonCase.AUTOMATIC_START_FAILED,
            RecordedReasons.refusalReason(IllegalArgumentException("bad type")),
        )
        assertEquals(
            ReasonCase.AUTOMATIC_START_FAILED,
            RecordedReasons.refusalReason(RuntimeException("something else entirely")),
        )
    }

    /** Every case has a sentence, and every sentence says what the operator can do about it. */
    @Test
    fun everyCaseHasAMessageThatNamesTheCase() {
        for (case in ReasonCase.values()) {
            val message = RecordedReasons.message(case)
            assertTrue("${case.name} has no message", message.length > 40)
            assertTrue("${case.name} does not mention collection", message.contains("Collection"))
        }
    }
}

package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What the screen shows when the app is opened.
 *
 * The tier argument for this whole change names one harm: "a phone that silently stopped reporting
 * while the app claims otherwise". This is the file that stops it, and the case it turns on is the
 * force-stop below - the one where every persisted fact says collection is on and none of them is
 * evidence that it is running.
 */
class CollectionPresentationTest {

    private val on = IntentReading.Known(CollectionIntent.ON)
    private val off = IntentReading.Known(CollectionIntent.OFF)

    /**
     * The force-stop case, in full.
     *
     * Intent ON, so every stored fact says collection was wanted and was working. The app is
     * force-stopped; a stopped app is not delivered the boot broadcast, so the reboot starts
     * nothing and leaves no reason behind either. The screen must show NOT RUNNING, because the
     * live signal says so, and must not be talked out of it by the stored intent.
     */
    @Test
    fun aForceStoppedAppShowsNotRunningEvenThoughTheStoredIntentIsOn() {
        val shown = CollectionPresentationPolicy.forOpen(
            liveSessionRunning = false,
            reading = on,
            recorded = null,
        )
        assertFalse("the stored intent must never be rendered as a running state", shown.running)
        assertNull(shown.reason)
        assertTrue("and the silence is named rather than left blank", shown.unexplainedStop)
    }

    /**
     * The other half of the same rule: a live session shows running, and a stored intent of ON is
     * neither necessary nor sufficient for it. Only the liveness signal decides.
     */
    @Test
    fun theRunningStateFollowsTheLiveSignalAndNothingElse() {
        for (reading in listOf(on, off, IntentReading.Missing, IntentReading.Unreadable)) {
            assertTrue(
                "live session, intent $reading",
                CollectionPresentationPolicy.forOpen(true, reading, null).running,
            )
            assertFalse(
                "no live session, intent $reading",
                CollectionPresentationPolicy.forOpen(false, reading, null).running,
            )
        }
    }

    /**
     * Running AND a reason. The case that exists precisely while collection runs: the phone
     * restarted correctly, is collecting, and has not managed a position since. A build that only
     * shows a reason when collection is stopped hides exactly this one, which is why it is asserted
     * separately from the stopped-with-a-reason case below.
     */
    @Test
    fun aRunningDeviceThatCouldNotTakeARestartPositionShowsRunningAndTheReason() {
        val shown = CollectionPresentationPolicy.forOpen(
            liveSessionRunning = true,
            reading = on,
            recorded = ReasonCase.NO_RESTART_POSITION_YET,
        )
        assertTrue(shown.running)
        assertEquals(ReasonCase.NO_RESTART_POSITION_YET, shown.reason)
        assertFalse(shown.unexplainedStop)
    }

    /** Not running AND a reason: the refused or failed automatic start. */
    @Test
    fun aRefusedOrFailedStartShowsNotRunningAndTheReason() {
        for (case in listOf(
            ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED,
            ReasonCase.PLATFORM_REFUSED_START,
            ReasonCase.AUTOMATIC_START_FAILED,
            ReasonCase.SERVER_NOT_CONFIGURED,
            ReasonCase.COLLECTION_INTENT_MISSING,
            ReasonCase.COLLECTION_INTENT_UNREADABLE,
        )) {
            val shown = CollectionPresentationPolicy.forOpen(false, on, case)
            assertFalse(shown.running)
            assertEquals(case, shown.reason)
            assertFalse("a reason IS the explanation, so nothing is unexplained", shown.unexplainedStop)
        }
    }

    /**
     * A device whose collection intent is simply off holds no recorded reason and presents none. It
     * is not broken; it is doing what it was told, and a warning on that screen is noise that
     * teaches the operator to ignore the next one.
     */
    @Test
    fun intentOffPresentsNoReasonAndNothingUnexplained() {
        val shown = CollectionPresentationPolicy.forOpen(
            liveSessionRunning = false,
            reading = off,
            recorded = ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED,
        )
        assertFalse(shown.running)
        assertNull("intent OFF is quiet even if something is left in the record", shown.reason)
        assertFalse(shown.unexplainedStop)
    }

    /**
     * A fresh install that has never been asked to collect is quiet too. `unexplainedStop` is about
     * a phone that was asked and is not doing it, which is a different thing from one nobody has
     * set up yet.
     */
    @Test
    fun anIntentThatWasNeverWrittenIsNotAnUnexplainedStop() {
        for (reading in listOf(IntentReading.Missing, IntentReading.Unreadable)) {
            assertFalse(
                CollectionPresentationPolicy.forOpen(false, reading, null).unexplainedStop,
            )
        }
    }
}

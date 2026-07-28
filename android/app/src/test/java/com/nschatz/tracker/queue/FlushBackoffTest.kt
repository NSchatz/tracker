package com.nschatz.tracker.queue

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The jitter applied to WorkManager's initial retry delay.
 *
 * Small, but not decorative: WorkManager's exponential backoff is deterministic, so without this
 * every phone that failed against the same outage retries at the same instants and re-creates the
 * outage on recovery (roadmap §6, the post-outage reconnect storm). Because WorkManager doubles
 * whatever initial delay it is given, this one number spreads the entire schedule.
 */
class FlushBackoffTest {

    @Test
    fun theDelayCoversTheWholeJitterWindow() {
        assertEquals(FlushBackoff.BASE_SECONDS, FlushBackoff.initialDelaySeconds(0.0))
        assertEquals(
            FlushBackoff.BASE_SECONDS + FlushBackoff.JITTER_SECONDS,
            FlushBackoff.initialDelaySeconds(1.0),
        )
    }

    @Test
    fun theDelayGrowsWithTheDraw() {
        val low = FlushBackoff.initialDelaySeconds(0.1)
        val high = FlushBackoff.initialDelaySeconds(0.9)
        assertTrue("$low should be below $high", low < high)
    }

    /** Two devices drawing differently must not land on the same schedule — that is the whole job. */
    @Test
    fun differentDrawsProduceDifferentSchedules() {
        assertNotEquals(
            FlushBackoff.initialDelaySeconds(0.0),
            FlushBackoff.initialDelaySeconds(1.0),
        )
    }

    /**
     * Never below WorkManager's own floor.
     *
     * A value under `MIN_BACKOFF_MILLIS` is silently clamped by the platform, which would remove the
     * jitter while leaving behind code that looks like it is applying some — the worst kind of
     * failure, because it is invisible.
     */
    @Test
    fun theDelayIsNeverBelowWorkManagersMinimum() {
        for (draw in listOf(0.0, 0.25, 0.5, 0.75, 1.0)) {
            assertTrue(FlushBackoff.initialDelaySeconds(draw) >= FlushBackoff.MIN_SECONDS)
        }
    }

    /** The draw is a fraction, and a caller that passes something else is a bug, not a rounding. */
    @Test
    fun anOutOfRangeDrawIsRefused() {
        for (draw in listOf(-0.1, 1.1, Double.NaN)) {
            val error = runCatching { FlushBackoff.initialDelaySeconds(draw) }.exceptionOrNull()
            assertTrue("draw $draw should have been refused", error is IllegalArgumentException)
        }
    }
}

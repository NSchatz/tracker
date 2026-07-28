package com.nschatz.tracker.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The retry schedule.
 *
 * Testable at all only because the policy takes its random draw as an argument instead of calling a
 * generator — so these are exact equalities, not "roughly doubles".
 */
class BackoffPolicyTest {

    private val policy = BackoffPolicy(
        baseDelayMillis = 1_000,
        multiplier = 2.0,
        maxDelayMillis = 16_000,
        maxAttempts = 6,
    )

    @Test
    fun delayDoublesWithEachFailedAttempt() {
        assertEquals(1_000, policy.uncappedFreeDelayMillis(1))
        assertEquals(2_000, policy.uncappedFreeDelayMillis(2))
        assertEquals(4_000, policy.uncappedFreeDelayMillis(3))
        assertEquals(8_000, policy.uncappedFreeDelayMillis(4))
    }

    @Test
    fun delayStopsGrowingAtTheCeiling() {
        assertEquals(16_000, policy.uncappedFreeDelayMillis(5))
        assertEquals(16_000, policy.uncappedFreeDelayMillis(6))
        assertEquals(16_000, policy.uncappedFreeDelayMillis(50))
    }

    /**
     * A large attempt count must **saturate**, not overflow.
     *
     * `base * 2^attempt` in Long arithmetic goes negative somewhere past attempt 63, and a negative
     * delay is not a slow retry — it is an instant one, forever, at full speed. Computing in Double
     * and capping is what makes that unreachable.
     */
    @Test
    fun anAbsurdAttemptCountSaturatesRatherThanOverflowingNegative() {
        for (attempt in listOf(64, 100, 1_000, Int.MAX_VALUE)) {
            val delay = policy.uncappedFreeDelayMillis(attempt)
            assertTrue("attempt $attempt produced $delay", delay > 0)
            assertEquals(16_000, delay)
        }
    }

    /**
     * **Full jitter.** The delay is a uniform draw over `[0, delay]`, not a fixed value.
     *
     * This is the part that protects the server rather than the phone. The roadmap names the
     * post-outage reconnect storm as a load risk: every device retries on the same schedule after
     * the same outage, so an unjittered backoff synchronises them into a thundering herd that
     * re-creates the outage. These assertions pin the draw to the endpoints and the midpoint.
     */
    @Test
    fun jitterSpreadsTheDelayAcrossTheWholeWindow() {
        assertEquals(0, policy.delayMillis(attempt = 3, randomFraction = 0.0))
        assertEquals(2_000, policy.delayMillis(attempt = 3, randomFraction = 0.5))
        assertEquals(4_000, policy.delayMillis(attempt = 3, randomFraction = 1.0))
    }

    @Test
    fun jitterNeverExceedsTheUncappedWindow() {
        for (attempt in 1..8) {
            val window = policy.uncappedFreeDelayMillis(attempt)
            for (fraction in listOf(0.0, 0.01, 0.37, 0.99, 1.0)) {
                val delay = policy.delayMillis(attempt, fraction)
                assertTrue("attempt=$attempt f=$fraction delay=$delay", delay in 0..window)
            }
        }
    }

    @Test
    fun retriesAreBoundedSoAFixIsEventuallySurfacedAsLost() {
        assertTrue(policy.shouldRetry(1))
        assertTrue(policy.shouldRetry(5))
        assertFalse("the sixth attempt is the last", policy.shouldRetry(6))
        assertFalse(policy.shouldRetry(7))
    }

    @Test
    fun aSingleAttemptPolicyNeverRetries() {
        assertFalse(BackoffPolicy(maxAttempts = 1).shouldRetry(1))
    }

    @Test
    fun rejectsIncoherentConfiguration() {
        assertThrows(IllegalArgumentException::class.java) { BackoffPolicy(baseDelayMillis = 0) }
        assertThrows(IllegalArgumentException::class.java) { BackoffPolicy(multiplier = 0.5) }
        assertThrows(IllegalArgumentException::class.java) {
            BackoffPolicy(baseDelayMillis = 10_000, maxDelayMillis = 1_000)
        }
        assertThrows(IllegalArgumentException::class.java) { BackoffPolicy(maxAttempts = 0) }
    }

    @Test
    fun rejectsAnOutOfRangeRandomDraw() {
        assertThrows(IllegalArgumentException::class.java) { policy.delayMillis(1, -0.1) }
        assertThrows(IllegalArgumentException::class.java) { policy.delayMillis(1, 1.1) }
    }

    @Test
    fun attemptNumbersAreOneBased() {
        assertThrows(IllegalArgumentException::class.java) { policy.uncappedFreeDelayMillis(0) }
    }

    /** The shipped defaults must be sane, not merely the constructor's contract. */
    @Test
    fun defaultsAreBoundedAndFinite() {
        val defaults = BackoffPolicy()
        assertTrue(defaults.maxAttempts in 2..10)
        assertTrue(defaults.maxDelayMillis <= 10 * 60_000)
        assertEquals(defaults.maxDelayMillis, defaults.uncappedFreeDelayMillis(1_000))
    }
}

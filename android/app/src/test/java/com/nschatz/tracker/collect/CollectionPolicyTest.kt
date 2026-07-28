package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The location-request cadence.
 *
 * There is no way to prove from a headless build that the fused provider *honours* these numbers —
 * it batches, defers under Doze, and is throttled further by OEM battery managers, all of which are
 * device behaviour. What can be proven is that the configuration is **coherent**, which matters
 * because an incoherent one produces a cadence nobody can predict rather than an error.
 */
class CollectionPolicyTest {

    @Test
    fun defaultsAreCoherentAndConstructible() {
        val policy = CollectionPolicy()
        assertEquals(60_000, policy.intervalMillis)
        assertEquals(30_000, policy.minUpdateIntervalMillis)
        assertEquals(25f, policy.minUpdateDistanceMeters, 0f)
        assertEquals(120_000, policy.maxUpdateDelayMillis)
    }

    /**
     * A floor slower than the target is not a floor, it is a second and contradictory target, and
     * the provider's behaviour when the two disagree is unspecified. Refusing the configuration is
     * better than shipping a cadence that cannot be reasoned about.
     */
    @Test
    fun refusesAFastestIntervalSlowerThanTheTargetInterval() {
        assertThrows(IllegalArgumentException::class.java) {
            CollectionPolicy(intervalMillis = 30_000, minUpdateIntervalMillis = 60_000)
        }
    }

    @Test
    fun refusesNonPositiveIntervals() {
        assertThrows(IllegalArgumentException::class.java) { CollectionPolicy(intervalMillis = 0) }
        assertThrows(IllegalArgumentException::class.java) {
            CollectionPolicy(minUpdateIntervalMillis = 0)
        }
    }

    @Test
    fun refusesNegativeDistanceAndBatchWindow() {
        assertThrows(IllegalArgumentException::class.java) {
            CollectionPolicy(minUpdateDistanceMeters = -1f)
        }
        assertThrows(IllegalArgumentException::class.java) {
            CollectionPolicy(maxUpdateDelayMillis = -1)
        }
    }

    /**
     * A zero displacement filter would report a stationary phone forever at full cadence, which is
     * the battery-hostile failure risk path #4 is about. Equal intervals are allowed — that is a
     * legitimate "exactly this cadence" request.
     */
    @Test
    fun allowsEqualIntervalsAndAZeroDisplacementFilterExplicitly() {
        val policy = CollectionPolicy(
            intervalMillis = 15_000,
            minUpdateIntervalMillis = 15_000,
            minUpdateDistanceMeters = 0f,
        )
        assertEquals(15_000, policy.intervalMillis)
        assertEquals(0f, policy.minUpdateDistanceMeters, 0f)
    }

    /** The shipped defaults must not be so aggressive that they are a battery bug on their own. */
    @Test
    fun defaultCadenceIsConservativeEnoughToRunAllDay() {
        val policy = CollectionPolicy()
        assertTrue("a sub-30s default cadence would be battery-hostile", policy.intervalMillis >= 30_000)
        assertTrue("a stationary phone must be able to fall silent", policy.minUpdateDistanceMeters > 0f)
    }
}

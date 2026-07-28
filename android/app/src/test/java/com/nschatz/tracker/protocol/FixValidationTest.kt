package com.nschatz.tracker.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Client-side validation, mirroring the server's `store.ValidateLonLat` and
 * `validateOptionalMetrics`.
 *
 * The axis-order cases are the ones that matter. tracker's own `CLAUDE.md` records the hard-won
 * fact from S1: **PostGIS does not reject an out-of-range coordinate, it coerces it.** A swapped
 * lon/lat whose latitude lands past ±90 is folded back into range and stored as a real place, with
 * every database constraint satisfied — Seattle becomes a point in the South Atlantic. There is no
 * later check that catches it. These tests, and their server-side counterparts, are the whole
 * defence.
 */
class FixValidationTest {

    private val now = 1_752_566_400L // a fixed "now" so every window boundary is exact

    // --- coordinates ---------------------------------------------------------------------------

    @Test
    fun acceptsCoordinatesAcrossTheWholeLegalRange() {
        val legal = listOf(
            0.0 to 0.0,            // Null Island — a real place, never a sentinel
            12.4964 to 41.9028,    // Rome
            -122.3321 to 47.6062,  // Seattle
            180.0 to 90.0,         // the corners are inclusive
            -180.0 to -90.0,
            179.999999 to 89.999999,
        )
        for ((lon, lat) in legal) {
            assertNull("lon=$lon lat=$lat should be accepted", FixValidation.validateCoordinate(lon, lat))
        }
    }

    /**
     * The swap, caught. Seattle is (lon −122.3321, lat 47.6062). Passed the wrong way round the
     * latitude becomes −122.3321, which is not a latitude at all.
     */
    @Test
    fun rejectsASwappedSeattleAndSaysSo() {
        val rejection = FixValidation.validateCoordinate(lon = 47.6062, lat = -122.3321)
        assertNotNull("a swapped lon/lat must be refused, never coerced", rejection)
        assertEquals(FixRejectionKind.COORDINATE_OUT_OF_RANGE, rejection!!.kind)
        assertTrue(
            "the message should point at the swap, since that is nearly always the cause: ${rejection.message}",
            "swapped" in rejection.message,
        )
    }

    @Test
    fun rejectsOutOfRangeLatitudeAndLongitudeJustPastTheBoundary() {
        assertNotNull(FixValidation.validateCoordinate(lon = 0.0, lat = 90.000001))
        assertNotNull(FixValidation.validateCoordinate(lon = 0.0, lat = -90.000001))
        assertNotNull(FixValidation.validateCoordinate(lon = 180.000001, lat = 0.0))
        assertNotNull(FixValidation.validateCoordinate(lon = -180.000001, lat = 0.0))
    }

    @Test
    fun rejectsNonFiniteCoordinates() {
        for (bad in listOf(Double.NaN, Double.POSITIVE_INFINITY, Double.NEGATIVE_INFINITY)) {
            assertNotNull("lat=$bad must be refused", FixValidation.validateCoordinate(0.0, bad))
            assertNotNull("lon=$bad must be refused", FixValidation.validateCoordinate(bad, 0.0))
        }
    }

    /**
     * A swap that does NOT leave the legal range is undetectable here, and pretending otherwise
     * would be worse than admitting it. Rome is (lon 12.4964, lat 41.9028); swapped it is
     * (lon 41.9028, lat 12.4964) — a perfectly legal point in the Indian Ocean. No range check can
     * catch that, on the client or in Go, which is why the server also carries a permanent
     * axis-order regression test against the actual SQL rather than relying on validation alone.
     */
    @Test
    fun aSwapWithinRangeIsNotDetectableAndThisTestRecordsThat() {
        assertNull(FixValidation.validateCoordinate(lon = 41.9028, lat = 12.4964))
    }

    // --- timestamp window ----------------------------------------------------------------------

    @Test
    fun acceptsTimestampsInsideTheServerWindow() {
        assertNull(FixValidation.validateTimestamp(now, now))
        assertNull(FixValidation.validateTimestamp(now + FixValidation.MAX_FUTURE_SKEW_SECONDS, now))
        assertNull(FixValidation.validateTimestamp(now - FixValidation.MAX_BACKLOG_SECONDS, now))
    }

    @Test
    fun rejectsTimestampsOneSecondOutsideEitherBound() {
        val future = FixValidation.validateTimestamp(now + FixValidation.MAX_FUTURE_SKEW_SECONDS + 1, now)
        assertEquals(FixRejectionKind.TIMESTAMP_OUT_OF_WINDOW, future?.kind)

        val past = FixValidation.validateTimestamp(now - FixValidation.MAX_BACKLOG_SECONDS - 1, now)
        assertEquals(FixRejectionKind.TIMESTAMP_OUT_OF_WINDOW, past?.kind)
    }

    /** The window must be the server's, or the client refuses fixes the server would take. */
    @Test
    fun windowConstantsMatchTheServerContract() {
        assertEquals(24L * 60 * 60, FixValidation.MAX_FUTURE_SKEW_SECONDS)
        assertEquals(90L * 24 * 60 * 60, FixValidation.MAX_BACKLOG_SECONDS)
    }

    // --- optional metrics ----------------------------------------------------------------------

    @Test
    fun absentMetricsAreAlwaysAcceptable() {
        assertNull(FixValidation.validateMetrics(Fix(lat = 0.0, lon = 0.0, tsEpochSeconds = now)))
    }

    @Test
    fun rejectsOutOfRangeMetrics() {
        val base = Fix(lat = 0.0, lon = 0.0, tsEpochSeconds = now)
        assertEquals(
            FixRejectionKind.METRIC_OUT_OF_RANGE,
            FixValidation.validateMetrics(base.copy(accuracyM = -1.0))?.kind,
        )
        assertEquals(
            FixRejectionKind.METRIC_OUT_OF_RANGE,
            FixValidation.validateMetrics(base.copy(speedMps = -0.1))?.kind,
        )
        assertEquals(
            FixRejectionKind.METRIC_OUT_OF_RANGE,
            FixValidation.validateMetrics(base.copy(batteryPct = 101))?.kind,
        )
        assertEquals(
            FixRejectionKind.METRIC_OUT_OF_RANGE,
            FixValidation.validateMetrics(base.copy(batteryPct = -1))?.kind,
        )
        assertEquals(
            FixRejectionKind.METRIC_OUT_OF_RANGE,
            FixValidation.validateMetrics(base.copy(accuracyM = Double.NaN))?.kind,
        )
    }

    @Test
    fun acceptsMetricsExactlyOnTheirBounds() {
        val base = Fix(lat = 0.0, lon = 0.0, tsEpochSeconds = now)
        assertNull(FixValidation.validateMetrics(base.copy(accuracyM = 0.0, speedMps = 0.0, batteryPct = 0)))
        assertNull(FixValidation.validateMetrics(base.copy(batteryPct = 100)))
    }

    /**
     * When both are wrong, the coordinate is the error reported.
     *
     * Deliberately pinned, and deliberately NOT the server's order — `store.IngestFix` checks the
     * timestamp first and the coordinate second. The divergence is harmless (either way the fix is
     * refused and stored nowhere) but it is real, so it is asserted here rather than left to be
     * discovered as a surprise by someone comparing the two error messages for the same bad fix.
     * The coordinate goes first on the client because a swapped axis is the defect this validation
     * exists for, and naming it is more useful to a developer than naming a clock skew.
     */
    @Test
    fun validateReportsTheCoordinateProblemFirst() {
        val badBoth = Fix(lat = 999.0, lon = 0.0, tsEpochSeconds = now + 999_999_999)
        assertEquals(
            FixRejectionKind.COORDINATE_OUT_OF_RANGE,
            FixValidation.validate(badBoth, now)?.kind,
        )
    }
}

package com.nschatz.tracker.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Building a wire [Fix] from a raw location reading.
 *
 * The unit conversion is the whole point. `Location.getTime()` is **milliseconds**; `ts` on the wire
 * is **seconds** (`SPEC.md`). Getting that wrong by a factor of 1000 does not fail loudly — it
 * produces a timestamp in the year 57,000, which the server's ingest window then rejects as
 * "more than 24h in the future", and the client looks like it has a clock problem rather than a
 * units problem.
 */
class FixFactoryTest {

    private val now = 1_752_566_400L

    @Test
    fun convertsMillisecondsToSeconds() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 41.9028, lon = 12.4964, elapsedMillis = 1_752_566_400_000),
            nowEpochSeconds = now,
        )
        val fix = (result as FixResult.Valid).fix
        assertEquals(1_752_566_400L, fix.tsEpochSeconds)
    }

    /** Sub-second remainders truncate toward the past, never round up into the future. */
    @Test
    fun truncatesSubSecondRemainders() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 0.0, lon = 0.0, elapsedMillis = 1_752_566_400_999),
            nowEpochSeconds = now,
        )
        assertEquals(1_752_566_400L, (result as FixResult.Valid).fix.tsEpochSeconds)
    }

    /**
     * floorDiv, not `/`. For a pre-epoch millisecond value the two disagree: `-1500 / 1000` is −1
     * (toward zero) but the second containing that instant is −2. Only one of those is the right
     * answer, and a time conversion that is quietly off by one is exactly the kind of defect that is
     * invisible until it is a wrong trail.
     */
    @Test
    fun handlesPreEpochTimestampsByFlooring() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 0.0, lon = 0.0, elapsedMillis = -1_500),
            nowEpochSeconds = 0,
        )
        assertEquals(-2L, (result as FixResult.Valid).fix.tsEpochSeconds)
    }

    @Test
    fun carriesEveryOptionalReadingThrough() {
        val result = FixFactory.build(
            reading = LocationReading(
                lat = 41.9028,
                lon = 12.4964,
                elapsedMillis = now * 1000,
                accuracyM = 7.5,
                speedMps = 1.4,
            ),
            nowEpochSeconds = now,
            batteryPct = 88,
            trigger = FixTrigger.PERIODIC,
            msgId = "m-1",
        )
        val fix = (result as FixResult.Valid).fix
        assertEquals(7.5, fix.accuracyM!!, 0.0)
        assertEquals(1.4, fix.speedMps!!, 0.0)
        assertEquals(88, fix.batteryPct)
        assertEquals("periodic", fix.trigger)
        assertEquals("m-1", fix.msgId)
    }

    @Test
    fun leavesAbsentReadingsAbsent() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 1.0, lon = 2.0, elapsedMillis = now * 1000),
            nowEpochSeconds = now,
        )
        val fix = (result as FixResult.Valid).fix
        assertEquals(null, fix.accuracyM)
        assertEquals(null, fix.speedMps)
        assertEquals(null, fix.batteryPct)
        assertEquals(null, fix.trigger)
    }

    /**
     * A reading that cannot make a valid fix is **refused, not clamped**.
     *
     * Clamping a latitude of 122 to 90 would put the family at the North Pole and report it as fact.
     * Risk path #2 in the roadmap is "wrong location math trusted as truth", and for a safety
     * product a confident wrong answer is worse than no answer.
     */
    @Test
    fun refusesAnImpossibleCoordinateInsteadOfClampingIt() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 122.3321, lon = 47.6062, elapsedMillis = now * 1000),
            nowEpochSeconds = now,
        )
        assertTrue(result is FixResult.Refused)
        assertEquals(
            FixRejectionKind.COORDINATE_OUT_OF_RANGE,
            (result as FixResult.Refused).rejection.kind,
        )
    }

    @Test
    fun refusesAFixWhoseClockIsOutsideTheServerWindow() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 1.0, lon = 2.0, elapsedMillis = (now + 200_000) * 1000),
            nowEpochSeconds = now,
        )
        assertEquals(
            FixRejectionKind.TIMESTAMP_OUT_OF_WINDOW,
            (result as FixResult.Refused).rejection.kind,
        )
    }

    /** A refused fix must never be serialisable to a body that could accidentally be sent. */
    @Test
    fun aValidResultAlwaysEncodesToAWellFormedBody() {
        val result = FixFactory.build(
            reading = LocationReading(lat = 41.9028, lon = 12.4964, elapsedMillis = now * 1000),
            nowEpochSeconds = now,
        )
        val body = (result as FixResult.Valid).fix.toJsonBody()
        assertTrue(body.startsWith("{") && body.endsWith("}"))
        assertTrue("\"lat\":41.9028" in body)
        assertTrue("\"lon\":12.4964" in body)
    }
}

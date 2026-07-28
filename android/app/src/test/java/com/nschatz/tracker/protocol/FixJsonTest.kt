package com.nschatz.tracker.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The wire format of `POST /v1/fixes`.
 *
 * These assertions are deliberately **exact strings**, not "contains lat". The server decodes this
 * body with `DisallowUnknownFields` and rejects trailing data (`SPEC.md`, "Strictness & errors"), so
 * a misspelled key or a stray field is a `400` on a real phone and nothing here would catch it if
 * the test only checked that the interesting bits were present somewhere.
 */
class FixJsonTest {

    @Test
    fun requiredFieldsOnlyProducesTheMinimalBody() {
        val fix = Fix(lat = 41.9028, lon = 12.4964, tsEpochSeconds = 1752566400)
        assertEquals("""{"lat":41.9028,"lon":12.4964,"ts":1752566400}""", fix.toJsonBody())
    }

    @Test
    fun everyOptionalFieldIsEmittedUnderItsSpecName() {
        val fix = Fix(
            lat = 41.9028,
            lon = 12.4964,
            tsEpochSeconds = 1752566400,
            accuracyM = 5.0,
            batteryPct = 88,
            speedMps = 1.4,
            trigger = "periodic",
            msgId = "abc-123",
        )
        assertEquals(
            """{"lat":41.9028,"lon":12.4964,"ts":1752566400,"accuracy":5.0,"battery":88,""" +
                """"speed":1.4,"trigger":"periodic","msg_id":"abc-123"}""",
            fix.toJsonBody(),
        )
    }

    /**
     * The single most important property of this encoder.
     *
     * An absent optional must be **absent from the body**, not present as zero. The server stores a
     * missing field as SQL `NULL` and a zero as a real measurement, so emitting `"battery":0` for a
     * phone that never reported its battery would fabricate a reading of a flat battery — data the
     * device never produced, indistinguishable afterwards from a real one.
     */
    @Test
    fun absentOptionalsAreOmittedRatherThanZeroed() {
        val fix = Fix(lat = 1.0, lon = 2.0, tsEpochSeconds = 100, accuracyM = null, batteryPct = null)
        val body = fix.toJsonBody()
        assertTrue("accuracy must not appear at all: $body", "accuracy" !in body)
        assertTrue("battery must not appear at all: $body", "battery" !in body)
        assertTrue("speed must not appear at all: $body", "speed" !in body)
        assertTrue("trigger must not appear at all: $body", "trigger" !in body)
        assertTrue("msg_id must not appear at all: $body", "msg_id" !in body)
    }

    /** A zero that IS real must survive: 0 % battery is a flat phone, not an absent reading. */
    @Test
    fun presentZerosAreEmitted() {
        val fix = Fix(
            lat = 0.0,
            lon = 0.0,
            tsEpochSeconds = 0,
            accuracyM = 0.0,
            batteryPct = 0,
            speedMps = 0.0,
        )
        assertEquals(
            """{"lat":0.0,"lon":0.0,"ts":0,"accuracy":0.0,"battery":0,"speed":0.0}""",
            fix.toJsonBody(),
        )
    }

    /**
     * Coordinates keep full precision.
     *
     * The seventh decimal place of a degree is roughly a centimetre; rounding to a "nice" number of
     * places would silently degrade every position in the product.
     */
    @Test
    fun coordinatePrecisionIsNotTruncated() {
        val fix = Fix(lat = 47.6062095, lon = -122.3320708, tsEpochSeconds = 1)
        assertEquals("""{"lat":47.6062095,"lon":-122.3320708,"ts":1}""", fix.toJsonBody())
    }

    /** Negative longitudes and a negative (pre-epoch) ts are ordinary JSON numbers. */
    @Test
    fun negativeValuesEncodeCorrectly() {
        val fix = Fix(lat = -33.8688, lon = -151.2093, tsEpochSeconds = -5)
        assertEquals("""{"lat":-33.8688,"lon":-151.2093,"ts":-5}""", fix.toJsonBody())
    }

    /**
     * `trigger` is free-form and `msg_id` is client-generated, so both can contain characters that
     * would break the body. An unescaped quote turns one field into three and the whole POST into a
     * `400 malformed`.
     */
    @Test
    fun stringsAreEscaped() {
        val fix = Fix(
            lat = 1.0,
            lon = 2.0,
            tsEpochSeconds = 3,
            trigger = "he said \"go\"\\now\n\ttab",
            msgId = "ctrl\u0001char",
        )
        assertEquals(
            """{"lat":1.0,"lon":2.0,"ts":3,"trigger":"he said \"go\"\\now\n\ttab",""" +
                """"msg_id":"ctrl\u0001char"}""",
            fix.toJsonBody(),
        )
    }

    @Test
    fun unicodeIsPassedThroughAsUtf8Text() {
        assertEquals(""""Ålesund — 東京"""", encodeJsonString("Ålesund — 東京"))
    }

    /**
     * NaN and the infinities have no JSON representation — `Double.toString` would emit the bare
     * token `NaN`, which is not valid JSON and would reach the server as an unparseable body. They
     * are caught by validation long before here; this asserts the encoder still refuses loudly
     * rather than putting garbage on the wire if a future caller ever skips that step.
     */
    @Test
    fun nonFiniteNumbersAreRefusedRatherThanEncoded() {
        for (bad in listOf(Double.NaN, Double.POSITIVE_INFINITY, Double.NEGATIVE_INFINITY)) {
            assertThrows(IllegalArgumentException::class.java) {
                Fix(lat = bad, lon = 0.0, tsEpochSeconds = 1).toJsonBody()
            }
        }
    }

    /**
     * The schema is exactly eight fields. A ninth would be an unknown field to the server's strict
     * decoder and a `400` — so this pins the key set as a whole, catching an addition that the
     * per-field tests above would each individually still pass.
     */
    @Test
    fun bodyContainsOnlyTheEightSpecFields() {
        val body = Fix(
            lat = 1.0, lon = 2.0, tsEpochSeconds = 3, accuracyM = 4.0,
            batteryPct = 5, speedMps = 6.0, trigger = "t", msgId = "m",
        ).toJsonBody()
        val keys = Regex("\"([a-z_]+)\":").findAll(body).map { it.groupValues[1] }.toSet()
        assertEquals(
            setOf("lat", "lon", "ts", "accuracy", "battery", "speed", "trigger", "msg_id"),
            keys,
        )
    }
}

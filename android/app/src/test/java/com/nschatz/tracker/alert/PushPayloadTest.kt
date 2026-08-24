package com.nschatz.tracker.alert

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * What a received push is allowed to become.
 *
 * The load-bearing case is the negative one: an incomplete payload renders NOTHING and is counted,
 * rather than being rendered with a guessed part. "Someone arrived somewhere" about a child is
 * worse than silence, because the crossing list can recover from silence and cannot recover from a
 * person having been told the wrong thing.
 */
class PushPayloadTest {

    private fun completeData(): MutableMap<String, String?> = mutableMapOf(
        "type" to "geofence",
        "device_id" to "dev-1",
        "place_id" to "place-1",
        "place_name" to "School",
        "transition" to "enter",
        "ts" to "2026-07-16T09:30:00Z",
    )

    @Test
    fun aCompletePushBecomesACrossingNamingTheDeviceThePlaceAndTheDirection() {
        val crossing = PushPayload.parse("Alice's phone", completeData())
        assertNotNull(crossing)
        requireNotNull(crossing)

        assertEquals("Alice's phone", crossing.deviceName)
        assertEquals("School", crossing.placeName)
        assertEquals(Transition.ENTER, crossing.transition)
        assertEquals("dev-1", crossing.deviceId)
        assertEquals("place-1", crossing.placeId)

        // The three things a person is told, all present in the rendered notification.
        val body = AlertText.notificationBody(crossing)
        assertEquals("Alice's phone arrived at School", body)
        assertEquals("Alice's phone", AlertText.notificationTitle(crossing))
    }

    @Test
    fun anExitReadsAsADeparture() {
        val data = completeData().apply { this["transition"] = "exit" }
        val crossing = requireNotNull(PushPayload.parse("Alice's phone", data))
        assertEquals(Transition.EXIT, crossing.transition)
        assertEquals("Alice's phone left School", AlertText.notificationBody(crossing))
    }

    /**
     * Every way a push can be incomplete, each one refused.
     *
     * The device name is included because it is the part a push carries as its TITLE rather than in
     * the data map, so it is the one an implementation is most likely to forget to require and
     * quietly substitute for.
     */
    @Test
    fun anIncompletePushIsRefusedAndNothingIsInvented() {
        val cases = listOf<Pair<String, Pair<String?, Map<String, String?>>>>(
            "no device name (no title)" to (null to completeData()),
            "blank device name" to ("   " to completeData()),
            "not a geofence message" to ("Alice's phone" to completeData().apply { this["type"] = "chat" }),
            "no type at all" to ("Alice's phone" to completeData().apply { remove("type") }),
            "no place name" to ("Alice's phone" to completeData().apply { remove("place_name") }),
            "blank place name" to ("Alice's phone" to completeData().apply { this["place_name"] = " " }),
            "no direction" to ("Alice's phone" to completeData().apply { remove("transition") }),
            "an unknown direction" to ("Alice's phone" to completeData().apply { this["transition"] = "loitered" }),
            "no device id" to ("Alice's phone" to completeData().apply { remove("device_id") }),
            "no place id" to ("Alice's phone" to completeData().apply { remove("place_id") }),
            "no ts" to ("Alice's phone" to completeData().apply { remove("ts") }),
            "a ts in the wrong format" to ("Alice's phone" to completeData().apply { this["ts"] = "1768555800" }),
        )
        for ((name, input) in cases) {
            assertNull("[$name] must not become a crossing", PushPayload.parse(input.first, input.second))
        }
    }

    /**
     * The ts normalisation, which is what makes one crossing one row across the two sources.
     *
     * The push states the instant as a UTC calendar timestamp and the crossings route states the same
     * instant as epoch seconds. If these two ever disagree, one crossing becomes two rows in the list
     * and the app looks like it is inventing arrivals.
     */
    @Test
    fun theUtcCalendarTimestampAgreesWithEpochSecondsToTheSecond() {
        val cases = mapOf(
            "1970-01-01T00:00:00Z" to 0L,
            "2026-07-16T09:30:00Z" to 1_784_194_200L,
            "2024-02-29T12:00:00Z" to 1_709_208_000L, // a leap day
            "2000-03-01T00:00:00Z" to 951_868_800L, // the year-2000 leap rule
            "2026-12-31T23:59:59Z" to 1_798_761_599L,
        )
        for ((calendar, epoch) in cases) {
            assertEquals("[$calendar]", epoch, PushPayload.parseUtcCalendarSeconds(calendar))
        }
    }

    @Test
    fun aTimestampThatIsNotTheServersFormatIsRefusedRatherThanGuessedAt() {
        val bad = listOf(
            "", "not a time", "2026-07-16 09:30:00Z", "2026-07-16T09:30:00", "2026-07-16T09:30:00+01:00",
            "2026-13-16T09:30:00Z", "2026-02-30T09:30:00Z", "2026-07-16T24:30:00Z", "20260716T093000Z",
        )
        for (value in bad) {
            assertNull("[$value] must not parse", PushPayload.parseUtcCalendarSeconds(value))
        }
    }
}

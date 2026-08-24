package com.nschatz.tracker.alert

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test

/**
 * Two properties of the alert surface that would be invisible if nobody asserted them: the discard
 * count behind A8, and the privacy boundary the whole surface rests on.
 *
 * These share a file because they share a subject: what the app does with a push it receives, and
 * what it is allowed to be holding afterwards.
 */
class AlertPrivacyAndDiscardTest {

    @Before
    fun clearSurface() = AlertSurface.reset()

    @After
    fun clearSurfaceAgain() = AlertSurface.reset()

    private fun completeData() = mutableMapOf<String, String?>(
        "type" to "geofence",
        "device_id" to "dev-1",
        "place_id" to "place-1",
        "place_name" to "School",
        "transition" to "enter",
        "ts" to "2026-07-16T09:30:00Z",
    )

    /**
     * A8: an incomplete push renders nothing, invents nothing, and is COUNTED - observable here, with
     * no device anywhere in the loop.
     */
    @Test
    fun anIncompletePushRendersNothingAndIsCounted() {
        assertEquals(0, AlertSurface.discardedPushes)

        assertNull(AlertIntake.receive(null, completeData()))
        assertEquals("a push with no device name must be discarded and counted", 1, AlertSurface.discardedPushes)

        assertNull(AlertIntake.receive("Alice's phone", completeData().apply { remove("place_name") }))
        assertNull(AlertIntake.receive("Alice's phone", completeData().apply { remove("transition") }))
        assertNull(AlertIntake.receive("Alice's phone", completeData().apply { this["type"] = "something-else" }))
        assertEquals(4, AlertSurface.discardedPushes)

        // And nothing was added to the list. A discarded push is not half a crossing.
        assertEquals(0, AlertSurface.pushed.size)
    }

    @Test
    fun aCompletePushIsRenderedAndNotCountedAsADiscard() {
        val crossing = AlertIntake.receive("Alice's phone", completeData())
        assertNotNull(crossing)
        assertEquals(0, AlertSurface.discardedPushes)
        assertEquals(1, AlertSurface.pushed.size)
    }

    @Test
    fun theSamePushArrivingTwiceIsOneRow() {
        AlertIntake.receive("Alice's phone", completeData())
        AlertIntake.receive("Alice's phone", completeData())
        assertEquals(1, AlertSurface.pushed.size)
        assertEquals(1, AlertSurface.listView.rows.size)
    }

    /**
     * A17: nothing the alert surface renders, holds or would log carries a coordinate, an accuracy or
     * a raw fix datum.
     *
     * The enforcement is structural - [Crossing] has no field for one - and this test is what stops a
     * later change from adding one quietly. It drives a push and a crossings read that both carry
     * coordinate-shaped extras, and then asserts that nothing the app produces from them contains a
     * coordinate.
     */
    @Test
    fun nothingTheAlertSurfaceProducesCarriesACoordinate() {
        // A push whose sender has (wrongly) added location data. The app must not carry it forward.
        val overreaching = completeData().apply {
            this["lat"] = "41.9028"
            this["lon"] = "12.4964"
            this["accuracy"] = "5.0"
        }
        val crossing = requireNotNull(AlertIntake.receive("Alice's phone", overreaching))

        // A crossings row with the same extras, which a strict reader simply does not carry either.
        val rows = CrossingsResponse.parse(
            """[{"device_id":"dev-1","device_name":"Alice's phone","place_id":"place-1",
                 "place_name":"School","transition":"enter","ts":1784194200,
                 "lat":41.9028,"lon":12.4964,"accuracy":5.0}]""",
        )
        AlertSurface.recordRead(AlertClient.CrossingsResult.Ok(rows))

        val produced = buildList {
            add(crossing.toString())
            add(AlertText.notificationTitle(crossing))
            add(AlertText.notificationBody(crossing))
            add(AlertText.listRow(crossing))
            addAll(rows.map { it.toString() })
            addAll(rows.map { AlertText.listRow(it) })
            add(AlertSurface.listView.toString())
            add(AlertSurface.pushed.toString())
        }

        // The numbers themselves, and the words that would name them. "12." and "41." are the two
        // coordinate prefixes the server-side guard uses for the same purpose.
        val forbidden = listOf("41.9028", "12.4964", "lat", "lon", "coordinate", "accuracy", "12.", "41.")
        for (text in produced) {
            val haystack = text.lowercase()
            for (needle in forbidden) {
                assertFalse(
                    "the alert surface produced something carrying $needle: $text",
                    haystack.contains(needle),
                )
            }
        }
    }

    /**
     * The other half of the privacy boundary: the app reads NO route that returns a coordinate. The
     * viewer credential it now holds could reach four of them.
     */
    @Test
    fun theAppReachesOnlyTheTwoRoutesThatCarryNoCoordinate() {
        val reached = listOf(AlertClient.CROSSINGS_PATH, AlertClient.REGISTER_PATH)
        assertEquals(listOf("/v1/geofence-events", "/v1/push-subscriptions"), reached)

        // Every viewer route that DOES return a coordinate. None of them may be what this client
        // reaches, and pinning the list here is what makes adding one a deliberate act.
        val coordinateBearing = listOf("/v1/positions", "/v1/near", "/v1/stream", "/v1/devices/")
        for (route in coordinateBearing) {
            for (used in reached) {
                assertFalse("the alert surface must not read $route", used.startsWith(route))
            }
        }
    }
}

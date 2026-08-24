package com.nschatz.tracker.alert

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The merge, and the five outcomes of the in-app list.
 *
 * The merge test is the one that matters most. A crossing reaches this app twice by design - once as
 * a push and once from the crossings route, which is the fail-safe FOR the push - and showing it
 * twice would make the fail-safe look like a bug. Collapsing two genuinely different crossings into
 * one would be much worse: it would hide an arrival out of the list that exists to catch what got
 * lost.
 */
class CrossingListViewTest {

    private fun crossing(
        device: String = "dev-1",
        deviceName: String? = "Alice's phone",
        place: String = "place-1",
        placeName: String = "School",
        transition: Transition = Transition.ENTER,
        at: Long = 1_784_194_200L,
    ) = Crossing(device, deviceName, place, placeName, transition, at)

    @Test
    fun oneCrossingFromBothSourcesIsShownOnce() {
        // The same crossing, stated by the two sources in their two ts formats. The push's calendar
        // timestamp is normalised at the edge; here they are already the instant they agree on.
        val fromPush = requireNotNull(
            PushPayload.parse(
                "Alice's phone",
                mapOf(
                    "type" to "geofence", "device_id" to "dev-1", "place_id" to "place-1",
                    "place_name" to "School", "transition" to "enter", "ts" to "2026-07-16T09:30:00Z",
                ),
            ),
        )
        val fromRoute = CrossingsResponse.parse(
            """[{"device_id":"dev-1","device_name":"Alice's phone","place_id":"place-1",
                 "place_name":"School","transition":"enter","ts":1784194200}]""",
        )

        assertEquals(
            "the two sources must agree on the instant, or one crossing becomes two rows",
            fromPush.instantSeconds,
            fromRoute.single().instantSeconds,
        )
        val merged = CrossingMerge.merge(fromRoute = fromRoute, fromPush = listOf(fromPush))
        assertEquals(1, merged.size)
    }

    /**
     * The forbidden merge key, stated as a test: two crossings of ONE device at ONE Place at
     * different instants stay two rows. A key of (device, Place) alone would collapse leaving for
     * school and coming home into one.
     */
    @Test
    fun twoCrossingsOfOneDeviceAtOnePlaceAtDifferentInstantsStayTwoRows() {
        val morning = crossing(transition = Transition.EXIT, at = 1_784_194_200L)
        val afternoon = crossing(transition = Transition.ENTER, at = 1_784_223_000L)
        val merged = CrossingMerge.merge(fromRoute = listOf(morning, afternoon), fromPush = emptyList())
        assertEquals(2, merged.size)
        // Newest first.
        assertEquals(afternoon.instantSeconds, merged[0].instantSeconds)
        assertEquals(morning.instantSeconds, merged[1].instantSeconds)
    }

    @Test
    fun twoDevicesAtOnePlaceAtOneInstantStayTwoRows() {
        val a = crossing(device = "dev-1", deviceName = "Alice's phone")
        val b = crossing(device = "dev-2", deviceName = "Bob's phone")
        assertEquals(2, CrossingMerge.merge(listOf(a, b), emptyList()).size)
    }

    @Test
    fun theRoutesCopyWinsWhereBothSourcesHaveOne() {
        val pushed = crossing(deviceName = "Alice's phone")
        val fromRoute = crossing(deviceName = "Alice's new phone")
        val merged = CrossingMerge.merge(fromRoute = listOf(fromRoute), fromPush = listOf(pushed))
        assertEquals("Alice's new phone", merged.single().deviceName)
    }

    /**
     * An empty family, a rejected credential and an unreachable server are three different answers.
     * Presenting any failure as an empty list is the defect this list exists to prevent.
     */
    @Test
    fun anEmptyFamilyIsDistinguishableFromEveryFailure() {
        val empty = CrossingListView.of(AlertClient.CrossingsResult.Ok(emptyList()), emptyList())
        val rejected = CrossingListView.of(AlertClient.CrossingsResult.Unauthorized, emptyList())
        val unreachable = CrossingListView.of(AlertClient.CrossingsResult.Unreachable("no route"), emptyList())
        val serverError = CrossingListView.of(AlertClient.CrossingsResult.ServerError("500"), emptyList())
        val checking = CrossingListView.of(null, emptyList())

        assertEquals(CrossingListState.EMPTY, empty.state)
        assertEquals(CrossingListState.CREDENTIAL_REJECTED, rejected.state)
        assertEquals(CrossingListState.SERVER_UNREACHABLE, unreachable.state)
        assertEquals(CrossingListState.SERVER_ERROR, serverError.state)
        assertEquals(CrossingListState.CHECKING, checking.state)

        val states = listOf(empty, rejected, unreachable, serverError, checking).map { it.state }
        assertEquals("all five must be distinguishable: $states", states.size, states.toSet().size)
    }

    /**
     * A failed read never hides a crossing this phone already received. The failure is said, ABOVE
     * the list, not instead of it.
     */
    @Test
    fun aFailedReadStillShowsTheCrossingsThatArrivedByPush()
    {
        val pushed = listOf(crossing())
        for (failure in listOf(
            AlertClient.CrossingsResult.Unauthorized,
            AlertClient.CrossingsResult.Unreachable("no route"),
            AlertClient.CrossingsResult.ServerError("500"),
        )) {
            val view = CrossingListView.of(failure, pushed)
            assertEquals("[$failure] the pushed crossing must still be listed", 1, view.rows.size)
            assertTrue("[$failure] and the failure must still be said", view.state != CrossingListState.SHOWING)
        }
    }

    @Test
    fun aSuccessfulReadWithRowsIsSimplyShowing() {
        val view = CrossingListView.of(AlertClient.CrossingsResult.Ok(listOf(crossing())), emptyList())
        assertEquals(CrossingListState.SHOWING, view.state)
        assertEquals(1, view.rows.size)
    }
}

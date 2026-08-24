package com.nschatz.tracker.alert

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId

/**
 * Reading `GET /v1/geofence-events` into crossings.
 *
 * Two things are load-bearing here and both are about NOT lying: an absent device name stays absent
 * rather than becoming an invented one, and a body this client cannot read raises rather than
 * returning an empty list that would be shown as "your family has no crossings".
 */
class CrossingsResponseTest {

    /** A pinned clock, so a rendered row is the same string on every machine this gate runs on. */
    private val UTC: ZoneId = ZoneId.of("UTC")

    private val row = """
        {"device_id":"dev-1","device_name":"Alice's phone","place_id":"place-1",
         "place_name":"School","transition":"enter","ts":1784194200}
    """.trimIndent()

    @Test
    fun aRowCarriesTheFamilysOwnNameForTheDevice() {
        val crossings = CrossingsResponse.parse("[$row]")
        assertEquals(1, crossings.size)
        val c = crossings.single()
        assertEquals("Alice's phone", c.deviceName)
        assertEquals("School", c.placeName)
        assertEquals(Transition.ENTER, c.transition)
        assertEquals(1_784_194_200L, c.instantSeconds)
        assertEquals("Alice's phone arrived at School (2026-07-16 09:30)", AlertText.listRow(c, UTC))
    }

    /**
     * A server older than this phase's additive field serves rows with no `device_name`. The crossing
     * still shows - the Place, the direction and the time are all there - and the name is a visible
     * placeholder, never a fabricated one.
     */
    @Test
    fun aRowWithNoDeviceNameShowsAPlaceholderAndKeepsEverythingElse() {
        val older = """
            {"device_id":"dev-1","place_id":"place-1","place_name":"School",
             "transition":"exit","ts":1784194200}
        """.trimIndent()
        val c = CrossingsResponse.parse("[$older]").single()

        assertNull("an absent device name must stay absent", c.deviceName)
        assertEquals("School", c.placeName)
        assertEquals(Transition.EXIT, c.transition)
        assertEquals(1_784_194_200L, c.instantSeconds)

        val rendered = AlertText.listRow(c, UTC)
        assertTrue("the placeholder must be visible: $rendered", rendered.contains(AlertText.UNNAMED_DEVICE))
        assertTrue("the Place must still be shown: $rendered", rendered.contains("School"))
        assertTrue("the direction must still be shown: $rendered", rendered.contains("left"))
        // A28 names three things a nameless row must still show, and the time is the third. It is
        // asserted on what a PERSON sees, not on the parsed value: a crossing whose instant is only
        // in the data model is a crossing the reader of the list cannot place in their day.
        assertTrue("the time must still be shown: $rendered", rendered.contains("2026-07-16 09:30"))
        // And it must not read as a name a family would have chosen.
        assertTrue("the placeholder must not look like a chosen name", AlertText.UNNAMED_DEVICE.startsWith("["))
    }

    /**
     * A16's rendering half. The merge correctly keeps two crossings of one device at one Place at
     * different instants as two rows - but two rows a person cannot tell apart are, to that person,
     * one row shown twice. The list exists to make a dropped alert findable, so "did she leave this
     * morning or this afternoon" has to be answerable off the screen.
     */
    @Test
    fun twoCrossingsOfOneDeviceAtOnePlaceAtDifferentInstantsRenderDifferently() {
        val morning = crossingAt(1_784_194_200L)
        val afternoon = crossingAt(1_784_194_200L + 6 * 60 * 60)

        val a = AlertText.listRow(morning, UTC)
        val b = AlertText.listRow(afternoon, UTC)
        assertNotEquals("two crossings at different instants must not render as the same sentence", a, b)
        assertTrue("the earlier row must carry its own time: $a", a.contains("09:30"))
        assertTrue("the later row must carry its own time: $b", b.contains("15:30"))
    }

    /**
     * The row is rendered in the reader's own clock, not in UTC. A parent looking at "left School"
     * needs the time their own day is measured in; the wire's UTC is the transport, not the display.
     */
    @Test
    fun theRowIsRenderedInTheZoneItIsAskedFor() {
        val c = crossingAt(1_784_194_200L)
        assertTrue(AlertText.listRow(c, UTC).contains("2026-07-16 09:30"))
        assertTrue(AlertText.listRow(c, java.time.ZoneId.of("Australia/Sydney")).contains("2026-07-16 19:30"))
        assertTrue(AlertText.listRow(c, java.time.ZoneId.of("America/Los_Angeles")).contains("2026-07-16 02:30"))
    }

    /**
     * A ts no calendar can render is a server sending nonsense. The row still shows the time it was
     * given, rather than throwing while the list is being drawn and taking every other crossing off
     * the screen with it.
     */
    @Test
    fun aTimeNoCalendarCanRenderIsShownRatherThanThrown() {
        val rendered = AlertText.listRow(crossingAt(Long.MAX_VALUE), UTC)
        assertTrue("got $rendered", rendered.contains(Long.MAX_VALUE.toString()))
        assertTrue("the rest of the row must survive it: $rendered", rendered.contains("School"))
    }

    private fun crossingAt(instantSeconds: Long) = Crossing(
        deviceId = "dev-1",
        deviceName = "Alice's phone",
        placeId = "place-1",
        placeName = "School",
        transition = Transition.EXIT,
        instantSeconds = instantSeconds,
    )

    @Test
    fun aBlankDeviceNameIsTreatedAsAbsentRatherThanAsANameOfSpaces() {
        val blank = """
            {"device_id":"dev-1","device_name":"  ","place_id":"place-1","place_name":"School",
             "transition":"enter","ts":1}
        """.trimIndent()
        assertNull(CrossingsResponse.parse("[$blank]").single().deviceName)
    }

    @Test
    fun anEmptyFamilyIsAnEmptyListAndNotAnError() {
        assertEquals(emptyList<Crossing>(), CrossingsResponse.parse("[]"))
    }

    @Test
    fun aBodyThisClientCannotReadRaisesRatherThanLookingLikeAnEmptyFamily() {
        val bad = listOf(
            "" to "empty body",
            "not json" to "not JSON at all",
            "{}" to "an object where an array belongs",
            "[1,2,3]" to "rows that are not objects",
            "[] trailing" to "trailing content",
            """[{"device_id":"d","place_id":"p","place_name":"P","transition":"enter"}]""" to "a row with no ts",
            """[{"device_id":"d","place_id":"p","place_name":"P","ts":1}]""" to "a row with no direction",
            """[{"device_id":"d","place_id":"p","place_name":"P","transition":"hovered","ts":1}]""" to "an unknown direction",
            """[{"place_id":"p","place_name":"P","transition":"enter","ts":1}]""" to "a row with no device id",
            """[{"device_id":"d","place_id":"p","place_name":"P","transition":"enter","ts":1.5}]""" to "a fractional ts",
        )
        for ((body, why) in bad) {
            val thrown = assertThrows("[$why] must be refused", CrossingsResponse.Unreadable::class.java) {
                CrossingsResponse.parse(body)
            }
            assertTrue("[$why] the refusal must say something", !thrown.message.isNullOrBlank())
        }
    }

    @Test
    fun escapedNamesSurviveTheRoundTrip() {
        val escaped = """
            [{"device_id":"d","device_name":"Bob \"BJ\" Smith's phone","place_id":"p",
              "place_name":"Nan & Pop's","transition":"enter","ts":1}]
        """.trimIndent()
        val c = CrossingsResponse.parse(escaped).single()
        assertEquals("Bob \"BJ\" Smith's phone", c.deviceName)
        assertEquals("Nan & Pop's", c.placeName)
    }
}

package com.nschatz.tracker.alert

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Reading `GET /v1/geofence-events` into crossings.
 *
 * Two things are load-bearing here and both are about NOT lying: an absent device name stays absent
 * rather than becoming an invented one, and a body this client cannot read raises rather than
 * returning an empty list that would be shown as "your family has no crossings".
 */
class CrossingsResponseTest {

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
        assertEquals("Alice's phone arrived at School", AlertText.listRow(c))
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

        val rendered = AlertText.listRow(c)
        assertTrue("the placeholder must be visible: $rendered", rendered.contains(AlertText.UNNAMED_DEVICE))
        assertTrue("the Place must still be shown: $rendered", rendered.contains("School"))
        assertTrue("the direction must still be shown: $rendered", rendered.contains("left"))
        // And it must not read as a name a family would have chosen.
        assertTrue("the placeholder must not look like a chosen name", AlertText.UNNAMED_DEVICE.startsWith("["))
    }

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

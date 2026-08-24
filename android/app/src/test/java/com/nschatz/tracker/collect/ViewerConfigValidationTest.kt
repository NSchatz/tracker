package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Validating the VIEWER credential the alert surface reads and registers with.
 *
 * It is a second, separate credential because the server's two credentials are separate by
 * construction: a device token is a `401` on every route this one reaches. So it validates
 * separately, fails separately, and says which of the two is missing - an app that reported one
 * "incomplete configuration" would send an operator looking in the wrong place, and would make a
 * perfectly good collection setup look broken because nobody had configured alerts yet.
 *
 * The URL rules are the SAME rules, deliberately: a viewer token is a bearer credential too, and one
 * that reads a family's whole crossing history rather than writing one phone's fixes.
 */
class ViewerConfigValidationTest {

    private val exampleViewerCredential = "EXAMPLE-VIEWER-TOKEN"

    @Test
    fun acceptsAnHttpsUrlWithAViewerCredential() {
        val status = ConfigValidation.validateViewer("https://tracker.example.org", exampleViewerCredential)
        val config = (status as ViewerConfigStatus.Configured).config
        assertEquals("https://tracker.example.org", config.baseUrl)
        assertEquals(exampleViewerCredential, config.viewerToken)
    }

    @Test
    fun trimsWhitespaceAndTrailingSlashes() {
        val status = ConfigValidation.validateViewer("  https://tracker.example.org//  ", "  $exampleViewerCredential ")
        val config = (status as ViewerConfigStatus.Configured).config
        assertEquals("https://tracker.example.org", config.baseUrl)
        assertEquals(exampleViewerCredential, config.viewerToken)
    }

    @Test
    fun reportsAMissingViewerCredentialByName() {
        for (value in listOf(null, "", "   ")) {
            val status = ConfigValidation.validateViewer("https://tracker.example.org", value)
            assertTrue("[$value] should be incomplete", status is ViewerConfigStatus.Incomplete)
            val reason = (status as ViewerConfigStatus.Incomplete).reason
            assertTrue("the reason must name the command that issues it: $reason", reason.contains("add-viewer"))
        }
    }

    @Test
    fun reportsAMissingUrl() {
        for (value in listOf(null, "", "   ")) {
            val status = ConfigValidation.validateViewer(value, exampleViewerCredential)
            assertTrue("[$value] should be incomplete", status is ViewerConfigStatus.Incomplete)
            assertTrue((status as ViewerConfigStatus.Incomplete).reason.contains("server URL", ignoreCase = true))
        }
    }

    /**
     * A bearer credential over plaintext is readable by anyone on the path, and this one reads a
     * family's whole crossing history. It is refused by default here for exactly the same reason the
     * device token is, and the sentence names WHICH credential is at risk.
     */
    @Test
    fun refusesPlaintextHttpByDefaultAndSaysWhichCredentialIsAtRisk() {
        val status = ConfigValidation.validateViewer("http://tracker.example.org", exampleViewerCredential)
        assertTrue(status is ViewerConfigStatus.Incomplete)
        val reason = (status as ViewerConfigStatus.Incomplete).reason
        assertTrue("plaintext must be named: $reason", reason.contains("plaintext"))
        assertTrue("the viewer token must be named: $reason", reason.contains("viewer token"))
    }

    @Test
    fun allowsPlaintextHttpOnlyWhenTheCallerOptsIn() {
        val status = ConfigValidation.validateViewer(
            "http://10.0.2.2:8080",
            exampleViewerCredential,
            allowPlaintextHttp = true,
        )
        assertEquals("http://10.0.2.2:8080", (status as ViewerConfigStatus.Configured).config.baseUrl)
    }

    @Test
    fun refusesASchemeWithNoHost() {
        for (url in listOf("https://", "https:///path", "ftp://tracker.example.org")) {
            val status = ConfigValidation.validateViewer(url, exampleViewerCredential)
            assertTrue("[$url] should be refused", status is ViewerConfigStatus.Incomplete)
        }
    }

    /**
     * The two credentials are validated independently. A device token that is present and a viewer
     * token that is not must produce a usable collection config and an unusable alert one, not one
     * verdict for both.
     */
    @Test
    fun theTwoCredentialsFailIndependently() {
        val collection = ConfigValidation.validate("https://tracker.example.org", "EXAMPLE-DEVICE-TOKEN")
        val alerts = ConfigValidation.validateViewer("https://tracker.example.org", null)
        assertTrue(collection is ConfigStatus.Configured)
        assertTrue(alerts is ViewerConfigStatus.Incomplete)
    }
}

package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Validating the client's server URL and device credential.
 *
 * The fail-safe under test: an incomplete configuration produces a **named reason**, never a
 * best-effort attempt. An app that starts a foreground service saying "sharing your location" while
 * posting to a URL nobody set is the silent no-op this project treats as worse than an outage.
 */
class ConfigValidationTest {

    private val exampleCredential = "EXAMPLE-DEVICE-TOKEN"

    @Test
    fun acceptsAnHttpsUrlWithACredential() {
        val status = ConfigValidation.validate("https://tracker.example.org", exampleCredential)
        val config = (status as ConfigStatus.Configured).config
        assertEquals("https://tracker.example.org", config.baseUrl)
        assertEquals(exampleCredential, config.deviceToken)
    }

    @Test
    fun trimsWhitespaceAndTrailingSlashes() {
        val status = ConfigValidation.validate("  https://tracker.example.org///  ", "  $exampleCredential  ")
        val config = (status as ConfigStatus.Configured).config
        assertEquals("https://tracker.example.org", config.baseUrl)
        assertEquals(exampleCredential, config.deviceToken)
    }

    @Test
    fun reportsAMissingUrl() {
        for (value in listOf(null, "", "   ")) {
            val status = ConfigValidation.validate(value, exampleCredential)
            assertTrue("[$value] should be incomplete", status is ConfigStatus.Incomplete)
            assertTrue((status as ConfigStatus.Incomplete).reason.contains("server URL", ignoreCase = true))
        }
    }

    @Test
    fun reportsAMissingCredential() {
        for (value in listOf(null, "", "   ")) {
            val status = ConfigValidation.validate("https://tracker.example.org", value)
            assertTrue("[$value] should be incomplete", status is ConfigStatus.Incomplete)
            assertTrue((status as ConfigStatus.Incomplete).reason.contains("enroll"))
        }
    }

    /**
     * A bearer credential over plaintext is readable by anyone on the path, and `SPEC.md` says so
     * outright. Refusing by default means shipping plaintext has to be something somebody typed,
     * not something that happened.
     */
    @Test
    fun refusesPlaintextHttpByDefault() {
        val status = ConfigValidation.validate("http://tracker.example.org", exampleCredential)
        assertTrue(status is ConfigStatus.Incomplete)
        assertTrue((status as ConfigStatus.Incomplete).reason.contains("plaintext"))
    }

    @Test
    fun allowsPlaintextHttpOnlyWhenExplicitlyPermitted() {
        val status = ConfigValidation.validate(
            "http://10.0.2.2:8080",
            exampleCredential,
            allowPlaintextHttp = true,
        )
        assertEquals("http://10.0.2.2:8080", (status as ConfigStatus.Configured).config.baseUrl)
    }

    @Test
    fun rejectsAUrlWithNoRecognisedScheme() {
        for (url in listOf("tracker.example.org", "ftp://tracker.example.org", "://x", "/v1/fixes")) {
            val status = ConfigValidation.validate(url, exampleCredential)
            assertTrue("[$url] should be refused", status is ConfigStatus.Incomplete)
        }
    }

    /**
     * `https://` alone parses into a `URL` object without complaint and then fails on every single
     * report with an opaque IOException. Catching it here turns that into one sentence, once.
     */
    @Test
    fun rejectsASchemeWithNoHost() {
        for (url in listOf("https://", "https:///v1", "http://", "https:// ")) {
            val status = ConfigValidation.validate(url, exampleCredential, allowPlaintextHttp = true)
            assertTrue("[$url] should be refused", status is ConfigStatus.Incomplete)
        }
    }

    @Test
    fun schemeMatchingIsCaseInsensitive() {
        val status = ConfigValidation.validate("HTTPS://Tracker.Example.Org", exampleCredential)
        assertTrue(status is ConfigStatus.Configured)
    }

    /** The URL check runs before the credential check, so the first fix named is the first to fix. */
    @Test
    fun aMissingUrlIsReportedBeforeAMissingCredential() {
        val status = ConfigValidation.validate(null, null)
        assertTrue((status as ConfigStatus.Incomplete).reason.contains("server URL", ignoreCase = true))
    }
}

package com.nschatz.tracker.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Classifying the server's answer to `POST /v1/fixes`.
 *
 * The retryable/permanent split is the load-bearing decision, and it is wrong in an expensive way in
 * both directions: retrying a permanent rejection is an infinite loop burning radio and battery on a
 * payload the server will never take, while treating a transient failure as permanent silently drops
 * a real fix (risk path #3).
 */
class ReportOutcomeTest {

    @Test
    fun a201IsAFreshlyStoredFix() {
        val outcome = ReportOutcome.fromStatus(201)
        assertEquals(ReportOutcome.Stored, outcome)
        assertTrue(outcome.isDelivered)
        assertFalse(outcome.isRetryable)
    }

    /**
     * `200` is the server saying "I already had this one" (`SPEC.md`, "Idempotency"). That is a
     * **success**: the fix is on the server, exactly once, which is the guarantee that makes retrying
     * safe at all. A client that treated it as a failure would be misreading its own safety net —
     * and would retry a fix that is already stored, forever.
     */
    @Test
    fun a200IsAnIdempotentReplayAndCountsAsDelivered() {
        val outcome = ReportOutcome.fromStatus(200)
        assertEquals(ReportOutcome.Duplicate, outcome)
        assertTrue("a deduped replay means the fix IS on the server", outcome.isDelivered)
        assertFalse(outcome.isRetryable)
    }

    @Test
    fun aBadRequestIsPermanentBecauseThePayloadWillNeverBeAccepted() {
        val outcome = ReportOutcome.fromStatus(400, "invalid_fix", "latitude 122 is outside [-90, 90]")
        assertFalse(outcome.isRetryable)
        assertFalse(outcome.isDelivered)
        val rejected = outcome as ReportOutcome.Rejected
        assertEquals(400, rejected.status)
        assertEquals("invalid_fix", rejected.errorCode)
    }

    @Test
    fun anUnauthorizedResponseIsPermanentBecauseItNeedsAHumanNotABackoff() {
        for (status in listOf(401, 403)) {
            val outcome = ReportOutcome.fromStatus(status, "unauthorized")
            assertFalse("HTTP $status must not be retried", outcome.isRetryable)
            assertFalse(outcome.isDelivered)
        }
    }

    @Test
    fun serverErrorsAreRetryableIncludingTheTypedInternalError() {
        for (status in listOf(500, 502, 503, 504, 599)) {
            val outcome = ReportOutcome.fromStatus(status, "internal")
            assertTrue("HTTP $status must be retried", outcome.isRetryable)
            assertFalse(outcome.isDelivered)
        }
    }

    /** Throttles and timeouts say "not now", not "not ever". */
    @Test
    fun throttlesAndTimeoutsAreRetryable() {
        for (status in listOf(408, 425, 429)) {
            assertTrue("HTTP $status must be retried", ReportOutcome.fromStatus(status).isRetryable)
        }
    }

    /**
     * A 3xx or an unexpected 2xx means this client is not talking to the API it thinks it is — a
     * captive portal, a proxy, a misconfigured URL. Retrying forever against it would look exactly
     * like a working client that never reports, which is the silent failure the fail-safe stance
     * forbids. Refuse, so the reason surfaces.
     */
    @Test
    fun aResponseFromSomethingThatIsNotTheApiIsPermanent() {
        for (status in listOf(204, 301, 302, 304, 418)) {
            val outcome = ReportOutcome.fromStatus(status)
            assertFalse("HTTP $status must not be retried", outcome.isRetryable)
            assertFalse("HTTP $status must not count as delivered", outcome.isDelivered)
        }
    }

    /** Exactly two statuses may ever be reported as delivered. */
    @Test
    fun onlyTheTwoDocumentedSuccessStatusesCountAsDelivered() {
        val delivered = (100..599).filter { ReportOutcome.fromStatus(it).isDelivered }
        assertEquals(listOf(200, 201), delivered)
    }

    /** Nothing may be both retryable and delivered — that combination has no meaning. */
    @Test
    fun noStatusIsBothDeliveredAndRetryable() {
        for (status in 100..599) {
            val outcome = ReportOutcome.fromStatus(status)
            assertFalse("HTTP $status", outcome.isDelivered && outcome.isRetryable)
        }
    }

    /**
     * The user-facing sentence must be useful with **no** error body, because on a `401` there is
     * none: `HttpURLConnection` intercepts that status and discards the response. Formatting two
     * nulls into "(HTTP 401 ): " would turn the single most likely misconfiguration — a wrong or
     * revoked device token — into an empty message.
     */
    @Test
    fun anAuthFailureDescribesItselfWithoutAServerBody() {
        for (status in listOf(401, 403)) {
            val text = ReportOutcome.Rejected(status, errorCode = null, message = null).describe()
            assertTrue("HTTP $status: $text", text.contains("token"))
            assertTrue("HTTP $status: $text", text.contains("enroll"))
            assertFalse("must not leave dangling punctuation: $text", text.contains("( )"))
        }
    }

    @Test
    fun anInvalidFixDescribesItselfUsingTheServersOwnWords() {
        val text = ReportOutcome.Rejected(
            status = 400,
            errorCode = "invalid_fix",
            message = "latitude 122.3321 is outside [-90, 90]",
        ).describe()
        assertTrue(text, text.contains("400"))
        assertTrue(text, text.contains("invalid_fix"))
        assertTrue(text, text.contains("outside"))
    }

    @Test
    fun aDescriptionWithNoBodyStillNamesTheStatus() {
        val text = ReportOutcome.Rejected(418, errorCode = null, message = null).describe()
        assertTrue(text, text.contains("418"))
    }

    @Test
    fun aNetworkFailureIsRetryable() {
        val outcome = ReportOutcome.Retryable("SocketTimeoutException: connect timed out")
        assertTrue(outcome.isRetryable)
        assertFalse(outcome.isDelivered)
    }
}

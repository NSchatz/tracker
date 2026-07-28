package com.nschatz.tracker.protocol

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * The reporter, exercised over **real HTTP** against a real loopback socket ([TestHttpServer]).
 *
 * ### What this proves
 *
 * That the client emits exactly the request `SPEC.md` documents: `POST /v1/fixes`, an
 * `Authorization: Bearer …` device credential, `Content-Type: application/json`, and a body that is
 * byte-for-byte the first-party schema. The server decodes that body with unknown-field rejection
 * and a no-trailing-data check, so "close enough" is a `400` on a real phone.
 *
 * ### What it does not
 *
 * That the **real** tracker server accepts these bytes — that is the Go suite's job on its side of
 * the wire, plus the operator's end-to-end device check (`android/README.md`). This is the client
 * half of the contract, held to the written spec rather than to a copy of the client's own
 * assumptions.
 */
class FixReporterTest {

    private lateinit var server: TestHttpServer

    private val exampleCredential = "EXAMPLE-DEVICE-TOKEN-abc123"

    private val fix = Fix(
        lat = 41.9028,
        lon = 12.4964,
        tsEpochSeconds = 1_752_566_400,
        accuracyM = 5.0,
        batteryPct = 88,
        speedMps = 1.4,
        trigger = FixTrigger.PERIODIC,
        msgId = "m-1",
    )

    @Before
    fun startServer() {
        server = TestHttpServer()
    }

    @After
    fun stopServer() {
        server.close()
    }

    private fun reporter(url: String = server.baseUrl) = FixReporter(url, exampleCredential)

    private fun lastRequest(): RecordedRequest {
        assertFalse("the client sent no request at all", server.requests.isEmpty())
        return server.requests.last()
    }

    // --- the request the client actually emits --------------------------------------------------

    @Test
    fun postsToTheV1FixesPath() {
        reporter().post(fix.toJsonBody())
        val request = lastRequest()
        assertEquals("POST", request.method)
        assertEquals("/v1/fixes", request.target)
    }

    /** The device (write) credential, in the canonical Bearer form SPEC.md documents. */
    @Test
    fun sendsTheDeviceTokenAsABearerCredential() {
        reporter().post(fix.toJsonBody())
        assertEquals("Bearer $exampleCredential", lastRequest().headers["authorization"])
    }

    @Test
    fun sendsJsonContentType() {
        reporter().post(fix.toJsonBody())
        assertEquals("application/json", lastRequest().headers["content-type"])
    }

    /**
     * The exact body — the contract, not a convenience. A misspelled key here is a `400` from the
     * server's strict decoder, and nothing else in the build would catch it.
     */
    @Test
    fun sendsExactlyTheFirstPartySchemaBody() {
        reporter().post(fix.toJsonBody())
        assertEquals(
            """{"lat":41.9028,"lon":12.4964,"ts":1752566400,"accuracy":5.0,"battery":88,""" +
                """"speed":1.4,"trigger":"periodic","msg_id":"m-1"}""",
            lastRequest().body,
        )
    }

    @Test
    fun omitsAbsentOptionalFieldsFromTheWireBody() {
        reporter().post(Fix(lat = 1.0, lon = 2.0, tsEpochSeconds = 3).toJsonBody())
        assertEquals("""{"lat":1.0,"lon":2.0,"ts":3}""", lastRequest().body)
    }

    /**
     * A declared `Content-Length` rather than chunked transfer encoding. The server caps bodies at
     * 64 KiB and can refuse an over-length report before reading it; a chunked body hides the length
     * until it has all arrived.
     */
    @Test
    fun sendsAFixedContentLengthRatherThanChunkedEncoding() {
        reporter().post(fix.toJsonBody())
        val request = lastRequest()
        assertNotNull("Content-Length must be declared", request.headers["content-length"])
        assertEquals(request.body.toByteArray(Charsets.UTF_8).size, request.headers["content-length"]!!.toInt())
        assertNull("must not be chunked", request.headers["transfer-encoding"])
    }

    @Test
    fun toleratesATrailingSlashOnTheConfiguredBaseUrl() {
        reporter("${server.baseUrl}/").post(fix.toJsonBody())
        assertEquals("/v1/fixes", lastRequest().target)
    }

    // --- classifying the server's answer --------------------------------------------------------

    @Test
    fun a201IsReportedAsStored() {
        server.responseStatus = 201
        assertEquals(ReportOutcome.Stored, reporter().post(fix.toJsonBody()))
    }

    @Test
    fun a200IsReportedAsAnIdempotentDuplicate() {
        server.responseStatus = 200
        server.responseBody = """{"status":"duplicate","deduped":true}"""
        val outcome = reporter().post(fix.toJsonBody())
        assertEquals(ReportOutcome.Duplicate, outcome)
        assertTrue("a replay means the fix is on the server", outcome.isDelivered)
    }

    /** The typed error body is surfaced so the device can say *why*, not just "failed". */
    @Test
    fun a400CarriesTheServersTypedErrorThrough() {
        server.responseStatus = 400
        server.responseBody = """{"error":"invalid_fix","message":"latitude 122.3321 is outside [-90, 90]"}"""
        val outcome = reporter().post(fix.toJsonBody()) as ReportOutcome.Rejected
        assertEquals(400, outcome.status)
        assertEquals("invalid_fix", outcome.errorCode)
        assertNotNull(outcome.message)
        assertTrue(outcome.message!!.contains("outside"))
        assertFalse("a 400 must never be retried", outcome.isRetryable)
    }

    /**
     * A `401` is permanent — and its body is **unreadable**, which is a platform behaviour worth
     * pinning rather than discovering on a phone.
     *
     * The **desktop JDK's** `HttpURLConnection` — the one these JVM unit tests link — intercepts
     * `401`/`407` for its built-in authentication handling and discards the response body:
     * `getErrorStream()` returns `null`, with or without a `WWW-Authenticate` header (the real
     * tracker server sends one; this test server mirrors that). So the typed `unauthorized` code is
     * simply not available to the client here.
     *
     * **Scope, stated honestly:** Android's `HttpURLConnection` is a different, OkHttp-backed
     * implementation and may surface the body. This test asserts the JVM behaviour because that is
     * what it can observe; it is not a claim about the phone. What it locks in is the thing that
     * must hold on *both*: the invariants that matter — permanent, not delivered, correct status —
     * and that [ReportOutcome.Rejected.describe] produces a useful sentence **without** the body, so
     * the message is right on whichever platform the client happens to be running.
     */
    @Test
    fun a401IsPermanentEvenThoughTheJdkSwallowsItsBody() {
        server.responseStatus = 401
        server.responseBody = """{"error":"unauthorized","message":"unknown or revoked token"}"""
        val outcome = reporter().post(fix.toJsonBody()) as ReportOutcome.Rejected
        assertEquals(401, outcome.status)
        assertFalse("a 401 must never be retried", outcome.isRetryable)
        assertFalse(outcome.isDelivered)
        assertNull("HttpURLConnection discards the 401 body — pinned deliberately", outcome.errorCode)
        assertTrue(
            "the user-facing sentence must not depend on the swallowed body: ${outcome.describe()}",
            outcome.describe().contains("token"),
        )
    }

    /** A 403, unlike a 401, is not intercepted, so its typed body does come through. */
    @Test
    fun a403IsPermanentAndCarriesTheServersTypedError() {
        server.responseStatus = 403
        server.responseBody = """{"error":"unauthorized","message":"device belongs to another family"}"""
        val outcome = reporter().post(fix.toJsonBody()) as ReportOutcome.Rejected
        assertEquals(403, outcome.status)
        assertFalse(outcome.isRetryable)
        assertTrue(outcome.describe().contains("token"))
    }

    @Test
    fun a500IsRetryable() {
        server.responseStatus = 500
        server.responseBody = """{"error":"internal","message":"could not store the fix"}"""
        val outcome = reporter().post(fix.toJsonBody())
        assertTrue(outcome.isRetryable)
        assertFalse(outcome.isDelivered)
    }

    /**
     * An unreachable server must come back as a retryable outcome, not an exception. On a phone this
     * is the *normal* case — a tunnel, a lift, airplane mode — and a reporter that threw would turn
     * every dead spot into a crash inside the collection service.
     */
    @Test
    fun anUnreachableServerIsRetryableRatherThanAnException() {
        // Port 1 on loopback: reliably refused, no DNS lookup, no timeout wait.
        val outcome = FixReporter("http://127.0.0.1:1", exampleCredential).post(fix.toJsonBody())
        assertTrue("expected retryable, got $outcome", outcome.isRetryable)
        assertFalse(outcome.isDelivered)
    }

    /** One report is one request. Any retry loop is the caller's, never a hidden one in here. */
    @Test
    fun oneReportSendsExactlyOneRequest() {
        reporter().post(fix.toJsonBody())
        assertEquals(1, server.requests.size)
    }

    /**
     * A body the client cannot parse must not change the verdict. Classification comes from the
     * status; the body only supplies a sentence. A captive portal returning HTML with a 503 is still
     * a retryable 503.
     */
    @Test
    fun anUnparseableErrorBodyDoesNotChangeTheClassification() {
        server.responseStatus = 503
        server.responseBody = "<html><body>Gateway Timeout</body></html>"
        assertTrue(reporter().post(fix.toJsonBody()).isRetryable)

        server.responseStatus = 400
        server.responseBody = "not json at all"
        val rejected = reporter().post(fix.toJsonBody()) as ReportOutcome.Rejected
        assertEquals(400, rejected.status)
        assertNull(rejected.errorCode)
    }

    /** The reporter is called from a background thread; concurrent reports must not interfere. */
    @Test
    fun concurrentReportsEachCompleteIndependently() {
        server.responseStatus = 201
        val reporter = reporter()
        val latch = CountDownLatch(4)
        val outcomes = Collections.synchronizedList(mutableListOf<ReportOutcome>())
        repeat(4) { index ->
            Thread {
                outcomes.add(reporter.post(fix.copy(tsEpochSeconds = 1_752_566_400L + index).toJsonBody()))
                latch.countDown()
            }.start()
        }
        assertTrue("reports did not finish in time", latch.await(30, TimeUnit.SECONDS))
        assertEquals(4, outcomes.size)
        assertTrue("every concurrent report should have stored: $outcomes", outcomes.all { it == ReportOutcome.Stored })
        assertEquals(4, server.requests.size)
    }
}

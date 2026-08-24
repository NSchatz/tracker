package com.nschatz.tracker.alert

import com.nschatz.tracker.protocol.TestHttpServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.ServerSocket

/**
 * The two viewer-credentialled calls, over a REAL loopback socket against a fake server - the same
 * pattern `FixReporterTest` and `QueueFlusherTest` use, and for the same reason: the claim under test
 * is "this client speaks the protocol the server already implements", and a mocked HTTP client is
 * satisfied by a correct client and a broken one alike.
 */
class AlertClientTest {

    private val viewerCredential = "viewer-token-9f2a4c7e1b6d8035"

    private fun client(server: TestHttpServer) = AlertClient(server.baseUrl, viewerCredential)

    /**
     * The headline: a viewer opens the app and sees the family's crossings, read back from the
     * server. This is the fail-safe for a push the backend dropped, so it has to work independently
     * of push having worked at all.
     */
    @Test
    fun aViewerReadsTheFamilysCrossingsFromTheGeofenceEventsRoute() {
        TestHttpServer().use { server ->
            server.responseStatus = 200
            server.responseBody = """
                [{"device_id":"dev-2","device_name":"Bob's phone","place_id":"place-2",
                  "place_name":"Home","transition":"exit","ts":1784197800},
                 {"device_id":"dev-1","device_name":"Alice's phone","place_id":"place-1",
                  "place_name":"School","transition":"enter","ts":1784194200}]
            """.trimIndent()

            val result = client(server).crossings()

            val ok = result as? AlertClient.CrossingsResult.Ok
                ?: error("expected the crossings, got $result")
            assertEquals(2, ok.crossings.size)
            // Rendered in a pinned zone so the row is the same wherever this test runs; the app
            // itself renders in the phone's zone, which is the clock its reader lives in.
            val utc = java.time.ZoneId.of("UTC")
            assertEquals("Bob's phone left Home (2026-07-16 10:30)", AlertText.listRow(ok.crossings[0], utc))
            assertEquals("Alice's phone arrived at School (2026-07-16 09:30)", AlertText.listRow(ok.crossings[1], utc))

            // The request itself: the right route, the viewer credential in a HEADER (never the URL),
            // and a bounded page.
            val request = server.requests.single()
            assertEquals("GET", request.method)
            assertTrue("target = ${request.target}", request.target.startsWith("/v1/geofence-events"))
            assertEquals("Bearer $viewerCredential", request.headers["authorization"])
            assertFalse(
                "the viewer credential must never travel in the URL: ${request.target}",
                request.target.contains(viewerCredential),
            )
        }
    }

    @Test
    fun aRejectedCredentialIsNotAnEmptyFamily() {
        TestHttpServer().use { server ->
            server.responseStatus = 401
            server.responseBody = """{"error":"unauthorized","message":"unknown or revoked token"}"""

            val result = client(server).crossings()
            assertEquals(AlertClient.CrossingsResult.Unauthorized, result)
            assertEquals(CrossingListState.CREDENTIAL_REJECTED, CrossingListView.of(result, emptyList()).state)
        }
    }

    @Test
    fun anUnreachableServerIsNotAnEmptyFamily() {
        // A port nothing is listening on: the connection is refused, which is what "unreachable"
        // means to a phone. Bound and released so the port is real and closed rather than guessed.
        val deadPort = ServerSocket(0).use { it.localPort }

        val result = AlertClient("http://127.0.0.1:$deadPort", viewerCredential).crossings()
        assertTrue("got $result", result is AlertClient.CrossingsResult.Unreachable)
        assertEquals(CrossingListState.SERVER_UNREACHABLE, CrossingListView.of(result, emptyList()).state)
    }

    @Test
    fun anUnreadableAnswerIsNotAnEmptyFamilyEither() {
        TestHttpServer().use { server ->
            server.responseStatus = 200
            server.responseBody = "<html>captive portal</html>"

            val result = client(server).crossings()
            assertTrue("got $result", result is AlertClient.CrossingsResult.ServerError)
            assertEquals(CrossingListState.SERVER_ERROR, CrossingListView.of(result, emptyList()).state)
        }
    }

    @Test
    fun aServerErrorIsNotAnEmptyFamilyEither() {
        TestHttpServer().use { server ->
            server.responseStatus = 500
            server.responseBody = """{"error":"internal","message":"could not read geofence events"}"""

            val result = client(server).crossings()
            assertTrue("got $result", result is AlertClient.CrossingsResult.ServerError)
        }
    }

    @Test
    fun anEmptyFamilyIsAnEmptyList() {
        TestHttpServer().use { server ->
            server.responseStatus = 200
            server.responseBody = "[]"

            val result = client(server).crossings()
            assertEquals(AlertClient.CrossingsResult.Ok(emptyList()), result)
            assertEquals(CrossingListState.EMPTY, CrossingListView.of(result, emptyList()).state)
        }
    }

    // --- registration -----------------------------------------------------------------------

    @Test
    fun registrationPostsTheEndpointUnderTheAuthenticatedViewer() {
        TestHttpServer().use { server ->
            server.responseStatus = 201
            server.responseBody = """{"id":"sub-1","provider":"fcm","configured_provider":"fcm"}"""

            val outcome = client(server).register(provider = "fcm", routingAddress = "phone-address-1")

            assertEquals(RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")), outcome)

            val request = server.requests.single()
            assertEquals("POST", request.method)
            assertEquals("/v1/push-subscriptions", request.target)
            assertEquals("Bearer $viewerCredential", request.headers["authorization"])
            assertEquals("application/json", request.headers["content-type"])
            // No `replaces_token`: an absent optional field is ABSENT, never a fabricated empty one,
            // and the route decodes strictly.
            assertEquals("""{"provider":"fcm","token":"phone-address-1"}""", request.body)
        }
    }

    /**
     * A rotated routing address names the one it supersedes. Without it the old address stays a live
     * row and one crossing reaches this phone twice.
     */
    @Test
    fun aRotatedAddressNamesTheAddressItReplaces() {
        TestHttpServer().use { server ->
            server.responseStatus = 201
            server.responseBody = """{"id":"sub-2","provider":"fcm","configured_provider":"fcm"}"""

            client(server).register(
                provider = "fcm",
                routingAddress = "phone-address-2",
                replacesRoutingAddress = "phone-address-1",
            )

            assertEquals(
                """{"provider":"fcm","token":"phone-address-2","replaces_token":"phone-address-1"}""",
                server.requests.single().body,
            )
        }
    }

    /**
     * The three answers `configured_provider` can give, and the three different states they produce.
     * They are three different things to go and fix, and only one of them is armed.
     */
    @Test
    fun theConfiguredProviderIsReadInAllThreeOfItsForms() {
        val cases = mapOf(
            """{"id":"s","provider":"fcm","configured_provider":"fcm"}""" to
                RegistrationOutcome.Accepted(ConfiguredProvider.Named("fcm")),
            """{"id":"s","provider":"fcm","configured_provider":""}""" to
                RegistrationOutcome.Accepted(ConfiguredProvider.None),
            """{"id":"s","provider":"fcm","configured_provider":"unifiedpush"}""" to
                RegistrationOutcome.Accepted(ConfiguredProvider.Named("unifiedpush")),
            // A server older than the addition that reports it: the field is ABSENT, which is not the
            // same as reporting none, and must not be read as either "none" or "fcm".
            """{"id":"s","provider":"fcm"}""" to
                RegistrationOutcome.Accepted(ConfiguredProvider.Absent),
            // An answer this client cannot read is treated as absent: the app did not learn what the
            // deployment configured, which can never produce armed.
            "not json at all" to RegistrationOutcome.Accepted(ConfiguredProvider.Absent),
        )
        for ((body, want) in cases) {
            TestHttpServer().use { server ->
                server.responseStatus = 201
                server.responseBody = body
                assertEquals("[$body]", want, client(server).register("fcm", "phone-address"))
            }
        }
    }

    @Test
    fun aRefusedRegistrationAndAnUnreachableServerAreDifferentOutcomes() {
        TestHttpServer().use { server ->
            server.responseStatus = 401
            server.responseBody = """{"error":"unauthorized","message":"unknown or revoked token"}"""
            assertEquals(RegistrationOutcome.Refused, client(server).register("fcm", "phone-address"))
        }
        val deadPort = ServerSocket(0).use { it.localPort }
        assertEquals(
            RegistrationOutcome.Unreachable,
            AlertClient("http://127.0.0.1:$deadPort", viewerCredential).register("fcm", "phone-address"),
        )
    }

    /**
     * The whole alert surface, driven end to end from the fake server's answers, produces exactly one
     * state per case - and every one of them is distinguishable from every other and from an empty
     * crossings list.
     */
    @Test
    fun everyAlertStatusBranchIsReachableFromWhatAFakeServerAnswers() {
        fun statusFor(
            registration: RegistrationOutcome?,
            notificationsPermitted: Boolean = true,
            routingAddress: String? = "phone-address",
            viewerCredentialPresent: Boolean = true,
            serverUrlUsable: Boolean = true,
        ) = AlertStatusPolicy.evaluate(
            AlertStatusInputs(
                viewerCredentialPresent,
                serverUrlUsable,
                notificationsPermitted,
                routingAddress,
                registration,
            ),
        )

        val accepted: (String) -> RegistrationOutcome = { body ->
            TestHttpServer().use { server ->
                server.responseStatus = 201
                server.responseBody = body
                client(server).register("fcm", "phone-address")
            }
        }
        val refused = TestHttpServer().use { server ->
            server.responseStatus = 401
            server.responseBody = """{"error":"unauthorized","message":"no"}"""
            client(server).register("fcm", "phone-address")
        }
        val deadPort = ServerSocket(0).use { it.localPort }
        val unreachable = AlertClient("http://127.0.0.1:$deadPort", viewerCredential).register("fcm", "phone-address")

        val states = listOf(
            "not-configured" to statusFor(null, viewerCredentialPresent = false),
            "cannot-show" to statusFor(null, notificationsPermitted = false),
            "R1" to statusFor(null, routingAddress = null),
            "unreachable" to statusFor(unreachable),
            "refused" to statusFor(refused),
            "R2" to statusFor(accepted("""{"id":"s","provider":"fcm","configured_provider":""}""")),
            "R3" to statusFor(accepted("""{"id":"s","provider":"fcm","configured_provider":"unifiedpush"}""")),
            "R4" to statusFor(accepted("""{"id":"s","provider":"fcm"}""")),
            "armed" to statusFor(accepted("""{"id":"s","provider":"fcm","configured_provider":"fcm"}""")),
        )

        assertEquals(
            "every branch must produce a distinct state: $states",
            states.size,
            states.map { it.second }.toSet().size,
        )
        assertEquals(AlertDeliveryStatus.Armed, states.last().second)
        for ((name, state) in states.dropLast(1)) {
            assertTrue("[$name] must not be armed", state != AlertDeliveryStatus.Armed)
        }
    }

    /**
     * The credential appears in nothing the app renders or sends as content.
     *
     * It travels in exactly one place - the `Authorization` header - and that is asserted positively
     * above. Here the assertion is negative and covers every OTHER string this surface produces: the
     * request bodies, the request targets, the failure details, and everything the list and the
     * status would display. A credential in a rendered string is a credential in a screenshot, a bug
     * report and, one refactor later, a log line.
     */
    @Test
    fun theViewerCredentialAppearsInNothingTheAppRendersOrLogs() {
        val produced = mutableListOf<String>()

        TestHttpServer().use { server ->
            server.responseStatus = 500
            server.responseBody = """{"error":"internal","message":"nope"}"""
            val c = client(server)

            val read = c.crossings()
            val registration = c.register("fcm", "phone-address", replacesRoutingAddress = "old-address")

            produced += read.toString()
            produced += registration.toString()
            produced += CrossingListView.of(read, emptyList()).toString()
            produced += AlertStatusPolicy.evaluate(
                AlertStatusInputs(true, true, true, "phone-address", registration),
            ).toString()
            for (request in server.requests) {
                produced += request.body
                produced += request.target
            }
        }

        for (rendered in produced) {
            assertFalse(
                "the viewer credential must not appear in: $rendered",
                rendered.contains(viewerCredential),
            )
        }
    }
}

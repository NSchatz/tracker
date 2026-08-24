package com.nschatz.tracker.alert

import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.nio.charset.StandardCharsets

/**
 * The two VIEWER-credentialled calls the alert surface makes: read the family's crossings, and
 * register this phone's push endpoint.
 *
 * Built on `java.net.HttpURLConnection` for the same reason `FixReporter` is: plain JDK, so it runs
 * and is TESTED in a JVM unit test against a real loopback socket with real bytes on the wire,
 * rather than against a mock that returns what the test told it to.
 *
 * ### Why a second credential, and what it does not do
 *
 * `GET /v1/geofence-events` and `POST /v1/push-subscriptions` are viewer-token routes, and a device
 * token presented there is a `401` by construction - the two credentials live in separate tables and
 * are not interchangeable. So this app cannot reach either with the write credential it already
 * holds, and holds a viewer credential alongside it.
 *
 * That is a real widening of what this phone carries: a viewer token reads the family's whole
 * crossing history. What bounds it here is what this class does NOT do. It calls exactly two routes,
 * neither of which returns a coordinate, and it never touches `/v1/positions`, `/v1/near`,
 * `/v1/devices/{id}/history` or `/v1/stream`, every one of which does. The alert surface holds
 * labels; it does not hold locations.
 *
 * @param baseUrl the server root. Trailing slashes are fine.
 * @param viewerToken the viewer (read) bearer token issued by `tracker add-viewer`.
 */
internal class AlertClient(
    baseUrl: String,
    private val viewerToken: String,
    private val connectTimeoutMillis: Int = 15_000,
    private val readTimeoutMillis: Int = 15_000,
) {
    private val root: String = baseUrl.trimEnd('/')

    /** What a crossings read produced. */
    sealed interface CrossingsResult {
        /** The family's crossings, newest first. An empty list here means the family has none. */
        data class Ok(val crossings: List<Crossing>) : CrossingsResult

        /** The viewer credential was rejected. NOT an empty family. */
        data object Unauthorized : CrossingsResult

        /** The server could not be reached. NOT an empty family. */
        data class Unreachable(val detail: String) : CrossingsResult

        /** The server answered something this client cannot use. NOT an empty family. */
        data class ServerError(val detail: String) : CrossingsResult
    }

    /**
     * Reads the family's recent crossings.
     *
     * Every failure is its own case and none of them is an empty list. That is the whole point: a
     * viewer opening the app to check whether their child got to school must never be shown "no
     * crossings" because the token was wrong or the phone was on a captive-portal wifi.
     */
    fun crossings(limit: Int = DEFAULT_LIMIT): CrossingsResult {
        var connection: HttpURLConnection? = null
        return try {
            connection = open("$root$CROSSINGS_PATH?limit=$limit", "GET")
            val status = connection.responseCode
            val payload = readBodyQuietly(
                if (status in 200..299) connection.inputStream else connection.errorStream,
            )
            when {
                status == HttpURLConnection.HTTP_UNAUTHORIZED -> CrossingsResult.Unauthorized
                status in 200..299 -> try {
                    CrossingsResult.Ok(CrossingsResponse.parse(payload))
                } catch (e: CrossingsResponse.Unreadable) {
                    CrossingsResult.ServerError(e.message ?: "unreadable crossings response")
                }

                else -> CrossingsResult.ServerError("the server answered $status")
            }
        } catch (e: IOException) {
            CrossingsResult.Unreachable(e.javaClass.simpleName + ": " + (e.message ?: "network failure"))
        } finally {
            connection?.disconnect()
        }
    }

    /**
     * Registers this phone's push endpoint under the authenticated viewer.
     *
     * @param provider the backend this routing address belongs to.
     * @param routingAddress the address the server should deliver to.
     * @param replacesRoutingAddress the address this one SUPERSEDES, when the phone is replacing one
     *   it previously registered, and null otherwise. Naming it is what stops a rotated address
     *   leaving a second deliverable row behind, which would buzz this phone twice for one arrival.
     *   The server removes it only if this same viewer registered it, and answers identically either
     *   way, so naming an address that is not ours is refused silently rather than reported.
     */
    fun register(
        provider: String,
        routingAddress: String,
        replacesRoutingAddress: String? = null,
    ): RegistrationOutcome {
        var connection: HttpURLConnection? = null
        return try {
            connection = open("$root$REGISTER_PATH", "POST")
            connection.doOutput = true
            connection.setRequestProperty("Content-Type", "application/json")
            val body = registrationBody(provider, routingAddress, replacesRoutingAddress)
                .toByteArray(StandardCharsets.UTF_8)
            connection.setFixedLengthStreamingMode(body.size)
            connection.outputStream.use { it.write(body) }

            val status = connection.responseCode
            val payload = readBodyQuietly(
                if (status in 200..299) connection.inputStream else connection.errorStream,
            )
            when {
                status in 200..299 -> RegistrationOutcome.Accepted(configuredProviderFrom(payload))
                // Every answered-and-refused status is one state: the registration did not take.
                // Distinguishing a 401 from a 400 here would be a distinction with no different
                // action behind it - both mean "this app is not registered", and the reason is in
                // the log, not in the state machine.
                else -> RegistrationOutcome.Refused
            }
        } catch (_: IOException) {
            RegistrationOutcome.Unreachable
        } finally {
            connection?.disconnect()
        }
    }

    /**
     * Reads `configured_provider` out of an accepted registration.
     *
     * Three answers, and they are three different things (see [ConfiguredProvider]): the field is
     * absent (a server older than the addition that reports it), present and empty (this deployment
     * configured no backend), or present and named. An unreadable body is treated as ABSENT, which
     * is the honest reading - the app did not learn what the deployment configured - and it is the
     * conservative one, because it can never produce `armed`.
     */
    private fun configuredProviderFrom(payload: String): ConfiguredProvider {
        val root = try {
            MiniJson.parse(payload)
        } catch (_: MiniJson.MalformedJson) {
            return ConfiguredProvider.Absent
        }
        val obj = root as? Map<*, *> ?: return ConfiguredProvider.Absent
        if (!obj.containsKey(CONFIGURED_PROVIDER_FIELD)) return ConfiguredProvider.Absent
        val named = (obj[CONFIGURED_PROVIDER_FIELD] as? String)?.trim().orEmpty()
        return if (named.isEmpty()) ConfiguredProvider.None else ConfiguredProvider.Named(named)
    }

    private fun open(url: String, method: String): HttpURLConnection =
        (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = connectTimeoutMillis
            readTimeout = readTimeoutMillis
            // The VIEWER (read) credential. It goes in a header and never in the URL: a query-string
            // credential lands in server logs and referrers, and this one reads a family's whole
            // crossing history.
            setRequestProperty("Authorization", "Bearer $viewerToken")
            setRequestProperty("Accept", "application/json")
            useCaches = false
        }

    /**
     * Bounded, failure-swallowing body read - the same shape and the same reasons as `FixReporter`'s:
     * a misconfigured base URL can point at something that streams megabytes, and `readNBytes` is API
     * 33 while this module's `minSdk` is 29.
     */
    private fun readBodyQuietly(stream: InputStream?): String {
        if (stream == null) return ""
        return try {
            stream.use { input ->
                val buffer = ByteArray(MAX_BODY_BYTES)
                var filled = 0
                while (filled < buffer.size) {
                    val read = input.read(buffer, filled, buffer.size - filled)
                    if (read < 0) break
                    filled += read
                }
                String(buffer, 0, filled, StandardCharsets.UTF_8)
            }
        } catch (_: IOException) {
            ""
        }
    }

    internal companion object {
        const val CROSSINGS_PATH = "/v1/geofence-events"
        const val REGISTER_PATH = "/v1/push-subscriptions"
        const val CONFIGURED_PROVIDER_FIELD = "configured_provider"

        /** How many crossings to ask for. The server clamps to 1..1000; 100 is its own default. */
        const val DEFAULT_LIMIT = 100

        /**
         * A crossings page is a few tens of KiB at the server's 1000-row cap. 512 KiB is generous
         * headroom and still a hard ceiling on what a wrong URL can make this app allocate.
         */
        const val MAX_BODY_BYTES = 512 * 1024

        /**
         * Builds the registration body.
         *
         * `replaces_token` is OMITTED when there is nothing being replaced, never sent as null or as
         * an empty string: the route decodes strictly, an absent optional field is absent, and this
         * repo does not fabricate a field it has no value for.
         */
        fun registrationBody(provider: String, routingAddress: String, replaces: String?): String {
            val sb = StringBuilder()
            sb.append("{\"provider\":").append(jsonString(provider))
            sb.append(",\"token\":").append(jsonString(routingAddress))
            if (!replaces.isNullOrBlank()) {
                sb.append(",\"replaces_token\":").append(jsonString(replaces))
            }
            sb.append('}')
            return sb.toString()
        }

        /** Minimal JSON string escaping, matching `protocol/Fix`'s. */
        fun jsonString(value: String): String {
            val sb = StringBuilder(value.length + 2)
            sb.append('"')
            for (c in value) {
                when {
                    c == '"' -> sb.append("\\\"")
                    c == '\\' -> sb.append("\\\\")
                    c == '\n' -> sb.append("\\n")
                    c == '\r' -> sb.append("\\r")
                    c == '\t' -> sb.append("\\t")
                    c.code < 0x20 -> sb.append("\\u").append(String.format("%04x", c.code))
                    else -> sb.append(c)
                }
            }
            sb.append('"')
            return sb.toString()
        }
    }
}

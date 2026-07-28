package com.nschatz.tracker.protocol

import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.nio.charset.StandardCharsets

/**
 * Posts a [Fix] to tracker's `POST /v1/fixes` (`SPEC.md`).
 *
 * Built on `java.net.HttpURLConnection` rather than a third-party client, for the same reason [Fix]
 * hand-rolls its JSON: `HttpURLConnection` is plain JDK, so this class runs — and is **tested** —
 * in a JVM unit test against a real local HTTP server, with real sockets and real bytes on the
 * wire. That makes the wire contract genuinely provable in a headless gate instead of asserted
 * against a mock that would only ever return what the test told it to.
 *
 * ### What this is not
 *
 * It is a **single attempt**, with no queue behind it. A fix that fails is the caller's problem;
 * there is no on-disk buffer, so a fix that cannot be delivered while the app is running is lost.
 * The durable, offline-surviving queue — persisted across process death, flushed by WorkManager on
 * `NetworkType.CONNECTED` — is **C2**, and it is the phase that closes risk path #3. C1 proves the
 * contract; C2 makes it lossless. See `android/README.md`.
 *
 * @param baseUrl the server root, e.g. `https://tracker.example.org`. Trailing slashes are fine.
 * @param deviceToken the per-device bearer token issued by `tracker enroll`.
 */
class FixReporter(
    baseUrl: String,
    private val deviceToken: String,
    private val connectTimeoutMillis: Int = 15_000,
    private val readTimeoutMillis: Int = 15_000,
) {
    private val endpoint: URL = URL(baseUrl.trimEnd('/') + FIXES_PATH)

    /**
     * Sends one fix and classifies the result.
     *
     * Never throws for a network failure — an unreachable server is an expected, transient state on
     * a phone, so it comes back as [ReportOutcome.Retryable] like any other. It also never *stores*
     * anything: a caller that wants the fix to survive this failure has to hold it itself (C2).
     */
    fun report(fix: Fix): ReportOutcome {
        val body = fix.toJsonBody().toByteArray(StandardCharsets.UTF_8)
        var connection: HttpURLConnection? = null
        return try {
            connection = (endpoint.openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                connectTimeout = connectTimeoutMillis
                readTimeout = readTimeoutMillis
                doOutput = true
                // The device (write) credential. SPEC.md accepts Basic-with-token-as-password too,
                // but that exists only for the stock OwnTracks app; Bearer is the canonical form and
                // the one the first-party client uses.
                setRequestProperty("Authorization", "Bearer $deviceToken")
                setRequestProperty("Content-Type", "application/json")
                setRequestProperty("Accept", "application/json")
                // Length is known, so stream in fixed-length mode: it avoids chunked encoding and
                // lets the server reject an over-length body (the 64 KiB cap) before we send it all.
                setFixedLengthStreamingMode(body.size)
                useCaches = false
            }
            connection.outputStream.use { it.write(body) }

            val status = connection.responseCode
            // A 4xx/5xx surfaces on getErrorStream, not getInputStream. The typed error body is
            // diagnostic only — the classification below is driven by the STATUS, never by parsing
            // a body that a proxy or captive portal may have substituted.
            val payload = readBodyQuietly(
                if (status in 200..299) connection.inputStream else connection.errorStream,
            )
            ReportOutcome.fromStatus(
                status = status,
                errorCode = extractJsonStringField(payload, "error"),
                message = extractJsonStringField(payload, "message"),
            )
        } catch (e: IOException) {
            // No radio, DNS failure, TLS failure, timeout. Says nothing about the payload, so the
            // fix is still deliverable — retryable, not rejected.
            ReportOutcome.Retryable(e.javaClass.simpleName + ": " + (e.message ?: "network failure"))
        } finally {
            connection?.disconnect()
        }
    }

    /**
     * Reads at most [MAX_ERROR_BODY_BYTES] of a response body, swallowing failures.
     *
     * The read is **bounded** on purpose: an error body from tracker is one sentence, but a
     * misconfigured `baseUrl` can point at something that streams megabytes, and a client that
     * reads it all to produce a log line would be a memory bug triggered by a typo.
     *
     * Hand-rolled rather than `InputStream.readNBytes`, which Android Lint correctly flags as API
     * 33 while this module's `minSdk` is 29 — on an Android 10 phone that call is a
     * `NoSuchMethodError` at runtime, in the error path, which is the worst place to discover it.
     */
    private fun readBodyQuietly(stream: InputStream?): String {
        if (stream == null) return ""
        return try {
            stream.use { input ->
                val buffer = ByteArray(MAX_ERROR_BODY_BYTES)
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

    private companion object {
        const val FIXES_PATH = "/v1/fixes"
        const val MAX_ERROR_BODY_BYTES = 4096
    }
}

/**
 * Pulls one top-level string field out of a small JSON object.
 *
 * Deliberately minimal and deliberately **diagnostic-only**: it feeds the human-readable reason on
 * a rejection and nothing else. No control-flow decision is ever made from its result — those come
 * from the HTTP status — so its failure mode is a missing sentence in a log, never a misrouted fix.
 * That is what makes a regex acceptable here where it would not be for a real parser.
 */
internal fun extractJsonStringField(json: String, field: String): String? {
    if (json.isEmpty()) return null
    val pattern = Regex("\"" + Regex.escape(field) + "\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"")
    val raw = pattern.find(json)?.groupValues?.get(1) ?: return null
    return unescapeJsonString(raw)
}

private fun unescapeJsonString(raw: String): String {
    if ('\\' !in raw) return raw
    val sb = StringBuilder(raw.length)
    var i = 0
    while (i < raw.length) {
        val ch = raw[i]
        if (ch != '\\' || i == raw.length - 1) {
            sb.append(ch)
            i++
            continue
        }
        when (val esc = raw[i + 1]) {
            '"' -> { sb.append('"'); i += 2 }
            '\\' -> { sb.append('\\'); i += 2 }
            '/' -> { sb.append('/'); i += 2 }
            'n' -> { sb.append('\n'); i += 2 }
            'r' -> { sb.append('\r'); i += 2 }
            't' -> { sb.append('\t'); i += 2 }
            'b' -> { sb.append('\b'); i += 2 }
            'f' -> { sb.append('\u000C'); i += 2 }
            'u' -> {
                val hex = raw.substring(i + 2, minOf(i + 6, raw.length))
                val code = hex.toIntOrNull(16)
                if (hex.length == 4 && code != null) {
                    sb.append(code.toChar())
                    i += 6
                } else {
                    sb.append(esc)
                    i += 2
                }
            }
            else -> { sb.append(esc); i += 2 }
        }
    }
    return sb.toString()
}

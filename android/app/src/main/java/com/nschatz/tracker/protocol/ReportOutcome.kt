package com.nschatz.tracker.protocol

/**
 * What became of one attempt to report a fix.
 *
 * The distinction that matters is **retryable vs permanent**, and getting it wrong is a real
 * failure mode in both directions:
 *
 * - Retrying a *permanent* rejection is an infinite loop. A `400 invalid_fix` means this exact
 *   payload will be refused every time, forever; a client that retries it burns radio and battery
 *   re-sending a fix the server will never take. A `401` means the token is wrong — replaying it
 *   faster does not make it right, and it needs a human, not a backoff.
 * - Treating a *transient* failure as permanent drops a real fix on the floor, which is risk path
 *   #3 (silent fix loss). A 5xx or a dead radio says nothing about the payload.
 *
 * [Duplicate] is deliberately a **success**, not an error: the server dedups on `(device_id, ts)`
 * and answers `200` to a replay (`SPEC.md`, "Idempotency"). That is the contract that makes retrying
 * safe at all — a report whose response was lost can be sent again without creating a second row —
 * so a client that treated `200` as a failure would be misreading the very guarantee it depends on.
 */
sealed interface ReportOutcome {

    /** Whether another attempt at the *same* payload could plausibly succeed. */
    val isRetryable: Boolean

    /** Whether the fix is now safely on the server (either freshly stored, or already there). */
    val isDelivered: Boolean

    /** `201` — the fix was stored for the first time. */
    data object Stored : ReportOutcome {
        override val isRetryable = false
        override val isDelivered = true
    }

    /** `200` — a replay of a fix the server already had. Idempotent no-op; nothing was lost. */
    data object Duplicate : ReportOutcome {
        override val isRetryable = false
        override val isDelivered = true
    }

    /**
     * The server refused this payload and always will. Carries the server's typed error code
     * (`unauthorized` / `malformed` / `invalid_fix` / `internal`) when one could be read, so the
     * device can say *why* rather than just "failed".
     */
    data class Rejected(val status: Int, val errorCode: String?, val message: String?) : ReportOutcome {
        override val isRetryable = false
        override val isDelivered = false

        /**
         * A sentence for the user, which must work **without** the server's error body.
         *
         * That caveat is not hypothetical, though its scope is narrower than it first looks. On the
         * **desktop JDK** — which is what the JVM unit tests actually link — `HttpURLConnection`
         * intercepts `401` and `407` for its built-in authentication handling and **discards the
         * response body**: `getErrorStream()` returns `null`, with or without a `WWW-Authenticate`
         * header. That was measured, not assumed, and it is pinned by
         * `FixReporterTest.a401IsPermanentEvenThoughTheJdkSwallowsItsBody`.
         *
         * On **Android**, `HttpURLConnection` is a different implementation (OkHttp-backed) and may
         * well surface the body. Nobody has measured that here, and it does not matter: writing the
         * sentence unconditionally means the message is right either way, and the alternative —
         * depending on the body and formatting a bare "(HTTP 401 ): " from two nulls when it is
         * absent — would turn the clearest possible diagnosis into noise on the single most likely
         * misconfiguration a user will hit.
         */
        fun describe(): String = when (status) {
            401, 403 -> "The server rejected this device's token (HTTP $status). Re-enroll the device " +
                "with `tracker enroll` and paste the new token."

            else -> buildString {
                append("The server refused the fix (HTTP ").append(status)
                errorCode?.let { append(" ").append(it) }
                append(")")
                message?.let { append(": ").append(it) }
                append(".")
            }
        }
    }

    /** A transient failure — a 5xx, a throttle, or a network that was not there. Try again later. */
    data class Retryable(val reason: String, val status: Int? = null) : ReportOutcome {
        override val isRetryable = true
        override val isDelivered = false
    }

    companion object {
        /**
         * Classifies an HTTP status from `POST /v1/fixes`.
         *
         * The mapping is driven by `SPEC.md`, not by a generic 4xx/5xx rule of thumb:
         *
         * - **201 / 200** — stored / absorbed replay. Both are success.
         * - **408, 425, 429** — timeout, too-early, throttled. The payload is fine; the moment was
         *   not. Retryable.
         * - **5xx** — the server's problem, explicitly including `500 internal`, which the server
         *   returns when the *database* write failed. Nothing was stored, so the fix is still ours
         *   to deliver. Retryable.
         * - **every other 4xx** — including `400 malformed`, `400 invalid_fix`, `401 unauthorized`
         *   and `403`. Permanent: the payload or the credential is wrong, and no amount of waiting
         *   changes either.
         * - **anything else** (a 3xx, or a status from a captive portal or proxy that is not the
         *   tracker server at all) — permanent, because it means this client is not talking to the
         *   API it thinks it is. Retrying against a misconfigured URL forever would look exactly
         *   like a working client that never reports, which is the silent failure the fail-safe
         *   stance forbids.
         */
        fun fromStatus(status: Int, errorCode: String? = null, message: String? = null): ReportOutcome = when {
            status == 201 -> Stored
            status == 200 -> Duplicate
            status == 408 || status == 425 || status == 429 ->
                Retryable("server asked to try again (HTTP $status)", status)

            status in 500..599 -> Retryable("server error (HTTP $status)", status)
            else -> Rejected(status, errorCode, message)
        }
    }
}

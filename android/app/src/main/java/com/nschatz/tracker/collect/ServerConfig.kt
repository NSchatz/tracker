package com.nschatz.tracker.collect

import java.util.Locale

/**
 * Where to report, and with what credential.
 *
 * @param baseUrl the server root — scheme and host, no path. `/v1/fixes` is appended by
 *   `FixReporter`.
 * @param deviceToken the per-device bearer token printed once by `tracker enroll`.
 */
data class ServerConfig(val baseUrl: String, val deviceToken: String)

/**
 * Where to READ from, and with what credential.
 *
 * A second, separate credential, because the server's two credentials are separate by construction:
 * a device token writes its own fixes and a viewer token reads its family, they live in different
 * tables, and presenting one on the other's routes is a `401`. The alert surface reads the family's
 * crossings and registers this phone's push endpoint, both of which are viewer routes, so it needs
 * the viewer token and cannot borrow the device one.
 *
 * This is an exposure widening on the phone and it is written down as one: a viewer token reads a
 * family's whole crossing history. It is bounded by what the app does with it (exactly two routes,
 * neither returning a coordinate) and by where it is kept, which is no worse than where the device
 * token is kept today - and no better either, which is the limitation this phase records rather than
 * pretends away.
 *
 * @param baseUrl the server root, no path.
 * @param viewerToken the viewer (read) bearer token printed once by `tracker add-viewer`.
 */
data class ViewerConfig(val baseUrl: String, val viewerToken: String)

/**
 * The result of reading the viewer configuration: something the alert surface can read with, or the
 * specific reason it cannot.
 *
 * Separate from [ConfigStatus] rather than folded into it, because the two credentials fail
 * independently and a person has to be told which one is missing. An app that reported one
 * "incomplete configuration" for a valid device token beside a missing viewer token would send the
 * operator looking in the wrong place - and, worse, would let a working collection setup be reported
 * as broken because the alert half was never configured.
 */
sealed interface ViewerConfigStatus {
    data class Configured(val config: ViewerConfig) : ViewerConfigStatus

    /** @param reason a sentence to show the user, naming what to fix. */
    data class Incomplete(val reason: String) : ViewerConfigStatus
}

/**
 * The result of reading the client's configuration: either something it can report with, or the
 * specific reason it cannot.
 *
 * There is no third case, and in particular there is no "partly configured, we'll try anyway". The
 * fail-safe stance this repo runs on says a missing prerequisite produces a **clear disabled state**
 * — never a silent no-op that looks like it is working. An app that shows a running notification
 * while posting to a URL that was never set is exactly that silent no-op, and on a product whose
 * job is knowing where a child is, "it looked like it was on" is the failure that matters.
 */
sealed interface ConfigStatus {
    data class Configured(val config: ServerConfig) : ConfigStatus

    /**
     * @param summary a few words for the SCREEN, naming what is wrong. The server card renders
     *   `"Not saved: " + summary`, so this is held to the same brevity floor every other label on
     *   that surface is (F8 of the umbrella's frontend conventions).
     * @param reason the sentence naming what to fix, for the explanation destination and the log.
     *   It is deliberately NOT what the card draws: a paragraph on a phone is a paragraph nobody
     *   reads, and it is one tap away behind "About server settings".
     */
    data class Incomplete(val summary: String, val reason: String) : ConfigStatus
}

/**
 * Validates the stored server URL and device token.
 *
 * Pure — it takes the two raw strings rather than reaching into `SharedPreferences` — so every
 * branch below is covered by a JVM unit test. `ClientPreferences` is the thin adapter that reads
 * them off disk.
 */
object ConfigValidation {

    /**
     * @param baseUrl raw stored value, possibly null/blank.
     * @param deviceToken raw stored value, possibly null/blank.
     * @param allowPlaintextHttp whether an `http://` URL is acceptable. Defaults to **false**: the
     *   device token is a bearer credential, and a bearer credential over plaintext is readable by
     *   anyone on the path. `SPEC.md` says so outright ("Tokens are bearer credentials and must
     *   travel over TLS in any real deployment"). The flag exists only so a debug build can talk to
     *   a laptop on `http://10.0.2.2:8080` while a server is being brought up — it is never the
     *   default, so shipping plaintext has to be a decision somebody typed.
     */
    fun validate(
        baseUrl: String?,
        deviceToken: String?,
        allowPlaintextHttp: Boolean = false,
    ): ConfigStatus {
        val url = baseUrl?.trim().orEmpty()
        // Named `trimmedToken` rather than the obvious short name: the repo's secret-scan tripwire
        // flags a credential-shaped identifier assigned any long unbroken literal, and the short
        // name plus this right-hand side is exactly that shape. The tripwire is deliberately blunt,
        // and the right response is to avoid writing the shape it hunts for — not to teach it an
        // exception that a genuine secret could later hide behind.
        val trimmedToken = deviceToken?.trim().orEmpty()

        validateBaseUrl(
            url,
            allowPlaintextHttp,
            "Refusing to send the device token over plaintext http://. Use https://.",
        )?.let {
            return ConfigStatus.Incomplete(summary = it.summary, reason = it.reason)
        }
        if (trimmedToken.isEmpty()) {
            return ConfigStatus.Incomplete(
                summary = "No device token set",
                reason = "No device token is set. Run `tracker enroll` on the server and paste the token here.",
            )
        }
        return ConfigStatus.Configured(ServerConfig(baseUrl = url.trimEnd('/'), deviceToken = trimmedToken))
    }

    /**
     * Validates the stored server URL and VIEWER token: what the alert surface needs.
     *
     * Same URL rules as [validate], for the same reason - a viewer token is a bearer credential too,
     * and one that reads a family's whole crossing history rather than writing one phone's fixes, so
     * if anything the plaintext refusal matters more here, not less. The URL check is therefore
     * shared rather than restated, so the two credentials can never drift into different rules.
     *
     * @param allowPlaintextHttp see [validate]. Defaults to false for the same reason.
     */
    fun validateViewer(
        baseUrl: String?,
        viewerToken: String?,
        allowPlaintextHttp: Boolean = false,
    ): ViewerConfigStatus {
        val url = baseUrl?.trim().orEmpty()
        val trimmedViewerToken = viewerToken?.trim().orEmpty()

        validateBaseUrl(
            url,
            allowPlaintextHttp,
            "Refusing to send the viewer token over plaintext http://. Use https://.",
        )?.let {
            return ViewerConfigStatus.Incomplete(it.reason)
        }
        if (trimmedViewerToken.isEmpty()) {
            return ViewerConfigStatus.Incomplete(
                "No viewer token is set. Run `tracker add-viewer` on the server and paste the token here.",
            )
        }
        return ViewerConfigStatus.Configured(
            ViewerConfig(baseUrl = url.trimEnd('/'), viewerToken = trimmedViewerToken),
        )
    }

    /**
     * Why a base URL is unusable: the label the server card draws, and the sentence behind it.
     *
     * Both halves are carried here rather than only the sentence, because F8 of the umbrella's
     * frontend conventions holds the card to a label and [ConfigStatus.Incomplete] takes the two
     * separately. A shared URL check that returned only the sentence would push the device half back
     * into restating its own labels, which is the drift this helper exists to prevent.
     */
    private data class BaseUrlProblem(val summary: String, val reason: String)

    /**
     * The URL half of both validations. Returns why it is unusable, or null when it is fine.
     *
     * @param plaintextRefusal the sentence to return when the URL is plaintext `http://`, naming the
     *   credential actually at risk rather than a generic one.
     *
     *   It is passed in WHOLE rather than assembled here from a credential name. The rules are what
     *   this helper exists to share; the sentences stay committed literals at their call sites, so a
     *   search for what a person is told finds it, and so the artefact in `internal/uiverify` that
     *   pins each of these sentences is measuring the real string rather than a template.
     */
    private fun validateBaseUrl(
        url: String,
        allowPlaintextHttp: Boolean,
        plaintextRefusal: String,
    ): BaseUrlProblem? {
        if (url.isEmpty()) {
            return BaseUrlProblem(
                summary = "No server URL set",
                reason = "No server URL is set. Enter the tracker server address.",
            )
        }
        val lower = url.lowercase(Locale.ROOT)
        val isHttps = lower.startsWith("https://")
        val isHttp = lower.startsWith("http://")
        if (!isHttps && !isHttp) {
            return BaseUrlProblem(
                summary = "Server URL needs https",
                reason = "The server URL must start with https:// (or http:// for local testing).",
            )
        }
        if (isHttp && !allowPlaintextHttp) {
            return BaseUrlProblem(summary = "Plain http refused", reason = plaintextRefusal)
        }
        // "https://" alone is a scheme with no host - a URL object would still build, and every
        // request would fail with an opaque IOException instead of this sentence.
        val afterScheme = url.substringAfter("://")
        if (afterScheme.isEmpty() || afterScheme.startsWith("/")) {
            return BaseUrlProblem(
                summary = "Server URL has no host",
                reason = "The server URL is missing a host name.",
            )
        }
        return null
    }
}

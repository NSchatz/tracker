package com.nschatz.tracker.collect

import android.content.Context
import com.nschatz.tracker.BuildConfig

/**
 * Reads and writes the server URL and device token.
 *
 * ### The plaintext caveat, stated plainly
 *
 * This uses ordinary [android.content.SharedPreferences], which means **the device token is stored
 * in plaintext** in the app's private data directory. On a non-rooted device with a lock screen that
 * is not readable by other apps, but it is readable by anyone with root, an unlocked bootloader, or
 * a full-device backup — and the token is a write credential for a family's location trail.
 *
 * This is a **known, phase-scoped limitation, not an oversight**. The roadmap puts secure storage in
 * **C3**: `EncryptedSharedPreferences` backed by an Android Keystore master key (StrongBox where
 * available), together with device enrollment and the logging-scrub tests. C1's job is to prove the
 * collection and reporting path works at all; storing the token somewhere it can be read is what
 * lets that happen a phase earlier, and the cost is written down here and in `android/README.md`
 * rather than discovered later.
 *
 * `allowBackup="false"` is already set in the manifest, which keeps the token out of cloud backups
 * in the meantime.
 */
class ClientPreferences(context: Context) {

    private val prefs = context.applicationContext
        .getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    /** The stored server URL, or null when never set. */
    var baseUrl: String?
        get() = prefs.getString(KEY_BASE_URL, null)
        set(value) = prefs.edit().putString(KEY_BASE_URL, value?.trim()).apply()

    /** The stored per-device bearer credential, or null when never set. */
    var deviceToken: String?
        get() = prefs.getString(KEY_DEVICE_TOKEN, null)
        set(value) = prefs.edit().putString(KEY_DEVICE_TOKEN, value?.trim()).apply()

    /**
     * The stored VIEWER (read) bearer credential, or null when never set.
     *
     * Held here, beside the device token, and therefore under exactly the same protection: app-private
     * `SharedPreferences`, plaintext, `allowBackup="false"`. That is deliberate rather than lazy - the
     * bar this phase had to meet was "no less protected than the device token is held today", and
     * putting the two in one place is what makes that true by construction and keeps SECRET-3 a
     * single change instead of two.
     *
     * It is also, honestly, the weaker of the two credentials to lose in one way and the stronger in
     * another: it cannot write anything, and it can read the whole family's crossing history. Both
     * halves of that are recorded in `android/README.md` under the limitations SECRET-3 closes.
     */
    var viewerToken: String?
        get() = prefs.getString(KEY_VIEWER_TOKEN, null)
        set(value) = prefs.edit().putString(KEY_VIEWER_TOKEN, value?.trim()).apply()

    /** Whether a viewer credential has been entered at all - the first step of the alert status. */
    fun hasViewerCredential(): Boolean = !viewerToken.isNullOrBlank()

    /**
     * Validates what is stored and reports either a usable config or the reason there is none.
     *
     * Plaintext `http://` is accepted **only in a debug build**. A release build refuses it, so a
     * shipped app cannot be pointed at an unencrypted endpoint and quietly send a bearer credential
     * in the clear; a developer bringing a server up on a laptop still can.
     */
    fun readConfig(): ConfigStatus = ConfigValidation.validate(
        baseUrl = baseUrl,
        deviceToken = deviceToken,
        allowPlaintextHttp = BuildConfig.DEBUG,
    )

    /** The same, for the viewer credential the alert surface reads and registers with. */
    fun readViewerConfig(): ViewerConfigStatus = ConfigValidation.validateViewer(
        baseUrl = baseUrl,
        viewerToken = viewerToken,
        allowPlaintextHttp = BuildConfig.DEBUG,
    )

    /**
     * The routing address this phone last registered, or null when it has never registered one.
     *
     * Kept so a rotated address can name the one it REPLACES. Registration is idempotent on
     * (provider, address), which refreshes an unchanged address but cannot help a rotated one: the
     * new address is a new row, and the old one stays deliverable, so one crossing would reach this
     * phone twice. The phone is the only party that knows its own previous address, so it has to
     * remember it.
     *
     * A phone that has lost this record - a reinstall, a restore onto another handset - cannot name
     * its predecessor, and the stale row survives until the backend reports that address
     * unregistered. That residual is recorded in `android/README.md` rather than solved here; it
     * wastes a send to a dead address and does not double-notify a live phone.
     */
    var registeredRoutingAddress: String?
        get() = prefs.getString(KEY_ROUTING_ADDRESS, null)
        set(value) = prefs.edit().putString(KEY_ROUTING_ADDRESS, value?.trim()).apply()

    private companion object {
        const val PREFS_NAME = "tracker_client"
        const val KEY_BASE_URL = "base_url"
        const val KEY_DEVICE_TOKEN = "device_token"
        const val KEY_VIEWER_TOKEN = "viewer_token"
        const val KEY_ROUTING_ADDRESS = "push_routing_address"
    }
}

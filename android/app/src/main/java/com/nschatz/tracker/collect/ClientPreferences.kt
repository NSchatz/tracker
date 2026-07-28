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

    private companion object {
        const val PREFS_NAME = "tracker_client"
        const val KEY_BASE_URL = "base_url"
        const val KEY_DEVICE_TOKEN = "device_token"
    }
}

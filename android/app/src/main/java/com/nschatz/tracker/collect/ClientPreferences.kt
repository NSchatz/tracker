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

    /**
     * Reads the operator's persisted collection intent.
     *
     * A `ClassCastException` here is not a crash and not a default: something that is not a string
     * is sitting where the intent lives, which is the unreadable case, and it is reported as such.
     * The value is never guessed.
     */
    fun readCollectionIntent(): IntentReading = try {
        CollectionIntentCodec.decode(prefs.getString(KEY_COLLECTION_INTENT, null))
    } catch (_: ClassCastException) {
        IntentReading.Unreadable
    }

    /**
     * Writes the operator's collection intent.
     *
     * `commit()`, not `apply()`, and the same for [writeRecordedReason] below. `apply()` returns
     * before the value is on disk, and the whole point of this value is to be readable **after a
     * power cycle** - including one that happens moments after the operator tapped Stop. One
     * synchronous write of a few bytes, on a control the operator touches by hand, is the right
     * price for the durability the boot path depends on.
     */
    fun writeCollectionIntent(intent: CollectionIntent) {
        prefs.edit().putString(KEY_COLLECTION_INTENT, CollectionIntentCodec.encode(intent)).commit()
    }

    /**
     * Reads the recorded reason, or null when none is held.
     *
     * A stored value that is not one of the known cases reads as **no reason**, never as a
     * fabricated one: an unrecognised case cannot be presented honestly, and inventing a case to
     * show would be worse than showing nothing.
     */
    fun readRecordedReason(): ReasonCase? = try {
        val stored = prefs.getString(KEY_RECORDED_REASON, null)
        ReasonCase.values().firstOrNull { it.name == stored }
    } catch (_: ClassCastException) {
        null
    }

    /** Writes, replaces, or (with null) clears the single recorded reason. */
    fun writeRecordedReason(case: ReasonCase?) {
        val editor = prefs.edit()
        if (case == null) editor.remove(KEY_RECORDED_REASON) else editor.putString(KEY_RECORDED_REASON, case.name)
        editor.commit()
    }

    private companion object {
        const val PREFS_NAME = "tracker_client"
        const val KEY_BASE_URL = "base_url"
        const val KEY_DEVICE_TOKEN = "device_token"

        /**
         * The operator's collection intent, as `"on"` or `"off"`.
         *
         * Named here rather than anywhere else because the operator device check for a corrupt
         * intent needs a documented way to corrupt it: on a debuggable build,
         * `adb shell run-as com.nschatz.tracker` and edit `shared_prefs/tracker_client.xml`, either
         * removing this entry (the missing case) or setting it to a value that is not `on` or `off`
         * (the unreadable case). See `android/README.md`.
         */
        const val KEY_COLLECTION_INTENT = "collection_intent"

        /** The single recorded reason, stored as a [ReasonCase] name. */
        const val KEY_RECORDED_REASON = "collection_recorded_reason"
    }
}

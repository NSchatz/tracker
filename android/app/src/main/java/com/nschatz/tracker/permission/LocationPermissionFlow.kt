package com.nschatz.tracker.permission

import android.Manifest
import android.os.Build

/**
 * What the OS currently grants this app. A plain snapshot — the caller reads it from
 * `ContextCompat.checkSelfPermission` at the framework edge, and everything below reasons about it
 * as data.
 */
data class LocationGrants(
    val fineLocation: Boolean = false,
    val coarseLocation: Boolean = false,
    val backgroundLocation: Boolean = false,
    /** `POST_NOTIFICATIONS`, which only exists on API 33+. Treat as granted below 33. */
    val postNotifications: Boolean = true,
) {
    /** True when *some* foreground location — precise or approximate — is available. */
    val hasForegroundLocation: Boolean get() = fineLocation || coarseLocation
}

/** How much location this app can actually collect right now. */
enum class CollectionCapability {
    /** No location permission at all. Collection is impossible; the UI must say so. */
    NONE,

    /**
     * Foreground location only. A location foreground service **can** be started while the app is
     * visible and will keep running, but the OS will not let the app *start* one from the
     * background — so collection does not survive the app being killed and restarted by the system.
     */
    FOREGROUND_ONLY,

    /** Foreground + background. This is the state the product actually needs. */
    CONTINUOUS,
}

/** Whether the granted location is precise or only approximate. */
enum class LocationPrecision { NONE, APPROXIMATE, PRECISE }

/** The single next thing the app should do to move the permission flow forward. */
sealed interface PermissionStep {

    /**
     * Ask the OS, with the normal runtime dialog, for [permissions].
     *
     * @param permissions never mixes foreground and background location — see
     *   [LocationPermissionFlow.nextStep] for why that separation is the whole point.
     */
    data class RequestRuntime(val permissions: List<String>) : PermissionStep

    /**
     * The runtime dialog cannot grant what is missing; only the app's system settings page can.
     * The caller opens `Settings.ACTION_APPLICATION_DETAILS_SETTINGS`.
     *
     * @param reason which grant the user has to make there, so the educational UI can name it.
     */
    data class OpenAppSettings(val reason: SettingsReason) : PermissionStep

    /** Nothing left to ask for. */
    data object Ready : PermissionStep
}

/** Why the user is being sent to the settings page. */
enum class SettingsReason {
    /**
     * Android 11+ (API 30+): the runtime dialog has **no "Allow all the time" option**, so
     * background location can only ever be granted from the settings page.
     */
    BACKGROUND_LOCATION_NEEDS_SETTINGS,

    /**
     * The user denied foreground location firmly enough that the OS will no longer show the dialog
     * (`shouldShowRequestPermissionRationale` is false after an ask). Calling `requestPermissions`
     * again is a silent no-op, so settings is the only remaining route.
     */
    FOREGROUND_LOCATION_PERMANENTLY_DENIED,
}

/**
 * The two-step background-location permission flow, as a pure state machine.
 *
 * ### The rule this exists to encode
 *
 * `ACCESS_BACKGROUND_LOCATION` became its own runtime permission in **Android 10 (API 29)** — which
 * is exactly why this app's `minSdk` is 29 — and how it is granted then changed again in **Android
 * 11 (API 30)**:
 *
 * | API | how "Allow all the time" is granted |
 * |-----|-------------------------------------|
 * | 29  | the system permission dialog **includes** an "Allow all the time" option |
 * | 30+ | the dialog **does not** include it; the user must enable it on a **settings page** |
 *
 * *(developer.android.com — "Request background location", the primary source cited by the roadmap
 * as `[android bg-location]`.)*
 *
 * So the flow is always **two steps, never one**: get foreground location first, then — as a
 * separate act, with an educational screen in between explaining why — go after background. On 30+
 * that second step is not a permission request at all, it is a trip to settings, and code that
 * calls `requestPermissions(ACCESS_BACKGROUND_LOCATION)` there will simply never succeed.
 *
 * ### Why this is a pure function
 *
 * Runtime permission grants cannot be proven by a headless build — a real grant needs a real user
 * tapping a real dialog on a real device, and a test that asserts a mocked `checkSelfPermission`
 * returned what the test told it to would be evidence of nothing. What *can* be proven, and is, is
 * the **decision**: given a grant state and an API level, which step comes next. That is this
 * object, it has no Android dependency beyond two constant tables, and it is covered exhaustively
 * in `LocationPermissionFlowTest`. The grant itself remains an operator check on a device — see
 * `android/README.md`.
 */
object LocationPermissionFlow {

    /**
     * The permissions to ask for in **step one**.
     *
     * Fine and coarse are requested together: that is the documented pairing, and it lets the user
     * choose "Approximate" without the app breaking. `POST_NOTIFICATIONS` joins the batch on API
     * 33+ because the foreground service's ongoing notification — which is not optional, it is what
     * makes the collection visible to the person being tracked — is suppressed without it.
     *
     * `ACCESS_BACKGROUND_LOCATION` is **deliberately absent**. It is step two, always.
     */
    fun foregroundStepPermissions(sdkInt: Int): List<String> = buildList {
        add(Manifest.permission.ACCESS_FINE_LOCATION)
        add(Manifest.permission.ACCESS_COARSE_LOCATION)
        if (sdkInt >= Build.VERSION_CODES.TIRAMISU) add(POST_NOTIFICATIONS)
    }

    /**
     * The next action to take, given what is granted.
     *
     * @param grants what the OS reports right now.
     * @param sdkInt the running API level (`Build.VERSION.SDK_INT`), passed in so the 29-vs-30 split
     *   is testable on both sides without an emulator.
     * @param foregroundRequestedAtLeastOnce whether step one has already been asked in this
     *   install. Needed to tell "not asked yet" from "asked and refused".
     * @param foregroundRationaleAvailable `shouldShowRequestPermissionRationale` for foreground
     *   location. Combined with the flag above this is Android's only signal for
     *   *permanently denied*: asked before, still not granted, and the OS will not show the dialog
     *   again.
     */
    fun nextStep(
        grants: LocationGrants,
        sdkInt: Int,
        foregroundRequestedAtLeastOnce: Boolean = false,
        foregroundRationaleAvailable: Boolean = false,
    ): PermissionStep {
        // --- Step one: foreground location. Nothing else can proceed without it; the OS will not
        // even consider a background-location grant for an app that has no foreground location.
        if (!grants.hasForegroundLocation) {
            return if (foregroundRequestedAtLeastOnce && !foregroundRationaleAvailable) {
                // Asked, refused, and the dialog is now suppressed. Asking again does nothing at
                // all — not an error, not a dialog, just silence — so routing to settings is the
                // difference between a flow that recovers and one that looks stuck for no reason.
                PermissionStep.OpenAppSettings(SettingsReason.FOREGROUND_LOCATION_PERMANENTLY_DENIED)
            } else {
                PermissionStep.RequestRuntime(foregroundStepPermissions(sdkInt))
            }
        }

        // --- Step two: background location, only ever reached with foreground already in hand.
        if (!grants.backgroundLocation) {
            return if (sdkInt >= Build.VERSION_CODES.R) {
                // Android 11+: the dialog has no "Allow all the time". Settings is the ONLY route.
                PermissionStep.OpenAppSettings(SettingsReason.BACKGROUND_LOCATION_NEEDS_SETTINGS)
            } else {
                // Android 10: background location is still a normal runtime dialog.
                PermissionStep.RequestRuntime(listOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION))
            }
        }

        return PermissionStep.Ready
    }

    /** What collection is actually possible in this grant state. */
    fun capability(grants: LocationGrants): CollectionCapability = when {
        !grants.hasForegroundLocation -> CollectionCapability.NONE
        !grants.backgroundLocation -> CollectionCapability.FOREGROUND_ONLY
        else -> CollectionCapability.CONTINUOUS
    }

    /**
     * Precise vs approximate.
     *
     * Worth surfacing rather than silently accepting: if the user grants only approximate location
     * in the foreground, the app gets **only approximate location in the background too**, however
     * "Allow all the time" is set. For a family tracker that is the difference between "at school"
     * and "somewhere in this part of town", so the UI says which one is in effect instead of
     * implying a precision it does not have.
     */
    fun precision(grants: LocationGrants): LocationPrecision = when {
        grants.fineLocation -> LocationPrecision.PRECISE
        grants.coarseLocation -> LocationPrecision.APPROXIMATE
        else -> LocationPrecision.NONE
    }

    /**
     * Whether an approximate-only grant could still be upgraded to precise.
     *
     * Offered as an optional prompt, never as a blocking step in [nextStep]: a user who
     * deliberately chose "Approximate" must not be trapped in a loop that re-asks forever, and the
     * app works — less precisely, honestly labelled — either way.
     */
    fun canUpgradeToPrecise(grants: LocationGrants): Boolean =
        grants.coarseLocation && !grants.fineLocation

    /**
     * The permissions to request when upgrading an approximate grant to a precise one.
     *
     * **Fine is requested together with coarse, never alone**, and that is not defensive style — it
     * is the documented rule. From **Android 12 (API 31)**, an app that requests
     * `ACCESS_FINE_LOCATION` must request `ACCESS_COARSE_LOCATION` in the same call; a request for
     * fine on its own is **ignored by the system** on some Android 12 releases
     * (developer.android.com, "Request location access at runtime" — the roadmap's
     * `[android permissions]`). This module targets API 34, so a fine-only upgrade request would be
     * a button that silently does nothing: the dialog never appears, the grants re-read unchanged,
     * and the "only approximate location" warning stays on screen forever.
     *
     * It exists as a function here, next to [foregroundStepPermissions], so that **every**
     * permission request in the app is built by this object. The upgrade affordance previously
     * assembled its own array in the UI, which is exactly how it drifted out of step with the rule
     * that [foregroundStepPermissions] has always followed and that this file's tests have always
     * asserted.
     */
    fun preciseUpgradePermissions(): List<String> = listOf(
        Manifest.permission.ACCESS_FINE_LOCATION,
        Manifest.permission.ACCESS_COARSE_LOCATION,
    )

    /**
     * Whether the foreground service's ongoing notification will actually be visible.
     *
     * On API 33+ a denied `POST_NOTIFICATIONS` suppresses it. The service still runs, but the
     * person carrying the phone loses the one always-on signal that it is collecting — so this is
     * surfaced as a warning rather than swallowed.
     */
    fun notificationVisible(grants: LocationGrants, sdkInt: Int): Boolean =
        sdkInt < Build.VERSION_CODES.TIRAMISU || grants.postNotifications

    /**
     * `Manifest.permission.POST_NOTIFICATIONS` as a literal.
     *
     * The constant was added in API 33 and this module compiles against 34, so referencing it
     * directly would be fine — but it is only ever *requested* on 33+, and spelling it out keeps
     * this file's permission table readable as one list of strings rather than a mix of symbols and
     * version guards.
     */
    const val POST_NOTIFICATIONS: String = "android.permission.POST_NOTIFICATIONS"
}

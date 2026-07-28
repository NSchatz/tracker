package com.nschatz.tracker.permission

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The two-step background-location permission flow.
 *
 * ### What this proves, and what it deliberately does not
 *
 * It proves the **decision**: given a grant state and an API level, which step the app takes next.
 * That is real logic with a real failure mode — an app that asks for background location in the same
 * breath as foreground, or that calls `requestPermissions` for background on Android 11 where no
 * dialog will ever appear, is broken in a way no compiler catches and every user experiences as
 * "it just doesn't work".
 *
 * It does **not** prove that a grant can be obtained. Nothing headless can: a runtime grant requires
 * a human tapping a system dialog on a real device. A test that mocked `checkSelfPermission` to
 * return `PERMISSION_GRANTED` and then asserted the app thinks it is granted would be asserting that
 * the mock was configured, which is evidence of nothing. That half is an operator check on a device
 * — see `android/README.md`.
 *
 * API levels used below: 29 = Android 10, 30 = Android 11, 33 = Android 13, 34 = Android 14.
 */
class LocationPermissionFlowTest {

    private val fine = "android.permission.ACCESS_FINE_LOCATION"
    private val coarse = "android.permission.ACCESS_COARSE_LOCATION"
    private val background = "android.permission.ACCESS_BACKGROUND_LOCATION"
    private val notifications = "android.permission.POST_NOTIFICATIONS"

    private val nothingGranted = LocationGrants(postNotifications = false)
    private val foregroundGranted = LocationGrants(
        fineLocation = true, coarseLocation = true, postNotifications = true,
    )
    private val allGranted = foregroundGranted.copy(backgroundLocation = true)

    // --- step one ------------------------------------------------------------------------------

    @Test
    fun stepOneAsksForForegroundLocationOnEveryApiLevel() {
        for (sdk in listOf(29, 30, 31, 33, 34)) {
            val step = LocationPermissionFlow.nextStep(nothingGranted, sdk)
            assertTrue("API $sdk should start with a runtime request", step is PermissionStep.RequestRuntime)
            val requested = (step as PermissionStep.RequestRuntime).permissions
            assertTrue("API $sdk must request fine location", fine in requested)
            assertTrue("API $sdk must request coarse location", coarse in requested)
        }
    }

    /**
     * **The rule this whole class exists for.** Background location is never bundled into the
     * foreground request, on any API level. Android grants background location only as a separate
     * act, and an app that asks for both at once ends up with neither properly explained.
     */
    @Test
    fun stepOneNeverIncludesBackgroundLocation() {
        for (sdk in listOf(29, 30, 31, 33, 34)) {
            val step = LocationPermissionFlow.nextStep(nothingGranted, sdk) as PermissionStep.RequestRuntime
            assertFalse(
                "API $sdk must not bundle background location into the foreground request",
                background in step.permissions,
            )
        }
    }

    @Test
    fun notificationPermissionJoinsStepOneOnlyFromApi33() {
        assertFalse(
            notifications in
                (LocationPermissionFlow.nextStep(nothingGranted, 32) as PermissionStep.RequestRuntime).permissions,
        )
        assertTrue(
            notifications in
                (LocationPermissionFlow.nextStep(nothingGranted, 33) as PermissionStep.RequestRuntime).permissions,
        )
    }

    // --- step two: the 29-vs-30 split ----------------------------------------------------------

    /**
     * Android 10: the system permission dialog still offers "Allow all the time", so background
     * location is an ordinary runtime request.
     */
    @Test
    fun onApi29StepTwoIsARuntimeRequest() {
        val step = LocationPermissionFlow.nextStep(foregroundGranted, sdkInt = 29)
        assertEquals(PermissionStep.RequestRuntime(listOf(background)), step)
    }

    /**
     * Android 11+: the dialog has **no** "Allow all the time" option. Only the settings page can
     * grant it, so calling `requestPermissions` here would silently never succeed — the failure
     * this branch exists to prevent.
     */
    @Test
    fun fromApi30StepTwoIsTheSettingsPageNotADialog() {
        for (sdk in listOf(30, 31, 32, 33, 34)) {
            val step = LocationPermissionFlow.nextStep(foregroundGranted, sdk)
            assertEquals(
                "API $sdk must route background location to settings",
                PermissionStep.OpenAppSettings(SettingsReason.BACKGROUND_LOCATION_NEEDS_SETTINGS),
                step,
            )
        }
    }

    /** Step two is only ever reached once foreground location is actually in hand. */
    @Test
    fun backgroundIsNeverRequestedBeforeForegroundIsGranted() {
        val backgroundOnlyGranted = LocationGrants(backgroundLocation = true)
        val step = LocationPermissionFlow.nextStep(backgroundOnlyGranted, sdkInt = 34)
        assertTrue(step is PermissionStep.RequestRuntime)
        assertTrue(fine in (step as PermissionStep.RequestRuntime).permissions)
    }

    /** An approximate-only foreground grant still progresses to step two. */
    @Test
    fun coarseOnlyStillReachesTheBackgroundStep() {
        val coarseOnly = LocationGrants(coarseLocation = true)
        assertEquals(
            PermissionStep.OpenAppSettings(SettingsReason.BACKGROUND_LOCATION_NEEDS_SETTINGS),
            LocationPermissionFlow.nextStep(coarseOnly, sdkInt = 34),
        )
    }

    // --- terminal and stuck states -------------------------------------------------------------

    @Test
    fun everythingGrantedIsReady() {
        for (sdk in listOf(29, 30, 34)) {
            assertEquals(PermissionStep.Ready, LocationPermissionFlow.nextStep(allGranted, sdk))
        }
    }

    /**
     * Denied firmly enough that Android stops showing the prompt: asked before, still not granted,
     * and no rationale available. Re-requesting is a **silent no-op** — no dialog, no callback, no
     * error — so a flow that kept calling `requestPermissions` would look permanently stuck on a
     * button that does nothing. Settings is the only route left.
     */
    @Test
    fun aPermanentlyDeniedForegroundGrantRoutesToSettings() {
        val step = LocationPermissionFlow.nextStep(
            grants = nothingGranted,
            sdkInt = 34,
            foregroundRequestedAtLeastOnce = true,
            foregroundRationaleAvailable = false,
        )
        assertEquals(
            PermissionStep.OpenAppSettings(SettingsReason.FOREGROUND_LOCATION_PERMANENTLY_DENIED),
            step,
        )
    }

    /** Denied once, with the rationale still available: ask again, do not give up. */
    @Test
    fun aSoftDenialAsksAgainRatherThanBouncingToSettings() {
        val step = LocationPermissionFlow.nextStep(
            grants = nothingGranted,
            sdkInt = 34,
            foregroundRequestedAtLeastOnce = true,
            foregroundRationaleAvailable = true,
        )
        assertTrue(step is PermissionStep.RequestRuntime)
    }

    /** Never asked: the rationale flag is meaningless, so it must not be read as a denial. */
    @Test
    fun aFirstRunIsNotMistakenForAPermanentDenial() {
        val step = LocationPermissionFlow.nextStep(
            grants = nothingGranted,
            sdkInt = 34,
            foregroundRequestedAtLeastOnce = false,
            foregroundRationaleAvailable = false,
        )
        assertTrue(step is PermissionStep.RequestRuntime)
    }

    // --- capability and precision --------------------------------------------------------------

    @Test
    fun capabilityReflectsWhatCollectionCanActuallyDo() {
        assertEquals(CollectionCapability.NONE, LocationPermissionFlow.capability(nothingGranted))
        assertEquals(CollectionCapability.FOREGROUND_ONLY, LocationPermissionFlow.capability(foregroundGranted))
        assertEquals(CollectionCapability.CONTINUOUS, LocationPermissionFlow.capability(allGranted))
    }

    /**
     * Background location without foreground location grants nothing usable. Android will not
     * deliver locations to an app with no foreground grant however "Allow all the time" is set, so
     * reporting anything but NONE here would be an optimistic lie the UI would repeat to the user.
     */
    @Test
    fun backgroundWithoutForegroundIsStillNoCapability() {
        assertEquals(
            CollectionCapability.NONE,
            LocationPermissionFlow.capability(LocationGrants(backgroundLocation = true)),
        )
    }

    @Test
    fun precisionDistinguishesPreciseFromApproximate() {
        assertEquals(LocationPrecision.NONE, LocationPermissionFlow.precision(nothingGranted))
        assertEquals(
            LocationPrecision.APPROXIMATE,
            LocationPermissionFlow.precision(LocationGrants(coarseLocation = true)),
        )
        assertEquals(
            LocationPrecision.PRECISE,
            LocationPermissionFlow.precision(LocationGrants(fineLocation = true, coarseLocation = true)),
        )
    }

    @Test
    fun onlyAnApproximateOnlyGrantOffersAPreciseUpgrade() {
        assertTrue(LocationPermissionFlow.canUpgradeToPrecise(LocationGrants(coarseLocation = true)))
        assertFalse(LocationPermissionFlow.canUpgradeToPrecise(foregroundGranted))
        assertFalse(LocationPermissionFlow.canUpgradeToPrecise(nothingGranted))
    }

    /**
     * **The precise upgrade must request fine AND coarse together.**
     *
     * From Android 12 (API 31) an app that requests `ACCESS_FINE_LOCATION` must request
     * `ACCESS_COARSE_LOCATION` in the same call; a fine-only request is **ignored by the system**.
     * This module targets API 34, so a fine-only upgrade would be a button that does nothing at all
     * — no dialog, no denial, no change — leaving the "only approximate location" warning on screen
     * permanently. That is the silent no-op the fail-safe stance forbids, and it is invisible to a
     * compiler, so it is pinned here.
     */
    @Test
    fun thePreciseUpgradeRequestsFineAndCoarseTogether() {
        val requested = LocationPermissionFlow.preciseUpgradePermissions()
        assertTrue("fine must be requested", fine in requested)
        assertTrue(
            "coarse must accompany fine — Android 12+ ignores a fine-only request",
            coarse in requested,
        )
    }

    /** The upgrade is a foreground concern; bundling background into it would break step two. */
    @Test
    fun thePreciseUpgradeNeverIncludesBackgroundLocation() {
        assertFalse(background in LocationPermissionFlow.preciseUpgradePermissions())
    }

    /**
     * The precise upgrade must never become a step in [LocationPermissionFlow.nextStep]. A user who
     * deliberately chose "Approximate" would then be trapped in a prompt that reappears forever.
     */
    @Test
    fun anApproximateGrantIsNotRePromptedAsABlockingStep() {
        val approximateAndBackground = LocationGrants(coarseLocation = true, backgroundLocation = true)
        assertEquals(PermissionStep.Ready, LocationPermissionFlow.nextStep(approximateAndBackground, 34))
    }

    // --- notification visibility ---------------------------------------------------------------

    @Test
    fun notificationsAreAlwaysVisibleBelowApi33() {
        assertTrue(LocationPermissionFlow.notificationVisible(LocationGrants(postNotifications = false), 32))
    }

    @Test
    fun fromApi33ADeniedNotificationPermissionHidesTheOngoingNotice() {
        assertFalse(LocationPermissionFlow.notificationVisible(LocationGrants(postNotifications = false), 33))
        assertTrue(LocationPermissionFlow.notificationVisible(LocationGrants(postNotifications = true), 33))
    }

    /**
     * A blocked notification must not block collection. The service still runs; the UI warns. The
     * opposite choice — refusing to collect — would be a worse failure for a safety product than a
     * missing status icon.
     */
    @Test
    fun blockedNotificationsDoNotStallThePermissionFlow() {
        val noNotifications = allGranted.copy(postNotifications = false)
        assertEquals(PermissionStep.Ready, LocationPermissionFlow.nextStep(noNotifications, 34))
        assertEquals(CollectionCapability.CONTINUOUS, LocationPermissionFlow.capability(noNotifications))
    }
}

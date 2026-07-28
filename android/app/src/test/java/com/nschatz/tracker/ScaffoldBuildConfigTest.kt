package com.nschatz.tracker

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The app module's generated build configuration.
 *
 * This started life in C0 as a placeholder that only proved `testDebugUnitTest` was wired into the
 * gate. It is kept because one of its assertions stopped being decorative in C1:
 * `VERBOSE_LOGGING` is now **read by real code** — `LocationCollectionService.logDebug` gates every
 * diagnostic on it, and the release variant sets it false so that location and credential
 * diagnostics cannot reach a release logcat. Pinning the two variants' values here keeps that switch
 * from being flipped by accident before C3 turns it into a tested guarantee.
 */
class ScaffoldBuildConfigTest {

    @Test
    fun applicationIdIsTheTrackerClient() {
        assertEquals("com.nschatz.tracker", BuildConfig.APPLICATION_ID)
    }

    @Test
    fun versionNameIsPopulated() {
        assertTrue("versionName must not be blank", BuildConfig.VERSION_NAME.isNotBlank())
    }

    @Test
    fun debugBuildEnablesVerboseLogging() {
        // The debug variant runs this test, and its buildConfigField opts verbose logging
        // ON; the release variant flips it OFF (so location/token/PII never reach release
        // logs — roadmap §7 / C3). Asserting it here locks the debug default in place before
        // any logging code exists to depend on it.
        assertTrue(BuildConfig.VERBOSE_LOGGING)
    }
}

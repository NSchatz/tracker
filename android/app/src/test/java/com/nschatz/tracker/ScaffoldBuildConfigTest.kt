package com.nschatz.tracker

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The C0 gate's JVM unit-test leg. Its job at scaffold stage is narrow and honest: prove
 * the `testDebugUnitTest` task is wired into `make check` and can see the app module's own
 * generated configuration. It asserts the scaffold's build metadata — nothing about
 * location, enrollment, or the server contract, all of which are later phases (C1+).
 *
 * Real unit tests arrive with the logic they cover; this one keeps the gate leg live in the
 * meantime, so a later phase inherits a working test harness rather than wiring one up.
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

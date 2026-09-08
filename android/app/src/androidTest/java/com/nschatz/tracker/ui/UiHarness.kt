package com.nschatz.tracker.ui

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.platform.app.InstrumentationRegistry
import com.google.android.apps.common.testing.accessibility.framework.AccessibilityCheckPreset
import com.google.android.apps.common.testing.accessibility.framework.AccessibilityCheckResult
import com.google.android.apps.common.testing.accessibility.framework.AccessibilityHierarchyCheckResult
import com.google.android.apps.common.testing.accessibility.framework.Parameters
import com.google.android.apps.common.testing.accessibility.framework.uielement.AccessibilityHierarchyAndroid
import com.google.android.apps.common.testing.accessibility.framework.utils.contrast.BitmapImage
import com.nschatz.tracker.collect.CollectionStatus
import java.io.ByteArrayOutputStream
import java.io.FileInputStream

/**
 * The emulator-side scaffolding for the Android half of the UI gate.
 *
 * Everything here reads the RUNNING app: the AccessibilityNodeInfo tree the platform built, the
 * pixels the emulator painted, the semantics Compose published. Nothing reads a resource table, a
 * preview or a Kotlin source file, because F2 of the umbrella's frontend conventions admits exactly
 * one Android grader for a claim about what a person sees and it is the emulator.
 */
object UiHarness {

    val context: Context get() = ApplicationProvider.getApplicationContext()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    /** Launches the screen, optionally broken in exactly one way (see [UiMutation]). */
    fun launch(mutation: UiMutation = UiMutation.NONE): ActivityScenario<MainActivity> {
        val intent = Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
            .putExtra(UiMutation.EXTRA, mutation.name)
        return ActivityScenario.launch(intent)
    }

    /** Runs a shell command through the instrumentation's UiAutomation and returns its output. */
    fun shell(command: String): String {
        val fd = instrumentation.uiAutomation.executeShellCommand(command)
        FileInputStream(fd.fileDescriptor).use { input ->
            val out = ByteArrayOutputStream()
            val buf = ByteArray(4096)
            while (true) {
                val n = input.read(buf)
                if (n <= 0) break
                out.write(buf, 0, n)
            }
            return out.toString("UTF-8")
        }
    }

    /**
     * Puts the whole device into light or dark mode.
     *
     * The system preference, set on the device, is what F10 is about: a page that follows a flag the
     * test set on itself would prove only that the flag works.
     */
    fun setNightMode(dark: Boolean) {
        shell("cmd uimode night " + if (dark) "yes" else "no")
        Thread.sleep(1_500)
    }

    /** Forces a 360dp-wide phone profile, whatever the AVD's own metrics are. */
    fun set360dpProfile() {
        // 1080 physical pixels at 480dpi is exactly 360dp of width - the narrow phone AC12 names.
        shell("wm size 1080x2340")
        shell("wm density 480")
        Thread.sleep(1_500)
    }

    fun resetDisplayProfile() {
        shell("wm size reset")
        shell("wm density reset")
        Thread.sleep(1_500)
    }

    fun takeScreenshot(): Bitmap = instrumentation.uiAutomation.takeScreenshot()

    fun activeWindowRoot(): AccessibilityNodeInfo? = instrumentation.uiAutomation.rootInActiveWindow

    /**
     * Runs Google's Accessibility Test Framework over the tree the platform actually built, with the
     * emulator's own screenshot supplied so the contrast checks have real pixels to measure.
     *
     * Returns every ERROR. It deliberately does NOT filter by check class: an accessibility harness
     * that only reports the failures it was written to expect is a harness that goes quiet the first
     * time a new kind of defect is introduced.
     */
    fun accessibilityErrors(): AtfRun {
        val root = activeWindowRoot() ?: return AtfRun(emptyList(), 0, "no active window")
        val screenshot = takeScreenshot()
        val hierarchy = AccessibilityHierarchyAndroid.newBuilder(root, context).build()
        val parameters = Parameters()
        parameters.putScreenCapture(BitmapImage(screenshot))

        var ran = 0
        val errors = mutableListOf<AccessibilityHierarchyCheckResult>()
        for (check in AccessibilityCheckPreset.getAccessibilityHierarchyChecksForPreset(AccessibilityCheckPreset.LATEST)) {
            val results = check.runCheckOnHierarchy(hierarchy, null, parameters)
            ran += results.size
            for (r in results) {
                if (r.type == AccessibilityCheckResult.AccessibilityCheckResultType.ERROR) {
                    errors.add(r)
                }
            }
        }
        return AtfRun(errors, ran, "")
    }

    /** Puts the process-scoped readout back to a freshly-started state. */
    fun resetStatus() {
        CollectionStatus.clearForTest()
    }
}

/**
 * One accessibility sweep.
 *
 * [resultCount] is what stops a green sweep from being vacuous: a hierarchy that produced no results
 * at all means the checks ran over nothing, which is a failure of the harness rather than a clean
 * bill of health for the screen.
 */
data class AtfRun(
    val errors: List<AccessibilityHierarchyCheckResult>,
    val resultCount: Int,
    val note: String,
) {
    fun describe(): String {
        if (note.isNotEmpty()) return note
        return errors.joinToString("\n  ") { r ->
            val where = r.element?.let { "${it.className} ${it.boundsInScreen}" } ?: "(no element)"
            "${r.sourceCheckClass.simpleName} on $where: ${r.getMessage(java.util.Locale.ENGLISH)}"
        }
    }
}

package com.nschatz.tracker.ui

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Rect
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.platform.app.InstrumentationRegistry
import com.google.android.apps.common.testing.accessibility.framework.AccessibilityCheckPreset
import com.google.android.apps.common.testing.accessibility.framework.AccessibilityCheckResult
import com.google.android.apps.common.testing.accessibility.framework.Parameters
import com.google.android.apps.common.testing.accessibility.framework.uielement.AccessibilityHierarchyAndroid
import com.google.android.apps.common.testing.accessibility.framework.utils.contrast.BitmapImage
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.CollectionStatus
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileInputStream
import java.util.Locale
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow

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

    /** Pixels per dp on the device as it is configured RIGHT NOW, read off the running display. */
    fun density(): Float = context.resources.displayMetrics.density

    fun dpOf(px: Int): Float = px / density()

    /**
     * The screenshot the emulator actually painted, or a refusal naming what could not be obtained.
     *
     * AC27: a route that cannot obtain a rendering must say which rendering it could not obtain and
     * report nothing as passed. `takeScreenshot` returning null is exactly that case, and it is the
     * one that would otherwise surface as a NullPointerException three frames later.
     */
    fun screenshotOrFail(): Bitmap = instrumentation.uiAutomation.takeScreenshot()
        ?: throw AssertionError(
            "the emulator returned no screenshot, so no rendered claim can be graded from this run; " +
                "no clause is reported as passed, skipped or green",
        )

    /**
     * The root of the accessibility tree, or a refusal naming what could not be obtained.
     *
     * A null root means the activity under test is not the active window - it did not start, it
     * crashed, or a system dialog is in front of it - and every sweep below would then be measuring
     * something that is not this screen.
     */
    fun activeWindowRootOrFail(): AccessibilityNodeInfo = instrumentation.uiAutomation.rootInActiveWindow
        ?: throw AssertionError(
            "no active window: the activity under test did not start, or something else is in front " +
                "of it, so there is no rendering to grade; no clause is reported as passed, skipped or green",
        )

    fun activeWindowRoot(): AccessibilityNodeInfo? = instrumentation.uiAutomation.rootInActiveWindow

    fun takeScreenshot(): Bitmap = screenshotOrFail()

    // --- the accessibility sweep -----------------------------------------------------------------

    /**
     * One sweep of the screen as it is right now: every node the platform published, each measured
     * for the three things AC13 names, plus every result Google's Accessibility Test Framework
     * produced over the same tree.
     *
     * ### Why the sweep measures as well as delegating
     *
     * Impl-gate finding F21: the previous sweep asked ATF for ERROR results and reported a clean bill
     * of health for a screen carrying a 20dp unlabelled control and sub-floor text. It could not be
     * shown going red, so nothing it ever reported green was evidence. Three separable causes were
     * found, and all three are answered here rather than papered over:
     *
     *  1. **Its anti-vacuity guard counted results that had not run.** `runCheckOnHierarchy` returns a
     *     result per element per check, and the ones it declined to evaluate come back as `NOT_RUN`.
     *     Counting those made `resultCount > 0` true on any tree at all, so the guard could never
     *     fire. [AtfRun.evaluated] now counts only results that actually ran, and the guard that
     *     matters is the per-claim demonstration rather than a result count.
     *  2. **ATF cannot report at ERROR what it had to guess.** A hierarchy built from an
     *     `AccessibilityNodeInfo` tree - the only kind a Compose surface has - carries no text colour
     *     and no background colour, so the contrast checks fall back to a screenshot heuristic and
     *     report at WARNING or decline as NOT_RUN. Filtering to ERROR threw every contrast finding
     *     away. So each claim now carries its OWN measuring code over the same two inputs the
     *     platform checks use (that node tree and that screenshot), and ATF's ERRORs are added to it
     *     rather than being all of it.
     *  3. **The mutation did not always break the claim it named.** Material's `Button` applies
     *     `minimumInteractiveComponentSize()`, so a `Modifier.size(20.dp)` Button still has a 48dp
     *     touch target and there was no target-size defect on the glass to find; and one fixed light
     *     grey measured 1.7:1 on a white card but nearly 9:1 on a dark one, so the dark
     *     demonstration was mutating nothing. Both are fixed in [UiMutation].
     */
    fun sweep(measureContrast: Boolean): AtfRun {
        val root = activeWindowRootOrFail()
        val screenshot = screenshotOrFail()
        val nodes = mutableListOf<A11yNode>()
        collectNodes(root, if (measureContrast) screenshot else null, nodes)

        val hierarchy = AccessibilityHierarchyAndroid.newBuilder(root, context).build()
        val parameters = Parameters()
        parameters.putScreenCapture(BitmapImage(screenshot))

        var evaluated = 0
        var notRun = 0
        val findings = mutableListOf<AtfFinding>()
        for (check in AccessibilityCheckPreset.getAccessibilityHierarchyChecksForPreset(AccessibilityCheckPreset.LATEST)) {
            val results = try {
                check.runCheckOnHierarchy(hierarchy, null, parameters)
            } catch (failure: RuntimeException) {
                findings.add(
                    AtfFinding(
                        check = check.javaClass.simpleName,
                        type = "THREW",
                        where = "(the whole hierarchy)",
                        message = failure.toString(),
                    ),
                )
                continue
            }
            for (r in results) {
                if (r.type == AccessibilityCheckResult.AccessibilityCheckResultType.NOT_RUN) {
                    notRun++
                    continue
                }
                evaluated++
                val element = r.element
                val where = element?.let { "${it.className} ${it.boundsInScreen}" } ?: "(no element)"
                findings.add(
                    AtfFinding(
                        check = check.javaClass.simpleName,
                        type = r.type.name,
                        where = where,
                        message = r.getMessage(Locale.ENGLISH).toString(),
                    ),
                )
            }
        }
        return AtfRun(nodes = nodes, findings = findings, evaluated = evaluated, notRun = notRun)
    }

    private fun collectNodes(node: AccessibilityNodeInfo?, shot: Bitmap?, out: MutableList<A11yNode>) {
        if (node == null) return
        val bounds = Rect()
        node.getBoundsInScreen(bounds)
        val text = node.text?.toString().orEmpty()
        val description = node.contentDescription?.toString().orEmpty()
        val hint = try {
            node.hintText?.toString().orEmpty()
        } catch (unsupported: NoSuchMethodError) {
            ""
        }
        out.add(
            A11yNode(
                id = node.viewIdResourceName ?: "",
                className = node.className?.toString() ?: "",
                text = text,
                description = description,
                hint = hint,
                bounds = Rect(bounds),
                clickable = node.isClickable,
                focusable = node.isFocusable,
                editable = node.isEditable,
                visible = node.isVisibleToUser,
                childCount = node.childCount,
                contrast = if (shot != null && text.isNotBlank()) contrastOf(shot, bounds) else null,
            ),
        )
        for (i in 0 until node.childCount) collectNodes(node.getChild(i), shot, out)
    }

    /**
     * The contrast ratio between the two colours a crop of the screen is actually made of, or null
     * when the crop is not a confident text-on-background sample.
     *
     * The same shape as the heuristic the platform's own contrast check uses - a histogram over the
     * element's pixels, the most common colour taken as the background, the colour furthest from it
     * in luminance taken as the foreground - and it runs here rather than being delegated because
     * that check can only report a heuristic finding as a WARNING, and a sweep that read ERRORs was
     * throwing every one of them away (finding F21).
     *
     * It returns null rather than a number whenever it is not confident: a crop with no dominant
     * background, or with no second colour occurring often enough to be a glyph rather than an
     * anti-aliasing fringe, is not measured at all. A refusal to measure is visible in the evidence
     * this run writes out; a guessed ratio would not be.
     */
    fun contrastOf(shot: Bitmap, bounds: Rect): Double? {
        val r = Rect(bounds)
        if (!r.intersect(Rect(0, 0, shot.width, shot.height))) return null
        if (r.width() < 8 || r.height() < 8) return null
        val area = r.width() * r.height()
        if (area < 200) return null

        val pixels = IntArray(r.width() * r.height())
        shot.getPixels(pixels, 0, r.width(), r.left, r.top, r.width(), r.height())
        val step = max(1, kotlin.math.sqrt(area / 20_000.0).toInt())
        val counts = HashMap<Int, Int>()
        var total = 0
        var row = 0
        while (row < r.height()) {
            var col = 0
            while (col < r.width()) {
                val c = pixels[row * r.width() + col] or (0xFF shl 24)
                counts[c] = (counts[c] ?: 0) + 1
                total++
                col += step
            }
            row += step
        }
        if (total < 64) return null

        var background = 0
        var backgroundCount = 0
        for ((colour, n) in counts) {
            if (n > backgroundCount) {
                background = colour
                backgroundCount = n
            }
        }
        // Not a text-on-background sample: no colour dominates, so whatever this crop shows, a
        // foreground/background split of it would be a guess.
        if (backgroundCount * 5 < total * 2) return null

        val floor = max(6, total / 1000)
        val backgroundLuminance = luminance(background)
        var foreground: Int? = null
        var furthest = 0.0
        for ((colour, n) in counts) {
            if (colour == background || n < floor) continue
            val distance = abs(luminance(colour) - backgroundLuminance)
            if (distance > furthest) {
                furthest = distance
                foreground = colour
            }
        }
        val fg = foreground ?: return null
        return contrastRatio(luminance(fg), backgroundLuminance)
    }

    private fun luminance(colour: Int): Double {
        fun channel(v: Int): Double {
            val s = v / 255.0
            return if (s <= 0.03928) s / 12.92 else ((s + 0.055) / 1.055).pow(2.4)
        }
        val r = channel((colour shr 16) and 0xFF)
        val g = channel((colour shr 8) and 0xFF)
        val b = channel(colour and 0xFF)
        return 0.2126 * r + 0.7152 * g + 0.0722 * b
    }

    private fun contrastRatio(a: Double, b: Double): Double =
        (max(a, b) + 0.05) / (min(a, b) + 0.05)

    // --- evidence --------------------------------------------------------------------------------

    /**
     * Writes one line of grading evidence where `make verify-ui-android` can read it back off the
     * device.
     *
     * AC13 asks a failing run to name the view, the check and the measured value; this is where the
     * measured values of a PASSING run go, so a green sweep is inspectable rather than merely quiet.
     * It lands in the app's cache directory rather than its files directory because the durable fix
     * queue owns the latter and counts what it finds there.
     */
    fun evidence(line: String) {
        try {
            val file = File(context.cacheDir, EVIDENCE_FILE)
            file.appendText(line + "\n")
        } catch (ignored: java.io.IOException) {
            // Evidence is an aid, never a gate: a route that turned red because it could not write a
            // log would be reporting on the log rather than on the screen.
        }
    }

    const val EVIDENCE_FILE = "ui-grading.log"

    // --- focus, keyboard and configuration -------------------------------------------------------

    /** Puts the process-scoped readout back to a freshly-started state. */
    fun resetStatus() {
        CollectionStatus.clearForTest()
    }

    /**
     * The node that currently holds input focus, as the PLATFORM reports it.
     *
     * This is the ground truth for "operable by directional navigation": it is the same focus an
     * assistive technology reads, and it does not depend on which Compose semantics node happens to
     * carry the Focused property relative to the one carrying the testTag.
     */
    fun focusedNode(): AccessibilityNodeInfo? = activeWindowRoot()?.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)

    /** How the focused control would be named in a failure message: its test tag, else its label. */
    fun focusedName(): String {
        val focused = focusedNode() ?: return "(nothing)"
        val id = focused.viewIdResourceName
        if (!id.isNullOrBlank()) return id
        val text = focused.text?.toString() ?: focused.contentDescription?.toString()
        if (!text.isNullOrBlank()) return text
        return focused.className?.toString() ?: "(unnamed)"
    }

    fun focusedLabel(): String? {
        val focused = focusedNode() ?: return null
        return (focused.text ?: focused.contentDescription)?.toString()
    }

    fun focusedIsEditable(): Boolean = focusedNode()?.isEditable == true

    /**
     * Closes the soft keyboard if one is up.
     *
     * Entering text into a Compose field raises the IME, and while it is up the IME is what receives
     * a d-pad key - so a directional traversal issued straight after typing goes to the keyboard and
     * never reaches the screen. ESCAPE rather than BACK on purpose: BACK with no IME showing would
     * finish the Activity, which would turn one weak assertion into a confusing crash.
     */
    fun dismissKeyboard() {
        shell("input keyevent 111") // KEYCODE_ESCAPE
        Thread.sleep(500)
    }

    /**
     * Takes every input method out of service for the duration of a case.
     *
     * A directional traversal is a claim about the SCREEN, and an on-screen keyboard that appears
     * when focus lands in a text field puts a second window between the keys and the screen. With no
     * input method enabled the emulator raises none, so what the traversal measures is the screen's
     * own focus order. Text still goes in: the suite types through Compose's semantics text action,
     * which is not the IME. [restoreInputMethods] puts the device back.
     */
    fun suppressSoftKeyboard() {
        for (id in shell("ime list -s -a").lines()) {
            val trimmed = id.trim()
            if (trimmed.isNotEmpty()) shell("ime disable $trimmed")
        }
        Thread.sleep(500)
    }

    fun restoreInputMethods() {
        shell("ime reset")
        Thread.sleep(500)
    }

    /**
     * Clears the stored server configuration.
     *
     * A refused save is one of the states the brevity floor has to bind on, and it is only reachable
     * while the stored configuration is unusable. `SharedPreferences` outlive a case, so without
     * this the state a case reaches would depend on which case happened to run before it.
     */
    fun clearConfig() {
        val prefs = ClientPreferences(context)
        prefs.baseUrl = ""
        prefs.deviceToken = ""
    }
}

/** One node of the tree the platform published, with what this run measured about it. */
data class A11yNode(
    val id: String,
    val className: String,
    val text: String,
    val description: String,
    val hint: String,
    val bounds: Rect,
    val clickable: Boolean,
    val focusable: Boolean,
    val editable: Boolean,
    val visible: Boolean,
    val childCount: Int,
    val contrast: Double?,
) {
    /** How this node is named in a failure message: its test tag first, because that is findable. */
    fun name(): String {
        if (id.isNotBlank()) return id
        if (text.isNotBlank()) return "\"" + text.take(40) + "\""
        if (description.isNotBlank()) return "\"" + description.take(40) + "\""
        return className.substringAfterLast('.') + " " + bounds.toShortString()
    }

    /** What a screen reader would have to announce for this node. */
    fun spokenName(): String = listOf(text, description, hint).firstOrNull { it.isNotBlank() }.orEmpty()

    fun isControl(): Boolean = visible && (clickable || editable) && bounds.width() > 0 && bounds.height() > 0

    fun isRenderedText(): Boolean = visible && text.isNotBlank() && bounds.width() > 0 && bounds.height() > 0
}

/** One result the Accessibility Test Framework produced, at whatever severity it produced it. */
data class AtfFinding(
    val check: String,
    val type: String,
    val where: String,
    val message: String,
) {
    fun describe(): String = "$check [$type] on $where: $message"
}

/**
 * One accessibility sweep: what the platform published, what this run measured, and what the
 * platform's own checks said about the same tree.
 *
 * [evaluated] counts only the results that actually RAN. The sweep this replaces counted `NOT_RUN`
 * results too, which is why its anti-vacuity guard was true on every tree and could never fire -
 * half of impl-gate finding F21.
 */
data class AtfRun(
    val nodes: List<A11yNode>,
    val findings: List<AtfFinding>,
    val evaluated: Int,
    val notRun: Int,
) {
    fun errorsFrom(checks: Set<String>): List<AtfFinding> =
        findings.filter { it.type == "ERROR" && checks.contains(it.check) }

    fun describeFindings(checks: Set<String>): String {
        val mine = findings.filter { checks.contains(it.check) }
        if (mine.isEmpty()) return "(the platform checks produced no result of any severity here)"
        return mine.joinToString("\n    ") { it.describe() }
    }
}

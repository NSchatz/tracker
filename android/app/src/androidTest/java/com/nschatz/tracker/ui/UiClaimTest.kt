package com.nschatz.tracker.ui

import android.view.KeyEvent
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.nschatz.tracker.R
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.CollectionStatus
import com.nschatz.tracker.collect.TroubleKind
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The Android half of the UI gate: every rendered claim about the client's one screen, graded on a
 * booted emulator, and every one of them ALSO shown going red.
 *
 * ### Why each claim appears twice
 *
 * AC18 of this item's spec: "a demonstration being the same measuring code shown going red against a
 * surface mutated to break exactly one claim - so that no assertion here can pass vacuously". An
 * accessibility assertion whose selector stopped matching would otherwise loop over nothing and pass
 * forever. So each `X` has an `X_demonstration` that launches the same screen with one thing broken
 * (a debug-only [UiMutation], inert in a release build) and asserts the assertion FAILS. The record
 * check counts both, and `make verify-ui-record` refuses a record naming an assertion that did not
 * run.
 *
 * ### Why this is not a JVM test
 *
 * F2 of the umbrella's frontend conventions: "an emulator, not the JVM, for Android". Robolectric,
 * Compose previews and screenshot diffs do not satisfy it, and the reason is the same one that makes
 * the PostGIS tests refuse to run against a mock: the thing under test here is what the PLATFORM
 * draws and what the PLATFORM's accessibility service reports about it, and a fake would only ever
 * assert what we already believed.
 *
 * ### Nothing here is @Ignore'd, and that is checked rather than trusted
 *
 * The AC13 and AC14 cases were parked when S0056 was narrowed; they are graded here again, and
 * FRONTEND-CONVENTIONS-RECORD.md names them rather than deferring them. `uiverify record` refuses a
 * deferral on any clause/surface pair at all now, and compares the record's (empty) deferred set
 * against this file's `@Ignore`d set in both directions - so an `@Ignore` added here turns
 * `make verify-ui-record` red until the record says so out loud, and no record cell can be written
 * that would make one legal.
 *
 * ### One mutation per claim
 *
 * AC13 makes four claims - contrast, touch target size, a non-empty spoken name, and no state
 * carried by colour alone - and each has a mutation that breaks THAT claim and no other, run in both
 * themes. Impl-gate finding F21 was one mutation breaking two claims at once: the sweep reported
 * nothing, and nothing in the result could say which of the two checks was blind. Each demonstration
 * below therefore reads the failure MESSAGE and requires it to name the mutated view and this
 * claim's own check, which is what makes it a guard rather than a formality.
 */
@RunWith(AndroidJUnit4::class)
class UiClaimTest {

    @get:Rule
    val compose = createEmptyComposeRule()

    /**
     * What the last measuring function actually saw, for the demonstration to quote.
     *
     * A demonstration that reports only "the check stayed green" says nothing about WHY, and the
     * grading evidence it would otherwise be read from is pulled off the device after the run - which
     * is no help when the device wrote none. This travels with the failure itself.
     */
    private var lastMeasurement: String = ""

    @Before
    fun setUp() {
        UiHarness.resetStatus()
        UiHarness.clearConfig()
        // Every case is graded in the one grant state that draws all nine of this screen's controls.
        // Without it two of them are not operable at all - the start/stop control is disabled and the
        // precise-location upgrade is not drawn - and AC14's "every control" could only ever be
        // measured over seven. See UiHarness.grantApproximateLocation.
        UiHarness.grantApproximateLocation()
        UiHarness.setNightMode(false)
    }

    @After
    fun tearDown() {
        UiHarness.resetStatus()
        UiHarness.clearConfig()
        UiHarness.resetDisplayProfile()
        UiHarness.setNightMode(false)
        // The AC14 cases take every input method out of service so that no on-screen keyboard sits
        // between the directional keys and the screen. Put the device back whichever case just ran,
        // so a later one never inherits a half-configured device.
        UiHarness.restoreInputMethods()
    }

    // --- AC12: brevity, the explanation destination, and a 360dp layout -------------------------

    @Test
    fun AC12_labels_stay_short() {
        UiHarness.launch()
        labelsStayShort()
        everyStateStaysShort()
    }

    @Test
    fun AC12_labels_stay_short_demonstration() {
        UiHarness.launch(UiMutation.PARAGRAPHS_ON_SURFACE)
        assertFails("a paragraph on the surface was not caught by the brevity assertion") { labelsStayShort() }

        // And the half a launch-state measurement cannot reach: a paragraph drawn only once the
        // screen has been driven into a degraded state.
        UiHarness.launch(UiMutation.PROSE_IN_A_DEGRADED_STATE)
        assertFails("a paragraph drawn only in a degraded state was not caught") { everyStateStaysShort() }
    }

    /**
     * The floor, applied to every state the screen can be in rather than only to the one it opens
     * in.
     *
     * The two texts the home tree draws from the domain layer - the refused-save verdict and the
     * collection warning - are not on the screen at launch, so a floor that measured only the launch
     * state could never see them however long they grew. This drives the screen INTO each of those
     * states and measures there, and it iterates the trouble vocabulary rather than a hand-written
     * list so that a kind added later is measured without anyone remembering to add it.
     */
    private fun everyStateStaysShort() {
        // A save the client refuses, reached the way a person reaches it: press Save with nothing
        // entered. setUp cleared the stored configuration, so this is the refusal every time.
        compose.onNodeWithTag("action-save").performScrollTo().performClick()
        compose.waitForIdle()
        val verdict = textOf("config-verdict")
        assertTrue(
            "pressing Save with no configuration rendered \"$verdict\"; the refused-save state was " +
                "never entered, so measuring it would prove nothing",
            verdict.contains("Not saved", ignoreCase = true),
        )
        labelsStayShort()

        for (kind in TroubleKind.entries) {
            InstrumentationRegistry.getInstrumentation().runOnMainSync {
                CollectionStatus.recordBlocked(kind, A_SENTENCE_THE_DOMAIN_PRODUCES)
            }
            compose.waitForIdle()
            assertTrue(
                "the collection card renders nothing for $kind, so the floor would be measuring a " +
                    "state that is not on the screen",
                textOf("collection-error").isNotBlank(),
            )
            labelsStayShort()
        }
    }

    private fun labelsStayShort() {
        val texts = renderedTexts()
        assertTrue("no rendered text was found at all, so this assertion would pass vacuously", texts.size >= 8)
        val wordy = texts.filter { wordCount(it) > MAX_LABEL_WORDS }
        assertTrue(
            "these are paragraphs, not labels (the floor is $MAX_LABEL_WORDS words; the explanation " +
                "belongs behind the card's affordance):\n  " + wordy.joinToString("\n  "),
            wordy.isEmpty(),
        )
    }

    @Test
    fun AC12_each_card_opens_its_explanation() {
        UiHarness.launch()
        eachCardOpensItsExplanation()
    }

    @Test
    fun AC12_each_card_opens_its_explanation_demonstration() {
        UiHarness.launch(UiMutation.NO_EXPLANATION_AFFORDANCE)
        assertFails("a card with no explanation affordance was not caught") { eachCardOpensItsExplanation() }
    }

    private fun eachCardOpensItsExplanation() {
        for (tag in listOf("explain-permissions", "explain-server", "explain-counters")) {
            assertEquals(
                "the card behind $tag must carry exactly one explanation affordance",
                1,
                compose.onAllNodesWithTag(tag).fetchSemanticsNodes().size,
            )
            // Scrolled to first: the home screen is a scrolling column and is TALLER than a phone
            // viewport, so the third card's affordance is simply not on screen when the screen
            // opens. Clicking a node that is not in the viewport injects a tap that lands nowhere,
            // which reads as "the affordance does not work" when what happened is that a person
            // would have scrolled to it.
            compose.onNodeWithTag(tag).performScrollTo().performClick()
            compose.waitForIdle()
            compose.onNodeWithTag("explanation").assertIsDisplayed()

            // The destination has to RENDER the paragraphs, not merely exist.
            val paragraphs = renderedTexts().filter { wordCount(it) > MAX_LABEL_WORDS }
            assertTrue(
                "the explanation opened from $tag renders no paragraph, so the claims were dropped rather than moved",
                paragraphs.isNotEmpty(),
            )
            compose.onNodeWithTag("explanation-back").performScrollTo().performClick()
            compose.waitForIdle()
            compose.onNodeWithTag("home").assertIsDisplayed()
        }
    }

    @Test
    fun AC12_nothing_clipped_at_360dp() {
        UiHarness.set360dpProfile()
        UiHarness.launch()
        nothingClippedAt360dp()
    }

    @Test
    fun AC12_nothing_clipped_at_360dp_demonstration() {
        UiHarness.set360dpProfile()
        UiHarness.launch(UiMutation.OVERFLOWING_LAYOUT)
        assertFails("a layout wider than the display was not caught") { nothingClippedAt360dp() }
    }

    private fun nothingClippedAt360dp() {
        compose.waitForIdle()
        val root = compose.onNodeWithTag("home").fetchSemanticsNode()
        val rootWidth = root.size.width
        assertTrue("the home screen laid out to zero width", rootWidth > 0)

        val offenders = mutableListOf<String>()
        var measured = 0
        for (node in compose.onAllNodesWithTag("home", useUnmergedTree = true).fetchSemanticsNodes()) {
            // Walk everything under the home column.
            walk(node) { child ->
                val text = child.config.getOrNull(SemanticsProperties.Text)?.joinToString(" ") { it.text }
                if (!text.isNullOrBlank()) {
                    // AC12 is about HORIZONTAL fit: "no text clipped or pushed outside the display
                    // and no horizontal scrolling". The column scrolls VERTICALLY by design, so a
                    // node below the fold has an empty boundsInRoot - it is out of the viewport, not
                    // cut off, and a person reaches it by scrolling. Measuring it would report every
                    // screen taller than the glass as clipped, which is what the first emulator run
                    // did. Only what is currently in view can be judged for clipping.
                    if (child.boundsInRoot.height > 0f && child.boundsInRoot.width > 0f) {
                        measured++
                        // boundsInRoot is CLIPPED by every ancestor; size is what layout gave the
                        // node. A visible box narrower than the laid-out box has had its text cut
                        // off, and a box running past the display has been pushed outside it.
                        if (child.boundsInRoot.width + 1f < child.size.width) {
                            offenders.add("clipped: \"$text\" (laid out ${child.size.width}px, visible ${child.boundsInRoot.width}px)")
                        }
                    }
                    // Horizontal overflow is judged on the LAID-OUT box whether or not the node is
                    // scrolled into view: a column wider than the display is wrong at every offset,
                    // and this is the half the OVERFLOWING_LAYOUT mutation trips.
                    if (child.positionInRoot.x + child.size.width > rootWidth + 1f) {
                        offenders.add("pushed outside: \"$text\" ends at ${child.positionInRoot.x + child.size.width}px of ${rootWidth}px")
                    }
                }
            }
        }
        assertTrue(
            "no text was in the viewport to measure, so this assertion would pass vacuously",
            measured >= 5,
        )
        assertTrue(
            "at a 360dp-wide profile the screen does not fit:\n  " + offenders.joinToString("\n  "),
            offenders.isEmpty(),
        )
    }

    // --- AC13: the platform accessibility checks, one claim at a time ---------------------------
    //
    // Four claims, each graded in BOTH themes, each with its own demonstration against a screen
    // mutated to break THAT claim and no other. Sixteen cases where S0056 had six, and the reason is
    // impl-gate finding F21: one mutation broke two claims at once, the sweep reported nothing, and
    // there was no way to tell which of the two checks was blind. A demonstration that cannot say
    // which claim stayed green is not a guard, it is a formality.

    @Test
    fun AC13_contrast_light() {
        UiHarness.setNightMode(false)
        UiHarness.launch()
        contrastMeetsTheFloor("light")
    }

    @Test
    fun AC13_contrast_light_demonstration() {
        UiHarness.setNightMode(false)
        UiHarness.launch(UiMutation.CONTRAST_BELOW_FLOOR)
        assertFailsNaming("collection-running", "contrast", "light") { contrastMeetsTheFloor("light") }
    }

    @Test
    fun AC13_contrast_dark() {
        UiHarness.setNightMode(true)
        UiHarness.launch()
        contrastMeetsTheFloor("dark")
    }

    @Test
    fun AC13_contrast_dark_demonstration() {
        UiHarness.setNightMode(true)
        UiHarness.launch(UiMutation.CONTRAST_BELOW_FLOOR)
        assertFailsNaming("collection-running", "contrast", "dark") { contrastMeetsTheFloor("dark") }
    }

    @Test
    fun AC13_target_size_light() {
        UiHarness.setNightMode(false)
        UiHarness.launch()
        everyTargetMeetsTheFloor("light")
    }

    @Test
    fun AC13_target_size_light_demonstration() {
        UiHarness.setNightMode(false)
        UiHarness.launch(UiMutation.TARGET_BELOW_FLOOR)
        assertFailsNaming("action-collection", "touch target size", "light") { everyTargetMeetsTheFloor("light") }
    }

    @Test
    fun AC13_target_size_dark() {
        UiHarness.setNightMode(true)
        UiHarness.launch()
        everyTargetMeetsTheFloor("dark")
    }

    @Test
    fun AC13_target_size_dark_demonstration() {
        UiHarness.setNightMode(true)
        UiHarness.launch(UiMutation.TARGET_BELOW_FLOOR)
        assertFailsNaming("action-collection", "touch target size", "dark") { everyTargetMeetsTheFloor("dark") }
    }

    @Test
    fun AC13_spoken_name_light() {
        UiHarness.setNightMode(false)
        UiHarness.launch()
        everyControlHasASpokenName("light")
    }

    @Test
    fun AC13_spoken_name_light_demonstration() {
        UiHarness.setNightMode(false)
        UiHarness.launch(UiMutation.CONTROL_WITHOUT_A_NAME)
        assertFailsNaming("action-collection", "spoken name", "light") { everyControlHasASpokenName("light") }
    }

    @Test
    fun AC13_spoken_name_dark() {
        UiHarness.setNightMode(true)
        UiHarness.launch()
        everyControlHasASpokenName("dark")
    }

    @Test
    fun AC13_spoken_name_dark_demonstration() {
        UiHarness.setNightMode(true)
        UiHarness.launch(UiMutation.CONTROL_WITHOUT_A_NAME)
        assertFailsNaming("action-collection", "spoken name", "dark") { everyControlHasASpokenName("dark") }
    }

    @Test
    fun AC13_no_state_by_colour_alone_light() {
        UiHarness.setNightMode(false)
        aDegradedStateIsOnTheScreen()
        UiHarness.launch()
        noStateByColourAlone("light")
    }

    @Test
    fun AC13_no_state_by_colour_alone_light_demonstration() {
        UiHarness.setNightMode(false)
        aDegradedStateIsOnTheScreen()
        UiHarness.launch(UiMutation.WARNING_BY_COLOUR_ONLY)
        assertFailsNaming("collection-error", "state carried by colour alone", "light") { noStateByColourAlone("light") }
    }

    @Test
    fun AC13_no_state_by_colour_alone_dark() {
        UiHarness.setNightMode(true)
        aDegradedStateIsOnTheScreen()
        UiHarness.launch()
        noStateByColourAlone("dark")
    }

    @Test
    fun AC13_no_state_by_colour_alone_dark_demonstration() {
        UiHarness.setNightMode(true)
        aDegradedStateIsOnTheScreen()
        UiHarness.launch(UiMutation.WARNING_BY_COLOUR_ONLY)
        assertFailsNaming("collection-error", "state carried by colour alone", "dark") { noStateByColourAlone("dark") }
    }

    private fun aDegradedStateIsOnTheScreen() {
        CollectionStatus.recordBlocked(
            TroubleKind.PERMISSION_LOST,
            "Location permission was revoked, so collection stopped.",
        )
    }

    // --- the four measuring functions ------------------------------------------------------------

    /**
     * Every rendered text measures at or above the WCAG 2.2 AA floor against the background it is
     * actually painted on.
     *
     * The ratio is computed here, from the emulator's own screenshot and the platform's own node
     * tree, because the platform check that would otherwise answer this cannot report at ERROR what
     * it had to infer from a screenshot - and a hierarchy built from an `AccessibilityNodeInfo` tree,
     * which is the only kind a Compose surface has, gives it nothing else to go on. Filtering that
     * check's results to ERROR is how finding F21's sweep came back clean over sub-floor text. Its
     * ERRORs are still failures here; they are no longer the only way this claim can fail.
     */
    private fun contrastMeetsTheFloor(theme: String) {
        val run = sweepEveryCard(theme, "contrast", measureContrast = true)
        // WCAG 2.2's 1.4.3 exempts "text or images of text that are part of an inactive user
        // interface component". Material paints a disabled control at 38% of the content colour by
        // design, so measuring one would report the platform's own disabled styling as a defect of
        // this screen. The start/stop control IS disabled on an emulator, where no location grant
        // exists, and it was the only thing the first run reported.
        val texts = run.nodes.filter { it.isRenderedText() && !isInactive(it, run.nodes) }
        val measured = texts.filter { it.contrast != null }.distinctBy { it.name() + it.bounds.toShortString() }
        lastMeasurement = "${measured.size} texts, ratios " +
            measured.joinToString(", ") { "${it.name()}=${ratio(it.contrast!!)}" }
        assertTrue(
            "contrast: no rendered text in the $theme theme could be measured at all " +
                "(${texts.size} text nodes seen, none with a confident foreground and background), " +
                "so a clean sweep would prove nothing",
            measured.size >= MINIMUM_TEXTS_MEASURED,
        )
        val offenders = measured.filter { it.contrast!! < CONTRAST_FLOOR }
            .map {
                "contrast: ${it.name()} measures ${ratio(it.contrast!!)} against its background in " +
                    "the $theme theme, and the floor is ${ratio(CONTRAST_FLOOR)}"
            }
        val platform = run.errorsFrom(CONTRAST_CHECKS).map { "contrast: " + it.describe() }
        // The message carries the offenders and NOTHING else that could name a view: a demonstration
        // reads it to decide whether THIS claim went red, and a message that listed every view it
        // inspected would answer yes whatever failed.
        assertTrue(
            (offenders + platform).joinToString("\n  ") +
                "\n  [${measured.size} texts measured; the sweep's own numbers are in the grading evidence]",
            offenders.isEmpty() && platform.isEmpty(),
        )
    }

    /** Every control a person can hit is at least 48dp square, measured on the running display. */
    private fun everyTargetMeetsTheFloor(theme: String) {
        val run = sweepEveryCard(theme, "target-size", measureContrast = false)
        val controls = largestObservationPerControl(run.nodes)
        lastMeasurement = "${controls.size} controls, sizes " + controls.joinToString(", ") {
            "${it.name()}=${dp(it.bounds.width())}x${dp(it.bounds.height())}dp"
        } + "; every node the sweep saw: " + run.nodes.joinToString(", ") {
            "${it.name()}[clickable=${it.clickable},visible=${it.visible},${dp(it.bounds.width())}x${dp(it.bounds.height())}dp]"
        }
        assertTrue(
            "touch target size: the $theme sweep found ${controls.size} controls on a screen that " +
                "has at least $MINIMUM_CONTROLS, so it was measuring something other than this screen",
            controls.size >= MINIMUM_CONTROLS,
        )
        val offenders = controls.filter {
            UiHarness.dpOf(it.bounds.width()) < TARGET_FLOOR_DP || UiHarness.dpOf(it.bounds.height()) < TARGET_FLOOR_DP
        }.map {
            "touch target size: ${it.name()} measures " +
                "${dp(it.bounds.width())}x${dp(it.bounds.height())}dp in the $theme theme, " +
                "and the floor is ${TARGET_FLOOR_DP.toInt()}x${TARGET_FLOOR_DP.toInt()}dp"
        }
        val platform = run.errorsFrom(TARGET_CHECKS).map { "touch target size: " + it.describe() }
        assertTrue(
            (offenders + platform).joinToString("\n  ") +
                "\n  [${controls.size} controls measured; each one's size is in the grading evidence]",
            offenders.isEmpty() && platform.isEmpty(),
        )
    }

    /**
     * The LARGEST rectangle each control was seen at, across the sweep's three scroll positions.
     *
     * `boundsInScreen` is clipped to what is on the glass, so a control half over the fold reports
     * the sliver that is showing - the first emulator run measured the start/stop control at 145x1dp
     * and called it an undersized target when it is 145x48. Every card is scrolled fully into view at
     * some point in the sweep, so the largest observation is the control's real size, and a control
     * that is genuinely small is small at every one of them.
     */
    private fun largestObservationPerControl(nodes: List<A11yNode>): List<A11yNode> {
        val biggest = mutableMapOf<String, A11yNode>()
        for (node in nodes.filter { it.isControl() }) {
            val key = node.name()
            val seen = biggest[key]
            val area = node.bounds.width().toLong() * node.bounds.height().toLong()
            val seenArea = seen?.let { it.bounds.width().toLong() * it.bounds.height().toLong() } ?: -1L
            if (area > seenArea) biggest[key] = node
        }
        return biggest.values.toList()
    }

    /** Every control and every text field has something a screen reader can announce. */
    private fun everyControlHasASpokenName(theme: String) {
        val run = sweepEveryCard(theme, "spoken-name", measureContrast = false)
        val controls = largestObservationPerControl(run.nodes)
        lastMeasurement = "${controls.size} controls, names " +
            controls.joinToString(", ") { "${it.name()}=\"${spokenNameOf(it, run.nodes)}\"" }
        assertTrue(
            "spoken name: the $theme sweep found ${controls.size} controls on a screen that has at " +
                "least $MINIMUM_CONTROLS, so it was measuring something other than this screen",
            controls.size >= MINIMUM_CONTROLS,
        )
        val offenders = controls.filter { spokenNameOf(it, run.nodes).isBlank() }.map {
            "spoken name: ${it.name()} is a control a person can operate and a screen reader would " +
                "announce it with nothing at all in the $theme theme (no text, no content " +
                "description, no hint, and nothing inside it either; it is a " +
                "${it.className.substringAfterLast('.')} at ${it.bounds.toShortString()})"
        }
        val platform = run.errorsFrom(SPOKEN_NAME_CHECKS).map { "spoken name: " + it.describe() }
        assertTrue(
            (offenders + platform).joinToString("\n  ") +
                "\n  [${controls.size} controls measured; each one's name is in the grading evidence]",
            offenders.isEmpty() && platform.isEmpty(),
        )
    }

    /**
     * What a screen reader would announce for a control: its own text, or anything inside it.
     *
     * Compose publishes a control and the label drawn inside it as separate nodes rather than one
     * merged node, so a control with a perfectly good label has no text OF ITS OWN. Reading only the
     * control's own text reported every control on the screen as unnamed, which is a defect in the
     * reading and not in the screen; requiring the control to be a leaf found no controls at all.
     * Containment is how a subtree reads geometrically, and it is what a person hears.
     */
    private fun spokenNameOf(control: A11yNode, nodes: List<A11yNode>): String {
        val own = control.ownSpokenName()
        if (own.isNotBlank()) return own
        return nodes.filter { it !== control && control.contains(it) }
            .map { it.ownSpokenName() }
            .firstOrNull { it.isNotBlank() }
            .orEmpty()
    }

    /**
     * Whether a node is, or sits inside, a control the screen has disabled.
     *
     * WCAG 2.2's 1.4.3 has no contrast requirement for an inactive component, and Material paints one
     * at 38% of the content colour by design. On an emulator there is no location grant, so the
     * start/stop control is disabled and its label measures about 2.3:1 - the platform's own styling,
     * not a defect of this screen.
     */
    private fun isInactive(node: A11yNode, nodes: List<A11yNode>): Boolean {
        if (!node.enabled) return true
        return nodes.any { it !== node && !it.enabled && (it.clickable || it.editable) && it.contains(node) }
    }

    /**
     * No state on this screen is carried by its colour alone: every one of the four AC13 names is
     * also a word.
     */
    private fun noStateByColourAlone(theme: String) {
        compose.waitForIdle()
        val warning = textOf("collection-error")
        lastMeasurement = "collection-error=\"$warning\" collection-running=\"" +
            textOf("collection-running") + "\" permission-body=\"" + textOf("permission-body") + "\""
        assertTrue(
            "state carried by colour alone: collection-error renders \"$warning\" in the $theme " +
                "theme, which names no state in words - a reader who cannot see the error colour is told nothing",
            warning.contains("Warning", ignoreCase = true),
        )
        val running = textOf("collection-running")
        assertTrue(
            "state carried by colour alone: collection-running renders \"$running\" in the $theme " +
                "theme, which is not a word a reader can act on",
            running.equals("Running", true) || running.equals("Stopped", true),
        )

        // AC13 enumerates FOUR states, not two: "permission step, running or stopped, a warning, a
        // refused save". The permission step names itself in words above its button, so a reader
        // who cannot see which control is emphasised is still told which step they are on.
        val step = textOf("permission-body")
        assertTrue(
            "state carried by colour alone: permission-body renders \"$step\" in the $theme theme, " +
                "which names no step in words - only the card's colour would say which step a reader is on",
            step.isNotBlank(),
        )

        // And the fourth: a refused save leads with the refusal as a WORD rather than carrying it
        // in the error colour alone. setUp cleared the stored configuration, so Save refuses.
        compose.onNodeWithTag("action-save").performScrollTo().performClick()
        compose.waitForIdle()
        val verdict = textOf("config-verdict")
        assertTrue(
            "state carried by colour alone: config-verdict renders \"$verdict\" in the $theme theme, " +
                "which carries the refusal in its colour alone",
            verdict.contains("Not saved", ignoreCase = true),
        )
        UiHarness.evidence(
            "AC13 no-state-by-colour-alone [$theme]: collection-error=\"$warning\" " +
                "collection-running=\"$running\" config-verdict=\"$verdict\"",
        )
    }

    /**
     * One sweep per card, because the platform's checks only ever see the window as it is RIGHT NOW.
     *
     * The home screen scrolls and is taller than a phone viewport, so a single sweep inspects the top
     * and reports a clean bill of health for everything below it - which is what the first emulator
     * run this suite ever had did: it passed the accessibility claim twice while the collection card,
     * carrying the defect, was off the glass.
     */
    private fun sweepEveryCard(theme: String, claim: String, measureContrast: Boolean): AtfRun {
        compose.waitForIdle()
        Thread.sleep(500)
        val nodes = mutableListOf<A11yNode>()
        val findings = mutableListOf<AtfFinding>()
        var evaluated = 0
        var notRun = 0
        for (tag in listOf("card-permissions", "card-server", "card-collection")) {
            compose.onNodeWithTag(tag).performScrollTo()
            compose.waitForIdle()
            Thread.sleep(400)
            val run = UiHarness.sweep(measureContrast)
            nodes += run.nodes
            findings += run.findings
            evaluated += run.evaluated
            notRun += run.notRun
        }
        val merged = AtfRun(nodes, findings, evaluated, notRun)
        UiHarness.evidence(
            "AC13 $claim [$theme]: ${nodes.size} nodes, platform checks evaluated $evaluated results " +
                "and declined $notRun",
        )
        for (node in nodes.filter { it.isRenderedText() || it.isControl() }) {
            UiHarness.evidence(
                "  $claim [$theme] ${node.name()}: ${dp(node.bounds.width())}x${dp(node.bounds.height())}dp " +
                    "contrast=" + (node.contrast?.let { ratio(it) } ?: "not measured") +
                    " spoken=\"${spokenNameOf(node, nodes)}\" clickable=${node.clickable}" +
                    " editable=${node.editable} enabled=${node.enabled}" +
                    " inactive=${isInactive(node, nodes)} children=${node.childCount}",
            )
        }
        for (finding in findings) UiHarness.evidence("  $claim [$theme] platform: " + finding.describe())
        return merged
    }

    // --- AC14: operable without a pointer -------------------------------------------------------

    @Test
    fun AC14_operable_without_a_pointer() {
        UiHarness.launch()
        operableWithoutAPointer()
    }

    @Test
    fun AC14_operable_without_a_pointer_demonstration() {
        // AC14 makes three SHALLs, and two of them are graded by this measuring code: every control
        // reachable and activatable, and the URL and token saved to the same effect a touch has. So
        // it is shown going red against a mutation for each.
        UiHarness.launch(UiMutation.SAVE_NOT_FOCUSABLE)
        assertFailsNaming("action-save", "directional navigation", "the save control") { operableWithoutAPointer() }

        // And the half impl-gate finding F1 named: a save control that is reachable, focusable and
        // activatable, and whose activation does not save. An effect assertion that accepts any
        // verdict at all stays green against this.
        UiHarness.launch(UiMutation.SAVE_WITHOUT_EFFECT)
        assertFailsNaming("action-save", "keyboard save", "the effect of a keyboard save") { operableWithoutAPointer() }
    }

    @Test
    fun AC14_focus_indicator_is_visible() {
        UiHarness.launch()
        focusIndicatorIsVisible()
    }

    @Test
    fun AC14_focus_indicator_is_visible_demonstration() {
        UiHarness.launch(UiMutation.FOCUS_INDICATOR_SUPPRESSED)
        assertFailsNaming("action-save", "focus indicator", "the save control") { focusIndicatorIsVisible() }
    }

    /**
     * The screen is driven end to end with no touch at all: EVERY control reached by the directional
     * keys and reported activatable where it stands, then text into both fields, then the save
     * control activated with the centre key and the effect asserted on what the screen then SAYS.
     *
     * Impl-gate finding F20 was this case going red on the emulator, and the cause was a product
     * defect rather than a harness one: a Compose text field consumes the arrow keys whether or not
     * its caret has anywhere to go, so a directional traversal that entered the server URL field
     * could never leave it and every control below - the save control included - was unreachable.
     * `Modifier.directionalPassThrough` on both fields is the fix.
     *
     * Two later findings shaped the rest of it, and both were assertions that could not fail:
     *
     *  - **F2**: AC14 says "every control reachable and activatable" and this graded ONE. A control
     *    nothing measures is F20 one step away, so the census below names every control the home
     *    screen declares and requires each to be operable, reached and activatable.
     *  - **F1**: the effect was asserted with `contains("Saved") || contains("Not saved")`, which
     *    accepts both verdicts this screen can draw - the second disjunct says so, and Kotlin's
     *    case-insensitive `contains` makes the first say it too. The claim passed whether the
     *    keyboard save SAVED or the screen REFUSED. It is now the saved verdict or nothing, and the
     *    SAVE_WITHOUT_EFFECT mutation is what shows it going red.
     */
    private fun operableWithoutAPointer() {
        compose.waitForIdle()
        UiHarness.suppressSoftKeyboard()

        // --- every control, reachable and activatable ---------------------------------------------
        //
        // One walk down the screen records where focus went and what the platform said each focused
        // node could do; the per-control lookups below then read that walk. A control the walk never
        // reached gets its own traversal, which is the case worth spending presses on.
        val visited = mutableListOf<FocusStop>()
        walkTheScreen(visited)
        val reached = linkedMapOf<String, Boolean>()
        reached["permission-action"] = focusByDirection("permission-action", visited)
        reached["action-precise"] = focusByDirection("action-precise", visited)
        reached["explain-permissions"] = focusByDirection("explain-permissions", visited)
        reached["field-url"] = focusByDirection("field-url", visited)
        reached["field-token"] = focusByDirection("field-token", visited)
        reached["action-save"] = focusByDirection("action-save", visited)
        reached["explain-server"] = focusByDirection("explain-server", visited)
        reached["action-collection"] = focusByDirection("action-collection", visited)
        reached["explain-counters"] = focusByDirection("explain-counters", visited)

        val route = visited.joinToString(" -> ") { it.name }.ifBlank { "(nothing at all)" }
        val census = reached.entries.joinToString(", ") {
            "${it.key}=${operabilityOf(it.key)}/${if (it.value) "reached" else "NOT-REACHED"}"
        }
        lastMeasurement = "focus visited $route; census $census"
        UiHarness.evidence("AC14 operable without a pointer: visited $route")
        UiHarness.evidence("AC14 operable without a pointer: census $census")

        val notOperable = mutableListOf<String>()
        val unreachable = mutableListOf<String>()
        val unactivatable = mutableListOf<String>()
        for ((tag, wasReached) in reached) {
            when (operabilityOf(tag)) {
                NOT_DRAWN -> notOperable.add("$tag (this screen state does not draw it at all)")
                DISABLED -> notOperable.add("$tag (drawn, but the screen has it disabled)")
                else -> {
                    if (!wasReached) {
                        unreachable.add(tag)
                    } else {
                        val stop = visited.first { it.name == tag }
                        if (!stop.activatable) unactivatable.add("$tag (${stop.detail})")
                    }
                }
            }
        }

        // The census is only evidence while every control it names is actually on the glass and
        // operable: a control the screen has stopped drawing would otherwise drop silently out of
        // the measured set, which is precisely how a control goes unmeasured.
        assertTrue(
            "directional navigation: the census cannot grade these controls because this screen " +
                "state does not offer them - " + notOperable.joinToString(", ") +
                " - so AC14's \"every control\" would be measured over fewer than the " +
                "${reached.size} the home screen declares",
            notOperable.isEmpty(),
        )
        assertTrue(
            "directional navigation: these controls could not be reached with the directional keys " +
                "at all - " + unreachable.joinToString(", ") +
                " - so a person using a keyboard, a d-pad or a screen reader's directional gestures " +
                "cannot operate them. $DIRECTIONAL_PRESSES presses of DPAD_DOWN focused, in order: $route",
            unreachable.isEmpty(),
        )
        assertTrue(
            "directional navigation: these controls take focus but the platform reports nothing to " +
                "activate on them, so the centre key would do nothing where they stand - " +
                unactivatable.joinToString(", "),
            unactivatable.isEmpty(),
        )

        // --- the URL and the token, entered and saved with no pointer -----------------------------
        compose.onNodeWithTag("field-url").performScrollTo().performTextReplacement(A_USABLE_URL)
        compose.onNodeWithTag("field-token").performScrollTo().performTextReplacement(A_USABLE_TOKEN)
        compose.waitForIdle()

        // Typing may still have raised an IME; while one is up it is the IME that receives a d-pad
        // key, and the traversal below would go to the keyboard rather than to the screen.
        UiHarness.dismissKeyboard()
        compose.waitForIdle()

        // The census walked PAST the save control, so focus has to be put back on it before the
        // centre key can mean anything. This is a fresh traversal, not a memo.
        assertTrue(
            "directional navigation: the save control (action-save) could not be focused again after " +
                "the census, so the centre key had nothing to activate. Focus visited: $route",
            focusOnto("action-save"),
        )
        sendKey(KeyEvent.KEYCODE_DPAD_CENTER)
        compose.waitForIdle()
        Thread.sleep(500)

        // --- and the EFFECT, which is the one a touch has -----------------------------------------
        val saved = UiHarness.context.getString(R.string.config_saved)
        val verdict = textOf("config-verdict").trim()
        val stored = ClientPreferences(UiHarness.context)
        lastMeasurement = "config-verdict=\"$verdict\" (a touch leaves \"$saved\"); " +
            "stored baseUrl=\"${stored.baseUrl.orEmpty()}\" token=${if (stored.deviceToken.isNullOrEmpty()) "empty" else "set"}"
        UiHarness.evidence("AC14 operable without a pointer: keyboard save left \"$verdict\" on config-verdict")
        // The verdict is pinned as a PHRASE, and the phrase is the one the screen draws for a save
        // that happened. `internal/uiverify`'s F1 regression artifact runs inside `make check` and
        // holds the two ends together: it reads config_saved and config_not_saved out of strings.xml
        // and refuses this predicate unless it accepts the saved verdict AND rejects the refusal.
        // That pairing is the fix for impl-gate finding F1, where the predicate was
        // `contains("Saved") || contains("Not saved")` and accepted both - "Not saved" contains
        // "Saved", so even the first disjunct alone could not tell one outcome from the other.
        assertTrue(
            "keyboard save: saving from the keyboard produced \"$verdict\" on config-verdict after " +
                "action-save was activated with the centre key, and a save that happens produces " +
                "\"$saved\". AC14 asks for the SAME EFFECT a touch has, asserted on what the screen " +
                "then shows, and this screen draws exactly two verdicts - so a predicate that accepts " +
                "either of them asserts nothing at all",
            verdict.contains("Saved to this device", ignoreCase = true),
        )
        // The screen said it saved; the store agrees. This is not the clause's own assertion - AC14
        // puts that on what the screen shows - it is the check that the screen was telling the truth.
        assertEquals(
            "keyboard save: config-verdict says \"$verdict\" after action-save was activated from the " +
                "keyboard, but the stored server URL is \"${stored.baseUrl.orEmpty()}\"; the effect a " +
                "touch has is that the configuration is kept",
            A_USABLE_URL,
            stored.baseUrl.orEmpty(),
        )
    }

    /** One stop a directional traversal made, and what the platform said about the control there. */
    private data class FocusStop(val name: String, val activatable: Boolean, val detail: String)

    /**
     * Walks the whole screen once with the directional keys, recording every control focus landed on.
     *
     * The per-control lookups read this rather than each re-walking the screen: a traversal restarts
     * from wherever focus is, so nine independent walks would measure nine different things.
     */
    private fun walkTheScreen(visited: MutableList<FocusStop>) {
        for (i in 0 until DIRECTIONAL_PRESSES) {
            sendKey(KeyEvent.KEYCODE_DPAD_DOWN)
            compose.waitForIdle()
            recordFocusStop(UiHarness.focusedName(), visited)
        }
    }

    /**
     * Records where focus is NOW, under [name], unless the traversal is still standing where it was.
     *
     * `activatable` is the PLATFORM's own answer about the focused node, which is what decides
     * whether a centre key press does anything: a node that reports neither a click nor an editable
     * field is one the directional keys can reach and nothing more.
     */
    private fun recordFocusStop(name: String, visited: MutableList<FocusStop>) {
        if (visited.isNotEmpty() && visited.last().name == name) return
        val node = UiHarness.focusedNode()
        visited.add(
            FocusStop(
                name = name,
                activatable = node != null && (node.isClickable || node.isEditable),
                detail = if (node == null) {
                    "nothing held focus"
                } else {
                    "clickable=${node.isClickable} editable=${node.isEditable} enabled=${node.isEnabled}"
                },
            ),
        )
    }

    /**
     * Whether the running screen draws [tag] at all, and whether it has disabled it.
     *
     * Read from the app as it stands rather than from a list written here: a control this screen
     * state does not offer is not a control AC14 can ask to be reachable, and a control it has
     * disabled has no functionality for the keyboard to reach (WCAG 2.2's 2.1.1 binds "all
     * functionality"). Saying which of the three it is, out loud, is what stops either case from
     * quietly shrinking the measured set.
     */
    private fun operabilityOf(tag: String): String {
        val nodes = compose.onAllNodesWithTag(tag, useUnmergedTree = true).fetchSemanticsNodes()
        if (nodes.isEmpty()) return NOT_DRAWN
        var disabled = false
        for (node in nodes) walk(node) { if (it.config.contains(SemanticsProperties.Disabled)) disabled = true }
        return if (disabled) DISABLED else OPERABLE
    }

    /**
     * Puts focus ON [tag] and LEAVES it there, walking up first and then down.
     *
     * Distinct from [focusByDirection], which answers "was this control reachable" and is allowed to
     * answer from the census. Activating a control needs focus to actually be on it, and after the
     * census it is at the bottom of the screen.
     */
    private fun focusOnto(tag: String): Boolean {
        if (isFocused(tag)) return true
        for (code in listOf(KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN)) {
            for (i in 0 until DIRECTIONAL_PRESSES) {
                sendKey(code)
                compose.waitForIdle()
                if (isFocused(tag)) return true
            }
        }
        return false
    }

    /**
     * The focused control PAINTS something, measured in the band that belongs to the focus ring and
     * to nothing else.
     *
     * F20 aborted before this half ever ran, so it had never been exercised against a rendering at
     * all. It is measured on the outer [FOCUS_RING_INSET_DP]dp of the control rather than over the
     * whole of it because a Material ripple tints the INSIDE of a control on focus whatever the ring
     * does: a whole-control diff would report an indicator that was never painted.
     */
    private fun focusIndicatorIsVisible() {
        compose.waitForIdle()
        UiHarness.suppressSoftKeyboard()

        // The two assertions below the capture are the ONLY ones in this function whose message names
        // both the mutated view and the check string "focus indicator", and that is deliberate.
        // `assertFailsNaming` accepts any failure carrying both, so a precondition that named them
        // would let the AC26 demonstration report a traversal failure - or a focus move that would not
        // budge - as the indicator having been demonstrated (impl-gate finding F4). Both of those now
        // fail under a different check name.
        val visited = mutableListOf<FocusStop>()
        val reached = focusByDirection("action-save", visited)
        assertTrue(
            "directional navigation: the save control (action-save) was not reachable, so there is " +
                "nothing focused to photograph - visited: " + visited.joinToString(" -> ") { it.name },
            reached,
        )
        val focused = captureOf(ringTagOf("action-save"))

        moveFocusAwayFrom("action-save")
        val unfocused = captureOf(ringTagOf("action-save"))
        val differing = ringPixelsThatDiffer(focused, unfocused)
        lastMeasurement = "$differing pixels of the ring band differ; the focused capture is " +
            "${focused.width}x${focused.height} and the unfocused one ${unfocused.width}x${unfocused.height}"
        assertTrue(
            "focus indicator: focusing the save control (action-save) changed $differing pixels of " +
                "its outer ${FOCUS_RING_INSET_DP}dp band, and a painted indicator changes at least " +
                "$MINIMUM_RING_PIXELS - so nothing on the glass says which control has focus",
            differing >= MINIMUM_RING_PIXELS,
        )
        UiHarness.evidence("AC14 focus indicator: $differing pixels of the ring band differ when focused")
    }

    // --- AC15: the counters name their set, and absence is not zero -----------------------------

    @Test
    fun AC15_counters_state_their_set() {
        UiHarness.launch()
        aRunHasHappened()
        countersStateTheirSet()
    }

    @Test
    fun AC15_counters_state_their_set_demonstration() {
        UiHarness.launch(UiMutation.COUNTERS_WITHOUT_THEIR_SET)
        aRunHasHappened()
        assertFails("counters with no set named beside them were not caught") { countersStateTheirSet() }
    }

    private fun countersStateTheirSet() {
        compose.waitForIdle()
        for (tag in listOf("counter-delivered", "counter-queued", "counter-dropped")) {
            val set = compose.onAllNodesWithTag("$tag-set", useUnmergedTree = true).fetchSemanticsNodes()
            assertEquals(
                "$tag renders no statement of the set it was counted over, so it is a figure a reader " +
                    "will take for a lifetime total",
                1,
                set.size,
            )
            val words = set[0].config.getOrNull(SemanticsProperties.Text)?.joinToString(" ") { it.text } ?: ""
            assertTrue("$tag's set statement is empty", words.isNotBlank())
            assertTrue(
                "$tag's set statement \"$words\" does not say when it was counted from",
                words.contains(":") || words.contains("not started") || words.contains("not read"),
            )
        }
    }

    @Test
    fun AC15_never_measured_reads_not_recorded() {
        UiHarness.launch()
        nothingHasBeenMeasured()
        neverMeasuredReadsNotRecorded()
    }

    @Test
    fun AC15_never_measured_reads_not_recorded_demonstration() {
        UiHarness.launch(UiMutation.NOT_RECORDED_AS_ZERO)
        nothingHasBeenMeasured()
        assertFails("a never-measured counter rendered as 0 was not caught") { neverMeasuredReadsNotRecorded() }
    }

    private fun neverMeasuredReadsNotRecorded() {
        compose.waitForIdle()
        for (tag in listOf("counter-delivered", "counter-dropped")) {
            val text = textOf("$tag-value")
            assertTrue(
                "$tag has never been measured but renders \"$text\"; an unmeasured figure must read as " +
                    "not recorded, never as 0 and never as a dash",
                text.contains("not recorded", ignoreCase = true),
            )
        }
        // ... and a genuine measured zero still reads as zero.
        val queued = textOf("counter-queued-value")
        assertTrue(
            "the queue was read and holds nothing, but renders \"$queued\"; a measured zero must read as 0",
            queued.trim().endsWith("0"),
        )
    }

    // --- AC16: one unreadable figure, and the three states --------------------------------------

    @Test
    fun AC16_unreadable_queue_costs_only_itself() {
        UiHarness.launch()
        theQueueCannotBeRead()
        unreadableQueueCostsOnlyItself()
    }

    @Test
    fun AC16_unreadable_queue_costs_only_itself_demonstration() {
        UiHarness.launch(UiMutation.UNREADABLE_QUEUE_BLANKS_CARD)
        theQueueCannotBeRead()
        assertFails("an unreadable queue that blanked the card was not caught") { unreadableQueueCostsOnlyItself() }
    }

    private fun unreadableQueueCostsOnlyItself() {
        compose.waitForIdle()
        val queued = textOf("counter-queued-value")
        assertTrue(
            "the queue could not be read but the figure renders \"$queued\"; it must read as unavailable in words",
            queued.contains("unavailable", ignoreCase = true),
        )
        // Everything else still draws, and still works. Scrolled to first: the column is taller
        // than the viewport, so "still draws" means a person can reach it, not that all three cards
        // fit on the glass at once.
        compose.onNodeWithTag("card-permissions").performScrollTo().assertIsDisplayed()
        compose.onNodeWithTag("card-server").performScrollTo().assertIsDisplayed()
        compose.onNodeWithTag("action-collection").performScrollTo().assertIsDisplayed()
        compose.onNodeWithTag("counter-delivered-value").performScrollTo().assertIsDisplayed()
        compose.onNodeWithTag("action-save").performScrollTo().performClick()
        compose.waitForIdle()
        assertTrue(
            "with the queue unreadable the save control no longer works",
            textOf("config-verdict").isNotBlank(),
        )
    }

    @Test
    fun AC16_three_states_are_distinct() {
        UiHarness.launch()
        threeStatesAreDistinct()
    }

    @Test
    fun AC16_three_states_are_distinct_demonstration() {
        UiHarness.launch(UiMutation.STATES_INDISTINGUISHABLE)
        assertFails("three states rendered with the same words were not caught") { threeStatesAreDistinct() }
    }

    private fun threeStatesAreDistinct() {
        val seen = mutableListOf<String>()

        UiHarness.resetStatus() // nothing has been read yet
        compose.waitForIdle()
        assertEquals(
            "the collection card draws more than one state element, so two of the three could show at once",
            1,
            compose.onAllNodesWithTag("collection-state").fetchSemanticsNodes().size,
        )
        seen.add(textOf("collection-state"))

        nothingHasBeenMeasured()
        compose.waitForIdle()
        seen.add(textOf("collection-state"))

        theQueueCannotBeRead()
        compose.waitForIdle()
        seen.add(textOf("collection-state"))

        assertTrue("a state rendered no words at all: $seen", seen.none { it.isBlank() })
        assertEquals(
            "the three states do not read differently: $seen",
            3,
            seen.map { it.lowercase() }.toSet().size,
        )
    }

    // --- AC17: stale is never shown as current --------------------------------------------------

    @Test
    fun AC17_stopped_reads_last_known() {
        UiHarness.launch()
        stoppedReadsLastKnown()
    }

    @Test
    fun AC17_stopped_reads_last_known_demonstration() {
        UiHarness.launch(UiMutation.ALWAYS_CURRENT)
        assertFails("a stopped run still reading as current was not caught") { stoppedReadsLastKnown() }
    }

    private fun stoppedReadsLastKnown() {
        // Running, with a fix inside the reporting interval: the counters describe now.
        aRunHasHappened()
        compose.waitForIdle()
        val whileRunning = textOf("counters-freshness")
        assertTrue(
            "a live run renders \"$whileRunning\"; it must read as current",
            whileRunning.contains("current", ignoreCase = true),
        )

        // Stopped: the same figures, now stated as last known at a time.
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            CollectionStatus.running = false
        }
        compose.waitForIdle()
        Thread.sleep(300)
        val whenStopped = textOf("counters-freshness")
        assertTrue(
            "collection stopped but the readout still says \"$whenStopped\"; a frozen readout must not " +
                "be presented as a live one",
            whenStopped.contains("last known", ignoreCase = true),
        )
        assertNotEquals("stopping changed nothing about what the screen says", whileRunning, whenStopped)

        // Running again but silent for longer than the reporting interval: still last known.
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            CollectionStatus.running = true
            CollectionStatus.lastFixAtMillis = System.currentTimeMillis() - 10 * 60_000
            CollectionStatus.runStartedAtMillis = System.currentTimeMillis() - 20 * 60_000
        }
        compose.waitForIdle()
        Thread.sleep(300)
        val whenSilent = textOf("counters-freshness")
        assertTrue(
            "a run that has produced no fix for ten minutes renders \"$whenSilent\"; it must read as last known",
            whenSilent.contains("last known", ignoreCase = true),
        )

        // And collection resuming puts them back to current.
        aRunHasHappened()
        compose.waitForIdle()
        Thread.sleep(300)
        val resumed = textOf("counters-freshness")
        assertTrue(
            "collection resumed but the readout still says \"$resumed\"",
            resumed.contains("current", ignoreCase = true),
        )
    }

    // --- fixtures --------------------------------------------------------------------------------
    //
    // The screen is the unit under test; these are its inputs. The spec allows a doctored input
    // where the running stack has no path to the state - a queue directory that cannot be listed is
    // exactly that - and CollectionStatus is a process-scoped singleton the instrumentation shares
    // with the app, so setting it is setting the real thing the screen reads.

    private fun aRunHasHappened() {
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            val now = System.currentTimeMillis()
            CollectionStatus.reset(now)
            CollectionStatus.recordQueued(2, now)
            CollectionStatus.recordFlush(delivered = 3, discarded = 0, queued = 2, reason = null, now = now)
            CollectionStatus.running = true
            CollectionStatus.lastFixAtMillis = now
        }
    }

    private fun nothingHasBeenMeasured() {
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            CollectionStatus.clearForTest()
            CollectionStatus.recordQueued(0)
        }
    }

    private fun theQueueCannotBeRead() {
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            CollectionStatus.clearForTest()
            CollectionStatus.recordQueued(null)
        }
    }

    // --- helpers ---------------------------------------------------------------------------------

    private fun textOf(tag: String): String {
        val nodes = compose.onAllNodesWithTag(tag, useUnmergedTree = true).fetchSemanticsNodes()
        if (nodes.isEmpty()) return ""
        return nodes[0].config.getOrNull(SemanticsProperties.Text)?.joinToString(" ") { it.text } ?: ""
    }

    /** Every text Compose is currently rendering, merged tree, in one flat list. */
    private fun renderedTexts(): List<String> {
        val out = mutableListOf<String>()
        for (root in compose.onAllNodesWithTag("home", useUnmergedTree = true).fetchSemanticsNodes() +
            compose.onAllNodesWithTag("explanation", useUnmergedTree = true).fetchSemanticsNodes()
        ) {
            walk(root) { node ->
                node.config.getOrNull(SemanticsProperties.Text)?.forEach { out.add(it.text) }
            }
        }
        return out.map { it.trim() }.filter { it.isNotEmpty() }
    }

    private fun walk(node: androidx.compose.ui.semantics.SemanticsNode, visit: (androidx.compose.ui.semantics.SemanticsNode) -> Unit) {
        visit(node)
        for (child in node.children) walk(child, visit)
    }

    /**
     * Walks down with the directional pad, exactly as a person with a keyboard, a d-pad or a screen
     * reader's directional gestures would, and records where focus went.
     *
     * The record is the point. Finding F20 was this returning false with nothing to say about WHY,
     * which left "product defect or harness defect" undecidable without another device. The visited
     * list distinguishes the three possibilities on the spot: focus never moved at all (the keys are
     * not reaching the screen), focus stalled on one control (that control is consuming them), or
     * focus visited everything except the one being looked for (the focus order skips it).
     */
    private fun focusByDirection(tag: String, visited: MutableList<FocusStop>): Boolean {
        // Already focused once on this traversal. A control is reachable or it is not; walking to it
        // a second time would only measure where the previous lookup happened to leave focus.
        if (visited.any { it.name == tag }) return true
        // A control this screen state does not draw, or has disabled, cannot take focus and there is
        // nothing to walk toward. Say so at once rather than spending the whole traversal budget on
        // it; the caller reports it as not-operable, which is a louder answer than "not reached".
        if (operabilityOf(tag) != OPERABLE) return false
        sendKey(KeyEvent.KEYCODE_DPAD_DOWN)
        for (i in 0 until DIRECTIONAL_PRESSES) {
            compose.waitForIdle()
            val here = UiHarness.focusedName()
            recordFocusStop(if (here == tag || isFocused(tag)) tag else here, visited)
            if (visited.last().name == tag) return true
            sendKey(KeyEvent.KEYCODE_DPAD_DOWN)
        }
        return false
    }

    /**
     * Whether [tag] holds keyboard focus, judged the way an assistive technology would and then, as
     * a fallback, over the tagged node AND its subtree in both the merged and the unmerged tree.
     *
     * The platform's own answer comes first and it is now read by test tag rather than by label:
     * the screen publishes `testTagsAsResourceId`, so the focused node names itself. Matching on the
     * rendered label instead made this answer depend on which words a control happened to be drawn
     * with. The Compose reads stay because the testTag and the Focused property are not always on the
     * same semantics node.
     */
    private fun isFocused(tag: String): Boolean {
        if (UiHarness.focusedName() == tag) return true
        for (unmerged in listOf(true, false)) {
            for (node in compose.onAllNodesWithTag(tag, useUnmergedTree = unmerged).fetchSemanticsNodes()) {
                var found = false
                walk(node) { if (it.config.getOrNull(SemanticsProperties.Focused) == true) found = true }
                if (found) return true
            }
        }
        return false
    }

    /**
     * The pixels of one control, wherever it currently sits, rather than of the whole display.
     *
     * Captured from the UNMERGED tree, and that is the whole point. The test tag and the Material
     * button's `mergeDescendants` clickable are on different layout nodes, so a merged lookup
     * resolves to the button's SURFACE - 79x42dp - while the node the tag is actually on, the one
     * carrying the focus ring and its inset, is 79x54dp. Capturing the merged node cut the ring out
     * of the picture entirely, and the claim was passing on the Material ripple's focus state layer
     * tinting the surface instead: the demonstration refused it, correctly, and said so.
     */
    private fun captureOf(tag: String): android.graphics.Bitmap {
        compose.onNodeWithTag(tag).performScrollTo()
        compose.waitForIdle()
        return compose.onNodeWithTag(tag, useUnmergedTree = true).captureToImage().asAndroidBitmap()
    }

    /**
     * Moves focus off [tag] without leaving the screen, and proves it moved.
     *
     * DOWN is tried first because the next control below is a button, while the one above is a text
     * field whose focus raises the soft keyboard and resizes the window. UP is the fallback for a
     * control that is the last focusable in its column. A traversal that failed to move focus would
     * otherwise give two identical captures and report a missing focus indicator that is there.
     */
    private fun moveFocusAwayFrom(tag: String) {
        for (code in listOf(KeyEvent.KEYCODE_DPAD_DOWN, KeyEvent.KEYCODE_DPAD_UP)) {
            sendKey(code)
            compose.waitForIdle()
            if (!isFocused(tag)) return
        }
        throw AssertionError(
            "focus traversal: focus could not be moved off $tag, so its unfocused paint cannot be " +
                "read. Named for the traversal rather than for the indicator on purpose: a " +
                "demonstration reads the failure message, and one that named the indicator here would " +
                "count a stuck focus as the indicator having been shown going red (finding F4)",
        )
    }

    private fun sendKey(code: Int) {
        InstrumentationRegistry.getInstrumentation().sendKeyDownUpSync(code)
        Thread.sleep(120)
    }

    /**
     * How many pixels of the control's outer focus-ring band changed between the two captures.
     *
     * Only the band, because that is the region the ring owns: a Material ripple paints a focus state
     * layer across the INSIDE of a control, so a whole-control diff comes back non-zero even when no
     * ring was painted at all, and the focus-indicator demonstration could never go red.
     */
    private fun ringPixelsThatDiffer(a: android.graphics.Bitmap, b: android.graphics.Bitmap): Int {
        // Measured on the ring's own wrapper, whose outer band nothing else paints in - see
        // RingedButton. Measuring it on the button itself read the Material ripple's focus state
        // layer instead of the ring, which is a difference that appears whether or not an indicator
        // was ever drawn.
        // The two captures are compared over the region they SHARE, and a size mismatch is not
        // treated as a difference. Returning "differs enormously" for one was an escape hatch that
        // could pass the claim without a ring ever being painted: the node's bounds are floats and a
        // capture taken at a different scroll offset can round to a pixel more or less, which is a
        // fact about rounding rather than about the indicator.
        val width = kotlin.math.min(a.width, b.width)
        val height = kotlin.math.min(a.height, b.height)
        // One dp inside the band, so the measurement is strictly of pixels the ring owns. The ripple's
        // focus state layer begins exactly where the ring's inset ends, and at the boundary a
        // half-pixel of it was enough to report an indicator that had not been painted.
        val band = kotlin.math.max(2, ((FOCUS_RING_INSET_DP - 1) * UiHarness.density()).toInt())
        var differing = 0
        for (y in 0 until height) {
            for (x in 0 until width) {
                val onTheBand = x < band || y < band || x >= width - band || y >= height - band
                if (!onTheBand) continue
                if (colourDistance(a.getPixel(x, y), b.getPixel(x, y)) > RING_COLOUR_TOLERANCE) differing++
            }
        }
        return differing
    }

    private fun colourDistance(p: Int, q: Int): Int {
        val dr = kotlin.math.abs(((p shr 16) and 0xFF) - ((q shr 16) and 0xFF))
        val dg = kotlin.math.abs(((p shr 8) and 0xFF) - ((q shr 8) and 0xFF))
        val db = kotlin.math.abs((p and 0xFF) - (q and 0xFF))
        return kotlin.math.max(dr, kotlin.math.max(dg, db))
    }

    private fun dp(px: Int): Int = kotlin.math.round(UiHarness.dpOf(px)).toInt()

    private fun ratio(value: Double): String = "%.2f:1".format(java.util.Locale.ENGLISH, value)

    /**
     * Runs a claim's own measuring code against a mutated screen and requires it to FAIL, naming the
     * view that was mutated and the check that belongs to this claim.
     *
     * AC25 and AC26 both turn on this: "a failure naming a different view or a different check" is
     * explicitly not a demonstration, because it would mean the mutation was caught by some other
     * assertion - or by a vacuity guard - rather than by the check it was built to trip. So the
     * message is read, not merely the fact that something threw.
     */
    private fun assertFailsNaming(view: String, check: String, context: String, body: () -> Unit) {
        val failure: Throwable? = try {
            body()
            null
        } catch (expected: AssertionError) {
            expected
        } catch (expected: RuntimeException) {
            expected
        }
        assertTrue(
            "the check for \"$check\" ($context) stayed green against a surface mutated to break " +
                "exactly that claim on $view, so its pass is not evidence.\n  What it measured: " +
                lastMeasurement.ifBlank { "(the measuring code recorded nothing)" },
            failure != null,
        )
        val message = failure!!.message.orEmpty()
        assertTrue(
            "the check for \"$check\" ($context) failed against the mutated screen, but its message " +
                "names neither the mutated view ($view) nor that check, so it is not this claim that " +
                "went red:\n$message",
            message.contains(view) && message.contains(check),
        )
        UiHarness.evidence("demonstration [$context]: \"$check\" went red naming $view")
    }

    private fun assertFails(why: String, body: () -> Unit) {
        val threw = try {
            body()
            false
        } catch (expected: AssertionError) {
            true
        } catch (expected: RuntimeException) {
            true
        }
        assertTrue("$why - the assertion stayed green against a surface broken to break it, so its pass is not evidence", threw)
    }

    private fun wordCount(s: String): Int = s.trim().split(Regex("\\s+")).count { it.isNotEmpty() }

    private companion object {
        /**
         * "Short enough to read at a glance" (AC12), given the same floor the browser surface is held
         * to. Long enough for "On Android 11 and later..." to fail it, short enough that every label
         * this screen ships passes.
         */
        const val MAX_LABEL_WORDS = 8

        /** WCAG 2.2 AA, success criterion 1.4.3: 4.5:1 for body text against its background. */
        const val CONTRAST_FLOOR = 4.5

        /**
         * The floor a touch target is held to, in dp.
         *
         * WCAG 2.2's 2.5.8 asks for 24 CSS pixels; Android's own guidance and the platform's
         * accessibility checks ask for 48dp, and the screen is already built to that. The stricter
         * of the two is the one applied here, so a control that passes this passes both.
         */
        const val TARGET_FLOOR_DP = 48f

        /** How many texts a sweep must have measured before a clean result means anything. */
        const val MINIMUM_TEXTS_MEASURED = 5

        /**
         * How many controls this screen has at its least populated: the permission action, both
         * server fields, the save control, the collection control and three explanation affordances.
         * A sweep that found fewer was not looking at this screen.
         */
        const val MINIMUM_CONTROLS = 5

        /** Presses of DPAD_DOWN a traversal is allowed before it reports a control unreachable. */
        const val DIRECTIONAL_PRESSES = 40

        /**
         * What the running screen says about a control the home tree can draw.
         *
         * AC14 binds every control to be "reachable and activatable", and the three answers are not
         * interchangeable: OPERABLE is the one the traversal grades, and the other two are reported
         * as failures rather than quietly excluded, because a control that stops being drawn is
         * exactly how one stops being measured.
         */
        const val OPERABLE = "operable"
        const val DISABLED = "disabled"
        const val NOT_DRAWN = "not-drawn"

        /**
         * A server configuration this client ACCEPTS, so that a save from the keyboard has the
         * effect a touch has rather than the refusal.
         *
         * It has to be usable: the verdict is validated, and a URL the client refuses would make the
         * screen say "Not saved" for a reason that has nothing to do with the keyboard.
         */
        const val A_USABLE_URL = "https://tracker.example.org"
        const val A_USABLE_TOKEN = "a-device-token"

        /** How different two pixels must be, per channel, to count as differing. */
        const val RING_COLOUR_TOLERANCE = 24

        /**
         * How many pixels of the ring band a painted focus indicator changes.
         *
         * A 48dp control at any density this app supports has a band of several hundred pixels, and
         * the ring fills it; the floor is set well under that so anti-aliasing at the rounded corners
         * cannot decide the answer, and well over zero so a suppressed ring cannot pass.
         */
        const val MINIMUM_RING_PIXELS = 100

        /** The platform checks that answer each of the three claims AC13 delegates. */
        val CONTRAST_CHECKS = setOf("TextContrastCheck", "ImageContrastCheck")
        val TARGET_CHECKS = setOf("TouchTargetSizeCheck")
        val SPOKEN_NAME_CHECKS = setOf("SpeakableTextPresentCheck", "EditableContentDescCheck")

        /**
         * A sentence of the length the domain layer really produces, for driving the states that
         * carry one.
         *
         * It is committed here at full length on purpose: the point of the sweep is that a sentence
         * this long reaching the screen has to FAIL, so a short fixture would prove nothing.
         */
        const val A_SENTENCE_THE_DOMAIN_PRODUCES =
            "Collection could not start: Android refused the location foreground service " +
                "(ForegroundServiceStartNotAllowedException). Check that location permission is " +
                "granted, then start it again from this screen."
    }
}

private fun <T> androidx.compose.ui.semantics.SemanticsConfiguration.getOrNull(
    key: androidx.compose.ui.semantics.SemanticsPropertyKey<T>,
): T? = this.getOrElseNullable(key) { null }

@Suppress("unused")
private fun SemanticsNodeInteraction.ignore() = Unit

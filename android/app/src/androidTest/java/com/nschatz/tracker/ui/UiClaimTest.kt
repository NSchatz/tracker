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
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
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
 */
@RunWith(AndroidJUnit4::class)
class UiClaimTest {

    @get:Rule
    val compose = createEmptyComposeRule()

    @Before
    fun setUp() {
        UiHarness.resetStatus()
        UiHarness.clearConfig()
        UiHarness.setNightMode(false)
    }

    @After
    fun tearDown() {
        UiHarness.resetStatus()
        UiHarness.clearConfig()
        UiHarness.resetDisplayProfile()
        UiHarness.setNightMode(false)
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
        compose.onNodeWithTag("action-save").performClick()
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
            compose.onNodeWithTag(tag).performClick()
            compose.waitForIdle()
            compose.onNodeWithTag("explanation").assertIsDisplayed()

            // The destination has to RENDER the paragraphs, not merely exist.
            val paragraphs = renderedTexts().filter { wordCount(it) > MAX_LABEL_WORDS }
            assertTrue(
                "the explanation opened from $tag renders no paragraph, so the claims were dropped rather than moved",
                paragraphs.isNotEmpty(),
            )
            compose.onNodeWithTag("explanation-back").performClick()
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
        for (node in compose.onAllNodesWithTag("home", useUnmergedTree = true).fetchSemanticsNodes()) {
            // Walk everything under the home column.
            walk(node) { child ->
                val text = child.config.getOrNull(SemanticsProperties.Text)?.joinToString(" ") { it.text }
                if (!text.isNullOrBlank()) {
                    // boundsInRoot is CLIPPED by every ancestor; size is what layout gave the node.
                    // A node whose visible box is narrower than its laid-out box has had its text cut
                    // off, and one whose box runs past the display has been pushed outside it.
                    if (child.boundsInRoot.width + 1f < child.size.width) {
                        offenders.add("clipped: \"$text\" (laid out ${child.size.width}px, visible ${child.boundsInRoot.width}px)")
                    }
                    if (child.positionInRoot.x + child.size.width > rootWidth + 1f) {
                        offenders.add("pushed outside: \"$text\" ends at ${child.positionInRoot.x + child.size.width}px of ${rootWidth}px")
                    }
                }
            }
        }
        assertTrue(
            "at a 360dp-wide profile the screen does not fit:\n  " + offenders.joinToString("\n  "),
            offenders.isEmpty(),
        )
    }

    // --- AC13: the platform's own accessibility checks, in both themes --------------------------

    @Test
    fun AC13_platform_checks_pass_light() {
        UiHarness.setNightMode(false)
        UiHarness.launch()
        platformChecksPass("light")
    }

    @Test
    fun AC13_platform_checks_pass_light_demonstration() {
        UiHarness.setNightMode(false)
        UiHarness.launch(UiMutation.LOW_CONTRAST_STATUS)
        assertFails("low-contrast text was not caught by the platform checks") { platformChecksPass("light") }
    }

    @Test
    fun AC13_platform_checks_pass_dark() {
        UiHarness.setNightMode(true)
        UiHarness.launch()
        platformChecksPass("dark")
    }

    @Test
    fun AC13_platform_checks_pass_dark_demonstration() {
        UiHarness.setNightMode(true)
        UiHarness.launch(UiMutation.LOW_CONTRAST_STATUS)
        assertFails("low-contrast text was not caught by the platform checks") { platformChecksPass("dark") }
    }

    private fun platformChecksPass(theme: String) {
        compose.waitForIdle()
        Thread.sleep(500)
        val run = UiHarness.accessibilityErrors()
        assertTrue(
            "the accessibility checks produced no results at all in the $theme theme (${run.note}), " +
                "so a clean sweep would prove nothing",
            run.resultCount > 0,
        )
        assertTrue(
            "the platform accessibility checks failed in the $theme theme:\n  " + run.describe(),
            run.errors.isEmpty(),
        )
    }

    @Test
    fun AC13_no_state_by_colour_alone() {
        CollectionStatus.recordBlocked(
            TroubleKind.PERMISSION_LOST,
            "Location permission was revoked, so collection stopped.",
        )
        UiHarness.launch()
        noStateByColourAlone()
    }

    @Test
    fun AC13_no_state_by_colour_alone_demonstration() {
        CollectionStatus.recordBlocked(
            TroubleKind.PERMISSION_LOST,
            "Location permission was revoked, so collection stopped.",
        )
        UiHarness.launch(UiMutation.WARNING_BY_COLOUR_ONLY)
        assertFails("a warning carried only by its colour was not caught") { noStateByColourAlone() }
    }

    private fun noStateByColourAlone() {
        compose.waitForIdle()
        val warning = compose.onNodeWithTag("collection-error").fetchSemanticsNode()
        val text = warning.config.getOrNull(SemanticsProperties.Text)?.joinToString(" ") { it.text } ?: ""
        assertTrue(
            "the degraded state renders \"$text\", which names no state in words - a reader who cannot " +
                "see the error colour is told nothing",
            text.contains("Warning", ignoreCase = true),
        )
        // The running/stopped state is a word.
        val running = textOf("collection-running")
        assertTrue(
            "the running state renders \"$running\", which is not a word a reader can act on",
            running.equals("Running", true) || running.equals("Stopped", true),
        )

        // AC13 enumerates FOUR states, not two: "permission step, running or stopped, a warning, a
        // refused save". The permission step names itself in words above its button, so a reader
        // who cannot see which control is emphasised is still told which step they are on.
        val step = textOf("permission-body")
        assertTrue(
            "the permission step renders \"$step\", which names no step in words - only the card's " +
                "colour would say which step a reader is on",
            step.isNotBlank(),
        )

        // And the fourth: a refused save leads with the refusal as a WORD rather than carrying it
        // in the error colour alone. setUp cleared the stored configuration, so Save refuses.
        compose.onNodeWithTag("action-save").performClick()
        compose.waitForIdle()
        val verdict = textOf("config-verdict")
        assertTrue(
            "a refused save renders \"$verdict\", which carries the refusal in its colour alone",
            verdict.contains("Not saved", ignoreCase = true),
        )
    }

    // --- AC14: operable without a pointer -------------------------------------------------------

    @Test
    fun AC14_operable_without_a_pointer() {
        UiHarness.launch()
        operableWithoutAPointer()
    }

    @Test
    fun AC14_operable_without_a_pointer_demonstration() {
        UiHarness.launch(UiMutation.SAVE_NOT_FOCUSABLE)
        assertFails("an unreachable save control was not caught") { operableWithoutAPointer() }
    }

    private fun operableWithoutAPointer() {
        compose.waitForIdle()

        // The server configuration is entered and saved with no touch at all: the fields take text
        // from the keyboard, and the save control is reached by directional navigation and activated
        // with the centre key.
        compose.onNodeWithTag("field-url").performTextReplacement("https://tracker.example.org")
        compose.onNodeWithTag("field-token").performTextReplacement("a-device-token")
        compose.waitForIdle()

        val reached = focusByDirection("action-save")
        assertTrue("the save control was not reachable by directional navigation", reached)

        // How the focused control paints, captured while it holds focus.
        val focused = captureOf("action-save")

        // Activated from the keyboard, to the same effect a touch has, while it still holds focus.
        sendKey(KeyEvent.KEYCODE_DPAD_CENTER)
        compose.waitForIdle()
        Thread.sleep(500)

        val verdict = textOf("config-verdict")
        assertTrue(
            "saving from the keyboard produced \"$verdict\"; it must have the same effect a touch has",
            verdict.contains("Saved", ignoreCase = true) || verdict.contains("Not saved", ignoreCase = true),
        )

        // A focus indicator is a RENDERED thing, and it is measured ON THE CONTROL.
        //
        // Diffing two FULL-SCREEN shots taken either side of the traversal graded the wrong thing:
        // the traversal moves focus through several controls and scrolls the column, so the display
        // differs whatever this control paints and the assertion passed on the scrolling alone.
        // captureToImage clips to the node, and each shot is of the node WHEREVER IT THEN IS, so
        // neither scrolling nor the verdict line appearing can contribute a differing pixel.
        moveFocusAwayFrom("action-save")
        val unfocused = captureOf("action-save")
        assertTrue(
            "focusing the save control changed not one pixel OF THAT CONTROL, so it paints no focus " +
                "indicator - whatever else on the screen moved",
            pixelsDiffer(focused, unfocused),
        )
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
        // Everything else still draws, and still works.
        compose.onNodeWithTag("card-permissions").assertIsDisplayed()
        compose.onNodeWithTag("card-server").assertIsDisplayed()
        compose.onNodeWithTag("action-collection").assertIsDisplayed()
        compose.onNodeWithTag("counter-delivered-value").assertIsDisplayed()
        compose.onNodeWithTag("action-save").performClick()
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

    private fun focusByDirection(tag: String): Boolean {
        // Start from the top of the screen, then walk down with the directional pad, exactly as a
        // person with a keyboard or a d-pad would.
        sendKey(KeyEvent.KEYCODE_DPAD_DOWN)
        for (i in 0 until 30) {
            compose.waitForIdle()
            if (isFocused(tag)) return true
            sendKey(KeyEvent.KEYCODE_DPAD_DOWN)
        }
        return false
    }

    private fun isFocused(tag: String): Boolean =
        compose.onAllNodesWithTag(tag, useUnmergedTree = true).fetchSemanticsNodes()
            .any { it.config.getOrNull(SemanticsProperties.Focused) == true }

    /** The pixels of one control, wherever it currently sits, rather than of the whole display. */
    private fun captureOf(tag: String): android.graphics.Bitmap =
        compose.onNodeWithTag(tag).captureToImage().asAndroidBitmap()

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
        throw AssertionError("focus could not be moved off $tag, so its unfocused paint cannot be read")
    }

    private fun sendKey(code: Int) {
        InstrumentationRegistry.getInstrumentation().sendKeyDownUpSync(code)
        Thread.sleep(120)
    }

    private fun pixelsDiffer(a: android.graphics.Bitmap, b: android.graphics.Bitmap): Boolean {
        if (a.width != b.width || a.height != b.height) return true
        var differing = 0
        var y = 0
        while (y < a.height) {
            var x = 0
            while (x < a.width) {
                if (a.getPixel(x, y) != b.getPixel(x, y)) {
                    differing++
                    if (differing > 50) return true
                }
                x += 2
            }
            y += 2
        }
        return differing > 50
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

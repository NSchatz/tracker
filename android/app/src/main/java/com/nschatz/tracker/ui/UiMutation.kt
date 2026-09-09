package com.nschatz.tracker.ui

import android.content.Intent
import com.nschatz.tracker.BuildConfig

/**
 * A surface broken in exactly one way, so the instrumented suite can be shown going RED.
 *
 * The frontend conventions' F2 says a rendered claim is graded by the runtime that draws it. AC18
 * adds the half that makes that worth anything: every assertion must also be demonstrated FAILING
 * against a surface mutated to break exactly the claim it measures. An accessibility assertion that
 * stopped matching anything would otherwise pass over an empty set forever, and nothing in a green
 * run would say so.
 *
 * **One mutation per claim.** A mutation that breaks two claims at once cannot say which check is
 * blind: a sweep that reports nothing has to be diagnosed one defect at a time, and a green run
 * against a doubly-broken surface names neither. So the four claims the accessibility sweep makes -
 * contrast, touch target size, a non-empty spoken name, and no state carried by colour alone - have
 * four separate mutations, and the two halves of operability without a pointer have two more.
 *
 * On Android there is no equivalent of rewriting the served bytes, so the mutation is a switch the
 * app itself honours - and it is honoured **only in a debug build**. [select] reads the debug flag
 * FIRST and returns [NONE] for any name whatever in a release build, so the shipped app has no path
 * to any of these states. This is the same shape as the map page's `window.trackerMap` hook: a
 * verification seam that changes nothing about what production does.
 */
enum class UiMutation {
    /** Ship the real screen. */
    NONE,

    /** Put the explanatory paragraph back on the surface instead of the short label. */
    PARAGRAPHS_ON_SURFACE,

    /** Remove the collection card's explanation affordance. */
    NO_EXPLANATION_AFFORDANCE,

    /**
     * Paint ONE text below the contrast floor, and change nothing else.
     *
     * The collection card's running/stopped word, drawn at a grey that measures about 1.7:1 against
     * the card behind it where WCAG 2.2 AA asks for 4.5:1. Its control keeps its name and its
     * target, so a run that stays green against this names the contrast check and nothing else.
     */
    CONTRAST_BELOW_FLOOR,

    /**
     * Shrink ONE touch target below the floor, and change nothing else.
     *
     * The start/stop control drawn as a 20dp clickable box, with the platform's minimum touch target
     * taken away for that one control. Both halves are needed, and finding out why is most of what
     * impl-gate finding F21 was hiding:
     *
     *  - a Material `Button` applies `minimumInteractiveComponentSize()`, which restores a 48dp
     *    touch target around a 20dp visual, so `Modifier.size(20.dp)` on one is not an undersized
     *    target at all;
     *  - and Compose reports a node's TOUCH bounds to the accessibility layer, expanding any small
     *    target to `ViewConfiguration.minimumTouchTargetSize` (48dp) for pointer input. So even a
     *    plain 20dp clickable still HAS a 48dp target, and a sweep reading those bounds - which is
     *    what the platform's own touch-target check reads - was telling the truth when it reported
     *    no defect. Success criterion 2.5.8 is about the target, not about the paint.
     */
    TARGET_BELOW_FLOOR,

    /**
     * Leave ONE control with no speakable name, and change nothing else.
     *
     * The start/stop control with its label removed: same size, same colours, nothing for a screen
     * reader to announce.
     */
    CONTROL_WITHOUT_A_NAME,

    /** Drop the word that carries a warning, leaving only its colour. */
    WARNING_BY_COLOUR_ONLY,

    /** Take exactly the save control out of the directional-navigation order. */
    SAVE_NOT_FOCUSABLE,

    /**
     * Let the save control be reached and activated, and take away exactly its EFFECT.
     *
     * The control still draws, still takes focus, still activates from the centre key and still puts
     * a verdict on the glass - what it no longer does is save. This is the demonstration for the
     * third of AC14's SHALLs, "saved to the same effect a touch has, with the effect asserted on
     * what the screen then shows", and it exists because impl-gate finding F1 found that clause
     * graded by a predicate which accepted the refusal verdict as readily as the saved one. An
     * assertion that cannot tell a save from a refusal cannot go red against this.
     */
    SAVE_WITHOUT_EFFECT,

    /**
     * Suppress exactly the focus indicator, and change nothing else.
     *
     * Every control still takes focus and still activates; the ring that says WHICH control has it
     * is never painted. This is the half of AC14 the F20 traversal failure aborted before reaching,
     * so it has never been exercised against a rendering.
     */
    FOCUS_INDICATOR_SUPPRESSED,

    /** Print the counters with no statement of the set they were counted over. */
    COUNTERS_WITHOUT_THEIR_SET,

    /** Render a never-measured counter as `0`. */
    NOT_RECORDED_AS_ZERO,

    /** Let an unreadable queue blank the whole card. */
    UNREADABLE_QUEUE_BLANKS_CARD,

    /** Render every card state with the same words. */
    STATES_INDISTINGUISHABLE,

    /** Keep saying the counters are current after collection has stopped. */
    ALWAYS_CURRENT,

    /** Lay the screen out wider than the display, so its content is pushed off and clipped. */
    OVERFLOWING_LAYOUT,

    /**
     * Draw the domain's whole SENTENCE where the surface should draw the few words that name it -
     * the refused-save verdict and the collection warning both.
     *
     * This reproduces impl-gate finding F8 behind a debug-only switch, so the brevity assertion can
     * be shown going red against a state that is not on the screen at launch. A brevity floor that
     * only ever measures the screen as it looks when it opens is exactly the hole F8 named.
     */
    PROSE_IN_A_DEGRADED_STATE,
    ;

    companion object {
        /** The intent extra the instrumented suite sets. Debug builds only. */
        const val EXTRA = "com.nschatz.tracker.ui.MUTATION"

        /**
         * The ONLY way a mutation is ever selected, and the only reader of the debug flag.
         *
         * Pure, so the release behaviour is provable off a device: `select(debug = false, ...)` is
         * [NONE] for every name this enum has and for every name it does not. `from` is the thin
         * Android edge that supplies the two arguments and decides nothing.
         */
        fun select(debug: Boolean, name: String?): UiMutation {
            if (!debug) return NONE
            if (name == null) return NONE
            return entries.firstOrNull { it.name == name } ?: NONE
        }

        fun from(intent: Intent?): UiMutation = select(BuildConfig.DEBUG, intent?.getStringExtra(EXTRA))
    }
}

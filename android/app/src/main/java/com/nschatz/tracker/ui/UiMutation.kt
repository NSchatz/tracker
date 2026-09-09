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
 * On Android there is no equivalent of rewriting the served bytes, so the mutation is a switch the
 * app itself honours — and it is honoured **only in a debug build**. [from] reads
 * [BuildConfig.DEBUG] first and returns [NONE] in a release build whatever the intent says, so the
 * shipped app has no path to any of these states. This is the same shape as the map page's
 * `window.trackerMap` hook: a verification seam that changes nothing about what production does.
 */
enum class UiMutation {
    /** Ship the real screen. */
    NONE,

    /** Put the explanatory paragraph back on the surface instead of the short label. */
    PARAGRAPHS_ON_SURFACE,

    /** Remove the collection card's explanation affordance. */
    NO_EXPLANATION_AFFORDANCE,

    /** Paint the collection status at a contrast the platform checks reject. */
    LOW_CONTRAST_STATUS,

    /** Drop the word that carries a warning, leaving only its colour. */
    WARNING_BY_COLOUR_ONLY,

    /** Make the save control unreachable by directional navigation. */
    SAVE_NOT_FOCUSABLE,

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

        fun from(intent: Intent?): UiMutation {
            if (!BuildConfig.DEBUG) return NONE
            val name = intent?.getStringExtra(EXTRA) ?: return NONE
            return entries.firstOrNull { it.name == name } ?: NONE
        }
    }
}

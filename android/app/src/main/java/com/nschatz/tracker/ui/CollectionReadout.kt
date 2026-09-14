package com.nschatz.tracker.ui

import com.nschatz.tracker.collect.CollectionPolicy
import com.nschatz.tracker.collect.CollectionStatus

/**
 * What the collection card SAYS, decided in one pure place.
 *
 * Two of the frontend conventions are impossible to keep honest if this decision is spread across a
 * composable. F3 ("absence is not zero") needs "nobody counted" and "we counted none" to be two
 * different values all the way to the screen, and F7 ("three states, all of them") needs exactly one
 * of loading / nothing-yet / cannot-read to be showing at any instant — which is a property of a
 * single computed value, not a coincidence of three independent `if` blocks.
 *
 * F6 lives here too. The counters read as CURRENT only while collection is running and something has
 * actually happened inside its own reporting interval; otherwise they read as last known at a stated
 * time. A frozen readout presented as a live one is the defect that clause exists to prevent, and it
 * is invisible precisely because a stopped app looks exactly like a running one.
 */

/** One figure, and the reason it looks the way it does. */
sealed interface Figure {
    /** A real measurement. Zero is a measurement. */
    data class Measured(val value: Int) : Figure

    /** Nothing has ever been counted. Rendered in words, never as `0` and never as a dash. */
    data object NotRecorded : Figure

    /** The measurement was attempted and could not be made. Costs this figure and nothing else. */
    data object Unavailable : Figure
}

/** The one state the collection card is in. */
enum class CollectionCardState {
    /** Before the first status read has happened at all. */
    UNKNOWN,

    /** Read successfully, and there is nothing yet to report. */
    NOTHING_YET,

    /** The queue (or the configuration behind it) could not be read. */
    UNREADABLE,

    /** There are figures to show. */
    REPORTING,
}

/** Everything the collection card renders, as values rather than as branches. */
data class CollectionReadout(
    val state: CollectionCardState,
    val running: Boolean,
    /**
     * True when a person asked for collection and it is not running.
     *
     * The card needs this as its own value rather than as two booleans a composable happens to read
     * together, because the honest rendering of the pair is not the conjunction of their separate
     * renderings. `running` false draws "Stopped", which is the whole truth when nobody asked for
     * collection and only half of it when somebody did: a phone that was asked to collect and is not
     * collecting looks, on a card that says only "Stopped", exactly like a phone somebody switched
     * off. That is the state a reboot leaves behind when the restart could not happen, and it is the
     * one a reader most needs told.
     *
     * It is deliberately NOT a third value of `running`. Whether the service is running is a fact
     * about the process and it stays a boolean; this is the fact that a stored ask disagrees with it.
     */
    val enabledButNotRunning: Boolean,
    val delivered: Figure,
    val queued: Figure,
    val dropped: Figure,
    /** True while the figures describe now; false when they are the last known ones. */
    val current: Boolean,
    /** When the figures were last measured, or null if never. */
    val asOfMillis: Long?,
    /** The start of the set the run-scoped figures were counted over, or null if no run. */
    val fromMillis: Long?,
) {
    companion object {

        /**
         * The window inside which a run with nothing new to say is still describing NOW.
         *
         * The app's own reporting interval, which is the number the screen is implicitly promising
         * when it shows a counter without qualification: if a fix was due a minute ago and none
         * came, what is on the screen is the last thing that was true, not the current thing.
         */
        const val FRESH_WINDOW_MILLIS: Long = CollectionPolicy.DEFAULT_INTERVAL_MILLIS

        /**
         * @param collectionEnabled the persisted ask, from `ClientPreferences.collectionEnabled`.
         *   Passed in rather than read here, so this stays a pure function of plain data and the one
         *   `SharedPreferences` read lives at the framework edge with the others.
         */
        fun of(status: CollectionStatus, now: Long, collectionEnabled: Boolean): CollectionReadout {
            val queued: Figure = when {
                status.readState == CollectionStatus.ReadState.UNREADABLE -> Figure.Unavailable
                status.queued == null -> Figure.NotRecorded
                else -> Figure.Measured(status.queued!!)
            }
            val delivered = figureOf(status.delivered)
            val dropped = figureOf(status.dropped)

            val state = when {
                status.readState == CollectionStatus.ReadState.UNKNOWN -> CollectionCardState.UNKNOWN
                status.readState == CollectionStatus.ReadState.UNREADABLE -> CollectionCardState.UNREADABLE
                delivered == Figure.NotRecorded && dropped == Figure.NotRecorded &&
                    queued == Figure.Measured(0) && status.lastFixAtMillis == 0L ->
                    CollectionCardState.NOTHING_YET

                else -> CollectionCardState.REPORTING
            }

            // The reference instant for freshness is the most recent thing that HAPPENED: a fix if
            // there has been one, otherwise the start of the run. A run that started ten seconds ago
            // and has no fix yet is still describing now; one that started an hour ago and has none
            // is describing an hour ago, whatever its counters say.
            val reference = maxOfOrNull(status.lastFixAtMillis.takeIf { it != 0L }, status.runStartedAtMillis)
            val current = status.running && reference != null && now - reference <= FRESH_WINDOW_MILLIS

            return CollectionReadout(
                state = state,
                running = status.running,
                enabledButNotRunning = collectionEnabled && !status.running,
                delivered = delivered,
                queued = queued,
                dropped = dropped,
                current = current,
                asOfMillis = status.countersAsOfMillis,
                fromMillis = status.runStartedAtMillis,
            )
        }

        private fun figureOf(value: Int?): Figure =
            if (value == null) Figure.NotRecorded else Figure.Measured(value)

        private fun maxOfOrNull(a: Long?, b: Long?): Long? = when {
            a == null -> b
            b == null -> a
            else -> maxOf(a, b)
        }
    }
}

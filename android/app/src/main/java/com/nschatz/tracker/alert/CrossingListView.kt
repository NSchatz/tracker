package com.nschatz.tracker.alert

/**
 * What the in-app crossing list should show right now, decided in one pure place.
 *
 * The list has FIVE outcomes a person must be able to tell apart, and the whole reason this is a
 * function rather than a few `if`s inside a composable is that four of them are failures which look
 * identical if you get it wrong:
 *
 *  - the family genuinely has no crossings;
 *  - the viewer credential was rejected;
 *  - the server could not be reached;
 *  - the server answered something unusable;
 *  - the read has not come back yet.
 *
 * Presenting any of the last four as "no crossings" is the failure this list exists to prevent. A
 * parent opening the app to check whether a child reached school must never be shown an empty list
 * because the wifi was on a captive portal.
 */
internal enum class CrossingListState {
    /** No read has resolved yet and nothing has arrived by push. */
    CHECKING,

    /** The read succeeded and there is genuinely nothing to show. */
    EMPTY,

    /** The read was refused as unauthorized: the viewer credential was rejected. */
    CREDENTIAL_REJECTED,

    /** The read could not reach the server. */
    SERVER_UNREACHABLE,

    /** The server answered something this client could not use. */
    SERVER_ERROR,

    /** There are crossings to show, and the last read succeeded. */
    SHOWING,
}

/**
 * The list, and what to say about it.
 *
 * @param rows the merged crossings, newest first. NON-EMPTY even in a failure state when pushes have
 *   arrived: a failed read never hides a crossing this phone already has.
 * @param state which of the five outcomes applies.
 * @param detail the server's or the network's own words for a failure, for the diagnostic line.
 *   Never carries a credential.
 */
internal data class CrossingListView(
    val rows: List<Crossing>,
    val state: CrossingListState,
    val detail: String? = null,
) {
    companion object {
        /**
         * Decides the view.
         *
         * @param read the last crossings read, or null when none has resolved.
         * @param pushed the crossings that arrived on this phone as pushes.
         */
        fun of(read: AlertClient.CrossingsResult?, pushed: List<Crossing>): CrossingListView {
            val fromRoute = (read as? AlertClient.CrossingsResult.Ok)?.crossings.orEmpty()
            val rows = CrossingMerge.merge(fromRoute = fromRoute, fromPush = pushed)

            return when (read) {
                null -> CrossingListView(
                    rows = rows,
                    state = if (rows.isEmpty()) CrossingListState.CHECKING else CrossingListState.SHOWING,
                )

                is AlertClient.CrossingsResult.Ok -> CrossingListView(
                    rows = rows,
                    state = if (rows.isEmpty()) CrossingListState.EMPTY else CrossingListState.SHOWING,
                )

                AlertClient.CrossingsResult.Unauthorized -> CrossingListView(
                    rows = rows,
                    state = CrossingListState.CREDENTIAL_REJECTED,
                )

                is AlertClient.CrossingsResult.Unreachable -> CrossingListView(
                    rows = rows,
                    state = CrossingListState.SERVER_UNREACHABLE,
                    detail = read.detail,
                )

                is AlertClient.CrossingsResult.ServerError -> CrossingListView(
                    rows = rows,
                    state = CrossingListState.SERVER_ERROR,
                    detail = read.detail,
                )
            }
        }
    }
}

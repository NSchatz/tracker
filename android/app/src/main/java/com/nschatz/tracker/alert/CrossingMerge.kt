package com.nschatz.tracker.alert

/**
 * Merges the two sources a crossing can reach this app by into one list.
 *
 * A crossing arrives as a **push** and is also returned by the **crossings route**, and a person
 * must see it once. The route is not a duplicate of the push, though: it is the fail-safe FOR the
 * push, because the backend stores only four collapsible messages per phone and then discards, so
 * the route is the only place a dropped alert can still be found. Both sources therefore have to
 * feed the same list.
 *
 * ### The merge key, and the key that is forbidden
 *
 * Crossings are identified by (device, Place, **instant**). The instant is load-bearing: the two
 * sources state the same one in different formats (a UTC calendar timestamp in a push, epoch seconds
 * on the route), which is why [Crossing] normalises both to epoch seconds before they ever get here.
 *
 * A key of (device, Place) alone is forbidden and this file is where that is enforced. It cannot
 * separate two crossings of one device at one Place at different instants - leaving for school in
 * the morning and coming home in the afternoon - so it would silently swallow the second one, out of
 * the very list that exists to catch what got lost.
 */
internal object CrossingMerge {

    /**
     * Merges, newest first.
     *
     * @param fromRoute crossings read back from `GET /v1/geofence-events`.
     * @param fromPush crossings that arrived as pushes on this phone.
     *
     * Where both sources hold the same crossing, the ROUTE's copy wins. It is the server's own
     * record rather than a message that transited a third party, and it is the one that carries the
     * device name as a field rather than as a notification title - so preferring it is also what
     * keeps a name from flickering between the two renderings of the same row.
     */
    fun merge(fromRoute: List<Crossing>, fromPush: List<Crossing>): List<Crossing> {
        val byIdentity = LinkedHashMap<CrossingIdentity, Crossing>()
        // Pushes first, so a route row with the same identity overwrites one.
        for (c in fromPush) byIdentity[c.identity] = c
        for (c in fromRoute) byIdentity[c.identity] = c
        return byIdentity.values.sortedWith(
            compareByDescending<Crossing> { it.instantSeconds }
                .thenBy { it.deviceId }
                .thenBy { it.placeId },
        )
    }
}

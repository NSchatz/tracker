package com.nschatz.tracker.collect

/**
 * The numbers one request to the fused provider is built from.
 *
 * Plain data with no Android type in it, so what the service *asks the provider for* is assertable
 * by the gate. `LocationRequest` is a framework class; a test that built one would be testing Play
 * services. A test that reads these four numbers is testing the decision this app actually makes.
 *
 * There are no `require`s here on purpose. [CollectionPolicy] is the thing with an opinion about
 * coherent cadence and it keeps every one of them; this is the translation of a policy into a
 * request, and the restart request deliberately uses values (a zero displacement filter, no
 * batching) that the steady-state policy would be wrong to allow.
 */
data class LocationRequestSpec(
    val intervalMillis: Long,
    val minUpdateIntervalMillis: Long,
    val minUpdateDistanceMeters: Float,
    val maxUpdateDelayMillis: Long,
)

/**
 * The two requests this client makes of the fused provider, and the boundary between them.
 *
 * Collection runs on **two** requests at a transition, not one, and that is what keeps the
 * displacement filter frozen while still delivering one exempt position:
 *
 * - [steadyState] is the collection stream. It carries [CollectionPolicy]'s numbers unchanged - the
 *   60 s target and the 25 m displacement filter - and it is the only stream running once the
 *   restart position has been taken.
 * - [restartPosition] is the exemption, and it is removed the instant it yields one accepted
 *   position ([FilterExemption]). Its displacement filter is zero, which is the exemption itself:
 *   it is what lets a phone that has not moved a centimetre report that it is alive.
 *
 * Two requests rather than one mutable one because the alternative - relaxing the steady-state
 * filter for a while and then putting it back - is a build in which the 25 m filter is a thing that
 * changes. It would have to be changed back correctly on every path, including the ones where
 * collection stops before a position arrives, and a missed one is a phone reporting continuously
 * with no filter at all. Here the frozen stream is never touched.
 */
object CollectionRequests {

    /**
     * The continuous collection stream: exactly the pin's cadence and displacement filter.
     *
     * Nothing in the reboot work is allowed to move these, and the gate asserts it.
     */
    fun steadyState(policy: CollectionPolicy = CollectionPolicy()): LocationRequestSpec =
        LocationRequestSpec(
            intervalMillis = policy.intervalMillis,
            minUpdateIntervalMillis = policy.minUpdateIntervalMillis,
            minUpdateDistanceMeters = policy.minUpdateDistanceMeters,
            maxUpdateDelayMillis = policy.maxUpdateDelayMillis,
        )

    /**
     * The one-shot request that produces the restart position.
     *
     * - `minUpdateDistanceMeters = 0` is the exemption, and it is the only difference that matters.
     * - `minUpdateIntervalMillis = 0` accepts the first position the provider produces whenever it
     *   produces it, rather than making the phone wait out a floor for a fix it already has.
     * - `maxUpdateDelayMillis = 0` refuses batching for this one request. Batching is a battery
     *   trade that buys nothing here: the request is removed after one position, and up to two
     *   minutes of deferral on the single position whose job is to show the device is alive *now*
     *   would spend the freshness this exists to prove.
     * - The target interval is left at the policy's, untouched, so that a request which somehow
     *   outlives its removal cannot become a faster cadence.
     */
    fun restartPosition(policy: CollectionPolicy = CollectionPolicy()): LocationRequestSpec =
        LocationRequestSpec(
            intervalMillis = policy.intervalMillis,
            minUpdateIntervalMillis = 0L,
            minUpdateDistanceMeters = 0f,
            maxUpdateDelayMillis = 0L,
        )
}

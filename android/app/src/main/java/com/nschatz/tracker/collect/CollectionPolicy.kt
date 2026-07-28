package com.nschatz.tracker.collect

/**
 * How often, and how precisely, the foreground service asks the fused provider for a location.
 *
 * These four numbers are the whole battery/freshness trade, and C1 sets them **conservatively and
 * statically**. Making them adaptive — slower when stationary, faster when driving — is **C4**, and
 * doing it here would mean shipping an untuned heuristic in the phase whose job is to prove the
 * collection path works at all.
 *
 * @param intervalMillis the cadence the app *wants*. The fused provider treats this as a target,
 *   not a contract: it batches, it defers under Doze, and OEM battery managers throttle it further.
 *   A fix arriving late is normal; the server timestamps `received_at` separately from the device's
 *   `ts` precisely so lateness is measurable rather than invisible.
 * @param minUpdateIntervalMillis the fastest the app will *accept* updates. The provider may deliver
 *   faster than [intervalMillis] when another app has already asked for a faster stream — the fix is
 *   free at that point, but reporting every one of them is not, so this floor bounds how much of
 *   someone else's cadence tracker inherits.
 * @param minUpdateDistanceMeters suppress updates that have not moved this far. This is the single
 *   biggest battery lever available without adaptive logic: a phone on a bedside table generates no
 *   traffic at all overnight. It is deliberately smaller than typical urban GPS error would make
 *   "safe", because a fix that is suppressed is a gap in someone's trail.
 * @param maxUpdateDelayMillis how long the provider may batch fixes before delivering them. Batching
 *   saves power by letting the radio and CPU stay asleep between wakeups, at the cost of freshness.
 */
data class CollectionPolicy(
    val intervalMillis: Long = DEFAULT_INTERVAL_MILLIS,
    val minUpdateIntervalMillis: Long = DEFAULT_MIN_UPDATE_INTERVAL_MILLIS,
    val minUpdateDistanceMeters: Float = DEFAULT_MIN_DISTANCE_METERS,
    val maxUpdateDelayMillis: Long = DEFAULT_MAX_UPDATE_DELAY_MILLIS,
) {
    init {
        require(intervalMillis > 0) { "intervalMillis must be positive" }
        require(minUpdateIntervalMillis > 0) { "minUpdateIntervalMillis must be positive" }
        // A floor slower than the target is not a floor, it is a second, contradictory target. The
        // provider's behaviour when the two disagree is unspecified, so refuse the configuration
        // rather than ship a cadence nobody can predict.
        require(minUpdateIntervalMillis <= intervalMillis) {
            "minUpdateIntervalMillis ($minUpdateIntervalMillis) must not exceed intervalMillis ($intervalMillis)"
        }
        require(minUpdateDistanceMeters >= 0f) { "minUpdateDistanceMeters must not be negative" }
        require(maxUpdateDelayMillis >= 0) { "maxUpdateDelayMillis must not be negative" }
    }

    companion object {
        /** One minute — frequent enough for a family map, slow enough not to dominate the battery. */
        const val DEFAULT_INTERVAL_MILLIS: Long = 60_000

        /** Accept a fix at most every 30 s, even when another app is driving a faster stream. */
        const val DEFAULT_MIN_UPDATE_INTERVAL_MILLIS: Long = 30_000

        /**
         * 25 m. Above typical good-GPS error (so a stationary phone stops reporting) and below the
         * scale at which a real move matters (so leaving the house is never missed).
         */
        const val DEFAULT_MIN_DISTANCE_METERS: Float = 25f

        /** Allow two minutes of batching. Freshness is bounded by the server's `received_at`. */
        const val DEFAULT_MAX_UPDATE_DELAY_MILLIS: Long = 120_000
    }
}

package com.nschatz.tracker.protocol

/**
 * The raw readings one platform location carries, lifted out of `android.location.Location` into
 * plain data.
 *
 * This indirection exists for one reason: `android.location.Location` is a **framework class**, and
 * in a JVM unit test every one of its methods throws "not mocked". Building a [Fix] straight from a
 * `Location` would have put the whole mapping — the millis→seconds conversion, the
 * `hasAccuracy()` guard, the float→double widening — behind a device boundary where the gate cannot
 * see it. Reading the fields at the framework edge (`LocationSample.from`, in the `collect`
 * package) and mapping *these* is what makes the mapping provable headlessly.
 *
 * Accuracy and speed are nullable here rather than being paired with `hasAccuracy()` /`hasSpeed()`
 * booleans: `Location` reports `0.0` for an *unset* accuracy, and 0 m of accuracy is a claim of
 * perfect precision, not an absence. Collapsing "unset" to null at the edge is what stops that zero
 * from being fabricated onto the wire.
 *
 * @param elapsedMillis the fix's own timestamp in **milliseconds** since the epoch, on the device
 *   clock (`Location.getTime()`).
 */
data class LocationReading(
    val lat: Double,
    val lon: Double,
    val elapsedMillis: Long,
    val accuracyM: Double? = null,
    val speedMps: Double? = null,
)

/**
 * Builds validated [Fix] payloads from raw location readings.
 *
 * Pure and deterministic: everything that varies — the clock, the battery level, the message id —
 * is a parameter, never read from the platform inside. That is what lets the gate assert the exact
 * bytes this produces.
 */
object FixFactory {

    /**
     * Converts a reading into the fix that will go on the wire.
     *
     * Returns a [FixRejection] instead of a [Fix] when the reading could not produce a valid report
     * — a fused provider under a spoofed mock provider, or a stale cached fix, or a clock that
     * jumped mid-session, can each yield one. Note what it does **not** catch: a device whose clock
     * is simply wrong by days drifts `nowEpochSeconds` and `location.time` together, so the window
     * check passes here and the mismatch is only visible to the SERVER, which compares against its
     * own clock. That is the correct division of labour — the server's window is the authoritative
     * one — but it means this check is a fast local filter, not a clock validator.
     * **A rejected reading is dropped, never rounded into range**: clamping a
     * latitude of 122 to 90 would put the family somewhere they have never been, which is the
     * confident-wrong-answer failure this project fears most (roadmap §4, risk path #2).
     *
     * @param nowEpochSeconds current time, for the ingest-window check. Injected, not read.
     * @param batteryPct battery level 0..100, or null when the platform could not report one.
     * @param trigger what prompted this report (see [FixTrigger]).
     * @param msgId a client-generated correlator for logs. Secondary only — the server's identity
     *   for a fix is `(device_id, ts)`, so this never affects dedup.
     */
    fun build(
        reading: LocationReading,
        nowEpochSeconds: Long,
        batteryPct: Int? = null,
        trigger: String? = null,
        msgId: String? = null,
    ): FixResult {
        val fix = Fix(
            lat = reading.lat,
            lon = reading.lon,
            // Location.getTime() is MILLISECONDS; `ts` is SECONDS (SPEC.md). floorDiv, not `/`,
            // so a pre-epoch timestamp truncates toward the past rather than toward zero — the
            // difference only shows up before 1970, but a silent off-by-one-second in a time
            // conversion is exactly the kind of bug that is invisible until it is a wrong trail.
            tsEpochSeconds = Math.floorDiv(reading.elapsedMillis, 1000L),
            accuracyM = reading.accuracyM,
            batteryPct = batteryPct,
            speedMps = reading.speedMps,
            trigger = trigger,
            msgId = msgId,
        )
        val rejection = FixValidation.validate(fix, nowEpochSeconds)
        return if (rejection != null) FixResult.Refused(rejection) else FixResult.Valid(fix)
    }
}

/** Either a fix worth sending, or the typed reason it will never be sent. */
sealed interface FixResult {
    data class Valid(val fix: Fix) : FixResult
    data class Refused(val rejection: FixRejection) : FixResult
}

/**
 * The `trigger` values this client sends. Free-form on the wire (`SPEC.md`), but a closed set here
 * so the server-side history is greppable and two phases cannot invent two spellings of the same
 * event.
 */
object FixTrigger {
    /**
     * A periodic update from the foreground service's continuous stream.
     *
     * The only value C1 sends. Further values (a first fix, a user-requested report) belong to the
     * phases that actually emit them — declaring them now would put unused strings in the server's
     * history vocabulary that nothing on either side has ever produced.
     */
    const val PERIODIC = "periodic"
}

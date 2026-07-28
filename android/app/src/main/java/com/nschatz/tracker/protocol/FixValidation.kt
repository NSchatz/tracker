package com.nschatz.tracker.protocol

/**
 * Why a fix was refused. The categories mirror the server's typed errors (`SPEC.md`, "Strictness &
 * errors") so a client-side refusal and a server-side `400` mean the same thing and can be reported
 * to the user in the same words.
 */
enum class FixRejectionKind {
    /** Longitude outside ±180, latitude outside ±90, or a non-finite coordinate. */
    COORDINATE_OUT_OF_RANGE,

    /** `ts` outside the server's accepted ingest window. */
    TIMESTAMP_OUT_OF_WINDOW,

    /** An optional metric (accuracy / battery / speed) outside its documented range. */
    METRIC_OUT_OF_RANGE,
}

/** A refused fix, with the reason in a sentence a human can act on. */
data class FixRejection(val kind: FixRejectionKind, val message: String)

/**
 * Client-side validation of a [Fix], mirroring the server's own checks.
 *
 * ### Why validate here when the server already does
 *
 * Not to replace the server's check — the server's is the authoritative one and must never be
 * removed. This exists because of what happens *without* it: an invalid fix would be serialised,
 * sent over the radio, and answered with a `400 invalid_fix` that the client can only discard.
 * Validating first turns a wasted round-trip on a metered radio into a local refusal, and — the part
 * that matters — makes the refusal **visible on the device** rather than only in a server log the
 * phone's owner will never read.
 *
 * ### The axis-order trap, restated on the client
 *
 * The server-side comment on `store.ValidateLonLat` explains the real hazard: **PostGIS does not
 * reject an out-of-range coordinate, it silently coerces it.** A latitude of 122.33 (the signature
 * of a swapped lon/lat) is folded back into range and stored as a real place in the South Atlantic,
 * with every constraint passing. The server catches that in Go, before any SQL. This check is the
 * same guard one hop earlier, and it is the reason [Fix] names its parameters `lat` and `lon`
 * explicitly and [FixFactory] never takes two bare doubles positionally.
 *
 * A latitude past ±90 is therefore treated as the swap it almost certainly is, and the message says
 * so — latitudes beyond ±90 do not exist, while longitudes beyond ±90 are half the planet.
 */
object FixValidation {

    /**
     * How far ahead of the server clock a `ts` may be, in seconds (24 hours).
     *
     * Mirrors `store.MaxIngestFutureSkew`. A fix from the future is a bad device clock, not a real
     * event.
     */
    const val MAX_FUTURE_SKEW_SECONDS: Long = 24L * 60 * 60

    /**
     * How far into the past a `ts` may be, in seconds (90 days).
     *
     * Mirrors `store.MaxIngestBacklog`. The server's window is finite because it provisions a
     * storage partition per month from the client-supplied `ts`; a fix older than this is refused
     * with a `400` and stored nowhere, so a client that buffered one has to drop it rather than
     * retry it forever. C2's durable queue must honour this bound when it replays a long backlog.
     */
    const val MAX_BACKLOG_SECONDS: Long = 90L * 24 * 60 * 60

    /**
     * Validates [fix] against the server's contract, returning null when it would be accepted.
     *
     * @param nowEpochSeconds the current time, passed in rather than read from the clock so the
     *   window boundaries are testable without waiting a day.
     */
    fun validate(fix: Fix, nowEpochSeconds: Long): FixRejection? =
        validateCoordinate(fix.lon, fix.lat)
            ?: validateTimestamp(fix.tsEpochSeconds, nowEpochSeconds)
            ?: validateMetrics(fix)

    /**
     * Rejects any coordinate the server's `ValidateLonLat` would reject.
     *
     * **Longitude first**, matching the Go function and the `ST_Point(lon, lat, 4326)` call it
     * defends: the argument order of this function is itself the convention it exists to protect.
     */
    fun validateCoordinate(lon: Double, lat: Double): FixRejection? = when {
        !lon.isFinite() || !lat.isFinite() -> FixRejection(
            FixRejectionKind.COORDINATE_OUT_OF_RANGE,
            "lon=$lon lat=$lat is not a finite coordinate",
        )

        lon < -180.0 || lon > 180.0 -> FixRejection(
            FixRejectionKind.COORDINATE_OUT_OF_RANGE,
            "longitude $lon is outside [-180, 180]",
        )

        lat < -90.0 || lat > 90.0 -> FixRejection(
            FixRejectionKind.COORDINATE_OUT_OF_RANGE,
            "latitude $lat is outside [-90, 90] — are lon and lat swapped?",
        )

        else -> null
    }

    /** Rejects a `ts` outside `[now − 90 days, now + 24 hours]`, the server's ingest window. */
    fun validateTimestamp(tsEpochSeconds: Long, nowEpochSeconds: Long): FixRejection? = when {
        tsEpochSeconds > nowEpochSeconds + MAX_FUTURE_SKEW_SECONDS -> FixRejection(
            FixRejectionKind.TIMESTAMP_OUT_OF_WINDOW,
            "fix timestamp is more than 24h in the future — check the device clock",
        )

        tsEpochSeconds < nowEpochSeconds - MAX_BACKLOG_SECONDS -> FixRejection(
            FixRejectionKind.TIMESTAMP_OUT_OF_WINDOW,
            "fix timestamp is more than 90 days old and can no longer be reported",
        )

        else -> null
    }

    /**
     * Rejects out-of-range optional metrics, mirroring the server's `validateOptionalMetrics`.
     *
     * A *missing* metric is always fine — absence is the documented normal case. Only a **present**
     * value out of its range is a refusal.
     */
    fun validateMetrics(fix: Fix): FixRejection? {
        val accuracy = fix.accuracyM
        if (accuracy != null && (!accuracy.isFinite() || accuracy < 0.0)) {
            return FixRejection(
                FixRejectionKind.METRIC_OUT_OF_RANGE,
                "accuracy must be a finite, non-negative number of metres",
            )
        }
        val speed = fix.speedMps
        if (speed != null && (!speed.isFinite() || speed < 0.0)) {
            return FixRejection(
                FixRejectionKind.METRIC_OUT_OF_RANGE,
                "speed must be a finite, non-negative number",
            )
        }
        val battery = fix.batteryPct
        if (battery != null && (battery < 0 || battery > 100)) {
            return FixRejection(
                FixRejectionKind.METRIC_OUT_OF_RANGE,
                "battery must be a percentage between 0 and 100",
            )
        }
        return null
    }
}

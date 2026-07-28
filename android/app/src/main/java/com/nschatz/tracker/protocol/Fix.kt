package com.nschatz.tracker.protocol

/**
 * One position report in tracker's **first-party v1 schema** — the body of `POST /v1/fixes`
 * (`SPEC.md`, roadmap §5.2).
 *
 * This type is deliberately **pure Kotlin**: no `android.location.Location`, no framework class,
 * nothing that needs a device. That is what lets the wire contract — the half of C1 that a headless
 * build genuinely *can* prove — be covered by real JVM unit tests, while the parts that need a
 * device (a live GPS fix, a granted runtime permission) stay honestly untested. See
 * `android/README.md`, "What the gate proves and what it cannot".
 *
 * ### Absent is not zero
 *
 * Every optional field is **nullable**, and [toJsonBody] **omits** a null rather than writing a
 * zero. The server preserves that distinction — an absent `battery` is stored as SQL `NULL`, a
 * `battery` of `0` is a real reading of a flat phone — so fabricating a zero here would invent data
 * the device never measured. `lat`/`lon` of `0,0` is Null Island, a real place; that is exactly why
 * the required fields are non-null primitives and the optional ones are not.
 *
 * ### The server is strict, so this must be exact
 *
 * `/v1/fixes` rejects unknown fields and trailing data with a `400` (`SPEC.md`, "Strictness &
 * errors"), so a typo'd key here is a loud failure, not a silently dropped field. The eight names
 * below are the whole schema; do not add a ninth without a `/v2`.
 *
 * @param lat latitude in degrees, WGS84, −90..90.
 * @param lon longitude in degrees, WGS84, −180..180.
 * @param tsEpochSeconds epoch **seconds** of the fix on the **device** clock. Seconds, not millis —
 *   `Location.getTime()` is millis and must be divided; see [FixFactory].
 * @param accuracyM horizontal accuracy in metres (≥ 0), or null if the fix carried none.
 * @param batteryPct battery level 0..100, or null if unknown.
 * @param speedMps ground speed in **metres per second** (≥ 0), or null. The server's OwnTracks
 *   adapter converts km/h; the first-party schema does not — it is m/s, the same unit
 *   `Location.getSpeed()` reports, so no conversion belongs here.
 * @param trigger free-form note on what prompted the report.
 * @param msgId client-generated correlator. It is a *secondary* correlator only: the server dedups
 *   on `(device_id, ts)`, so a `msg_id` does not and must not change idempotency.
 */
data class Fix(
    val lat: Double,
    val lon: Double,
    val tsEpochSeconds: Long,
    val accuracyM: Double? = null,
    val batteryPct: Int? = null,
    val speedMps: Double? = null,
    val trigger: String? = null,
    val msgId: String? = null,
)

/**
 * Encodes this fix as the JSON body of `POST /v1/fixes`.
 *
 * Hand-rolled rather than delegating to `org.json`, for a specific reason: `org.json` ships in the
 * Android framework, and in a **JVM unit test** every framework method throws
 * "not mocked" by default. Routing the wire format through it would have made the payload
 * untestable without a device — which is precisely the thing C1 must avoid, because the payload is
 * one of the few parts of this phase that a headless gate *can* prove. A small encoder that is
 * itself unit-tested is the honest trade.
 *
 * Required fields are always present; optional fields appear only when non-null.
 */
fun Fix.toJsonBody(): String {
    val sb = StringBuilder(192)
    sb.append('{')
    // Required. Order is cosmetic (JSON objects are unordered) but stable output makes the wire
    // format readable in a log or a test failure.
    sb.append("\"lat\":").append(encodeJsonNumber(lat))
    sb.append(",\"lon\":").append(encodeJsonNumber(lon))
    sb.append(",\"ts\":").append(tsEpochSeconds)
    // Optional — omitted entirely when absent, never emitted as a fabricated zero.
    accuracyM?.let { sb.append(",\"accuracy\":").append(encodeJsonNumber(it)) }
    batteryPct?.let { sb.append(",\"battery\":").append(it) }
    speedMps?.let { sb.append(",\"speed\":").append(encodeJsonNumber(it)) }
    trigger?.let { sb.append(",\"trigger\":").append(encodeJsonString(it)) }
    msgId?.let { sb.append(",\"msg_id\":").append(encodeJsonString(it)) }
    sb.append('}')
    return sb.toString()
}

/**
 * Renders a double as a JSON number.
 *
 * `Double.toString` produces the shortest decimal that round-trips, which is what a coordinate
 * needs — truncating to a fixed number of decimal places would throw away real precision (the
 * seventh decimal place of a degree is about a centimetre).
 *
 * NaN and the infinities have **no JSON representation** — `Double.toString` would emit the bare
 * words `NaN` / `Infinity`, which is not valid JSON and would reach the server as a `400 malformed`.
 * They cannot arrive here: [FixValidation.validate] rejects a non-finite coordinate or metric
 * before anything is encoded, mirroring the server's own `ValidateLonLat`. This throws rather than
 * emitting garbage, so a future caller that skips validation fails loudly instead of putting an
 * unparseable body on the wire.
 */
private fun encodeJsonNumber(value: Double): String {
    require(value.isFinite()) { "a non-finite number ($value) has no JSON representation" }
    return value.toString()
}

/**
 * Quotes and escapes a string per RFC 8259.
 *
 * `trigger` is free-form and `msg_id` is client-generated, so both are strings this code does not
 * control. An unescaped quote or backslash would corrupt the whole body into a `400`; an unescaped
 * control character is outright invalid JSON. Everything below U+0020 is escaped, using the short
 * forms where they exist and `\uXXXX` otherwise.
 */
internal fun encodeJsonString(value: String): String {
    val sb = StringBuilder(value.length + 2)
    sb.append('"')
    for (ch in value) {
        when {
            ch == '"' -> sb.append("\\\"")
            ch == '\\' -> sb.append("\\\\")
            ch == '\n' -> sb.append("\\n")
            ch == '\r' -> sb.append("\\r")
            ch == '\t' -> sb.append("\\t")
            ch == '\b' -> sb.append("\\b")
            ch == '\u000C' -> sb.append("\\f")
            ch < ' ' -> sb.append("\\u").append(String.format("%04x", ch.code))
            else -> sb.append(ch)
        }
    }
    sb.append('"')
    return sb.toString()
}

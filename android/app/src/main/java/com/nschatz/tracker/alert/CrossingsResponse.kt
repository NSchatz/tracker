package com.nschatz.tracker.alert

/**
 * Reads the body of `GET /v1/geofence-events` into [Crossing]s.
 *
 * The wire, from `SPEC.md`: a JSON array, newest first, each row carrying `device_id`, `place_id`,
 * `place_name`, `transition` and `ts` as **epoch seconds** - plus, since this phase, `device_name`.
 * An empty family is `[]`, never null.
 *
 * `device_name` is read as OPTIONAL, and its absence is preserved as null rather than filled in. A
 * server that predates this phase's additive field serves rows without it, and this client must
 * still show those crossings: the Place, the direction and the time are all there, and only the
 * device's name is missing. Inventing one ("Unknown phone" as though the family had chosen it) would
 * be the confident wrong answer; showing a visibly-not-a-name placeholder is not.
 */
internal object CrossingsResponse {

    /** Why a crossings body could not be read. Distinct from an empty family, always. */
    class Unreadable(message: String) : Exception(message)

    /**
     * Parses a crossings body.
     *
     * @throws Unreadable when the body is not a JSON array of crossing rows. A row missing a
     *   REQUIRED field (its ids, its Place name, its direction or its ts) makes the whole response
     *   unreadable rather than being silently skipped: a list that quietly dropped the one crossing
     *   a parent was looking for would be worse than a list that says it could not be read.
     */
    fun parse(body: String): List<Crossing> {
        val root = try {
            MiniJson.parse(body)
        } catch (e: MiniJson.MalformedJson) {
            throw Unreadable("the server's answer was not readable JSON: ${e.message}")
        }
        val rows = root as? List<*> ?: throw Unreadable("the server's answer was not a JSON array")

        return rows.mapIndexed { index, raw ->
            val row = raw as? Map<*, *> ?: throw Unreadable("crossing $index was not a JSON object")
            Crossing(
                deviceId = requiredString(row, "device_id", index),
                // Optional and absence-preserving: null when the server does not carry it, and null
                // when it carries a blank. A blank name is not a name the family chose either.
                deviceName = (row["device_name"] as? String)?.trim()?.takeIf { it.isNotEmpty() },
                placeId = requiredString(row, "place_id", index),
                placeName = requiredString(row, "place_name", index),
                transition = Transition.fromWire(requiredString(row, "transition", index))
                    ?: throw Unreadable("crossing $index has a direction this client does not know"),
                instantSeconds = requiredEpochSeconds(row, index),
            )
        }
    }

    private fun requiredString(row: Map<*, *>, field: String, index: Int): String {
        val value = (row[field] as? String)?.trim()
        if (value.isNullOrEmpty()) throw Unreadable("crossing $index is missing $field")
        return value
    }

    /**
     * Reads `ts` as epoch seconds.
     *
     * It arrives as a JSON number, which [MiniJson] renders as a Double. Epoch seconds are well
     * inside a Double's exact-integer range (2^53 seconds is about 285 million years), so the
     * conversion is lossless for every timestamp this product will ever see - but a value with a
     * fractional part is refused rather than truncated, because the whole point of this field is to
     * be the instant the push and the route agree on.
     */
    private fun requiredEpochSeconds(row: Map<*, *>, index: Int): Long {
        val value = row["ts"] as? Double ?: throw Unreadable("crossing $index is missing ts")
        val whole = value.toLong()
        if (whole.toDouble() != value) throw Unreadable("crossing $index has a ts that is not whole seconds")
        return whole
    }
}

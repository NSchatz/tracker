package com.nschatz.tracker.alert

/**
 * Turns a received push into a [Crossing], or refuses it.
 *
 * ### The wire it reads
 *
 * `SPEC.md`, *What a push contains*: the notification's **title** is the device's name, and the data
 * map is exactly `type`, `device_id`, `place_id`, `place_name`, `transition`, `ts` - with `ts` as a
 * **UTC calendar timestamp** (`2026-07-16T09:30:00Z`), which is a different format from the epoch
 * seconds the crossings route uses for the same instant. The device name is deliberately NOT in the
 * data map; it is the title. So this parser takes both.
 *
 * ### Why it refuses rather than fills in
 *
 * A push that does not carry a complete crossing renders nothing at all. Not a partial notification,
 * not a placeholder name, not "someone arrived somewhere" - the discard is counted and the person is
 * told nothing, because a notification about a child's movements that the app had to invent half of
 * is worse than silence, and the in-app crossing list is there to make the silence recoverable.
 *
 * This is stricter than it strictly must be: it requires the identity fields (`device_id`,
 * `place_id`, `ts`) as well as the four a notification needs (the kind, the device name, the Place
 * name and the direction). That is deliberate. Without the identity the crossing cannot be told
 * apart from the same crossing arriving over the crossings route, so a rendered-but-unidentifiable
 * push would show up twice in the list - and the server sends all six on every push, so a payload
 * missing one is not a legitimate variant, it is something this app cannot fully account for.
 */
object PushPayload {

    /** The `type` every geofence push carries. Anything else is not a crossing. */
    const val TYPE_GEOFENCE: String = "geofence"

    /**
     * Parses a received push.
     *
     * @param title the notification title as the backend delivered it - the device's name.
     * @param data the push's data map.
     * @return the crossing, or null when the payload is incomplete. A null result is the caller's
     *   cue to count a discard and render nothing.
     */
    fun parse(title: String?, data: Map<String, String?>): Crossing? {
        if (data["type"] != TYPE_GEOFENCE) return null

        val deviceName = title?.trim().orEmpty()
        val deviceId = data["device_id"]?.trim().orEmpty()
        val placeId = data["place_id"]?.trim().orEmpty()
        val placeName = data["place_name"]?.trim().orEmpty()
        val transition = Transition.fromWire(data["transition"]?.trim()) ?: return null
        val instant = parseUtcCalendarSeconds(data["ts"]?.trim()) ?: return null

        if (deviceName.isEmpty() || deviceId.isEmpty() || placeId.isEmpty() || placeName.isEmpty()) return null

        return Crossing(
            deviceId = deviceId,
            deviceName = deviceName,
            placeId = placeId,
            placeName = placeName,
            transition = transition,
            instantSeconds = instant,
        )
    }

    /**
     * Parses the server's `2006-01-02T15:04:05Z` UTC calendar timestamp into epoch seconds.
     *
     * Hand-rolled rather than `java.time`: `java.time` is API 26 and this module's `minSdk` is 29, so
     * it would be available - but `DateTimeFormatter` on a fixed, always-UTC, always-second-precision
     * format is more machinery than the format needs, and the arithmetic below is exactly testable.
     * The one thing that matters is that it agrees, to the second, with the epoch seconds the
     * crossings route states for the same crossing; the tests assert exactly that.
     *
     * Anything that is not that exact shape is refused (null), never coerced. A timestamp the app
     * guessed at would silently split one crossing into two rows in the list.
     */
    internal fun parseUtcCalendarSeconds(value: String?): Long? {
        if (value == null || value.length != 20) return null
        if (value[4] != '-' || value[7] != '-' || value[10] != 'T' ||
            value[13] != ':' || value[16] != ':' || value[19] != 'Z'
        ) {
            return null
        }
        val year = value.substring(0, 4).toIntOrNull() ?: return null
        val month = value.substring(5, 7).toIntOrNull() ?: return null
        val day = value.substring(8, 10).toIntOrNull() ?: return null
        val hour = value.substring(11, 13).toIntOrNull() ?: return null
        val minute = value.substring(14, 16).toIntOrNull() ?: return null
        val second = value.substring(17, 19).toIntOrNull() ?: return null

        if (month !in 1..12 || day !in 1..31 || hour !in 0..23 || minute !in 0..59 || second !in 0..60) return null
        if (day > daysInMonth(year, month)) return null

        val days = daysFromCivil(year, month, day)
        return days * 86_400L + hour * 3_600L + minute * 60L + second
    }

    /** Days since 1970-01-01 for a proleptic-Gregorian y/m/d. Howard Hinnant's `days_from_civil`. */
    private fun daysFromCivil(year: Int, month: Int, day: Int): Long {
        val y = if (month <= 2) year - 1 else year
        val era = (if (y >= 0) y else y - 399) / 400
        val yoe = (y - era * 400).toLong() // [0, 399]
        val mp = (month + 9) % 12
        val doy = (153L * mp + 2) / 5 + day - 1 // [0, 365]
        val doe = yoe * 365 + yoe / 4 - yoe / 100 + doy // [0, 146096]
        return era.toLong() * 146_097L + doe - 719_468L
    }

    private fun daysInMonth(year: Int, month: Int): Int = when (month) {
        1, 3, 5, 7, 8, 10, 12 -> 31
        4, 6, 9, 11 -> 30
        else -> if (isLeap(year)) 29 else 28
    }

    private fun isLeap(year: Int): Boolean = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
}

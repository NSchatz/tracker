package com.nschatz.tracker.alert

/**
 * Which way a device crossed a Place.
 *
 * A closed set with no "unknown" member on purpose: a push or a row whose direction the app cannot
 * read is not a crossing with an unknown direction, it is an incomplete payload, and the fail-safe
 * for that is to discard and count it rather than to render "your child did something at School".
 */
enum class Transition(val wire: String) {
    ENTER("enter"),
    EXIT("exit"),
    ;

    companion object {
        /** Parses the wire value, or null when it is neither of the two the server sends. */
        fun fromWire(value: String?): Transition? = entries.firstOrNull { it.wire == value }
    }
}

/**
 * One geofence crossing as the app holds it: which device, which Place, which way, and when.
 *
 * ### What is deliberately NOT here
 *
 * No coordinate, no accuracy, no raw fix datum. Not because none was needed, but because none may
 * exist on this surface at all: a push transits a third party's servers, and the alert surface's
 * whole privacy claim is that it carries only the labels the family chose for themselves. There is
 * nowhere in this class to put a latitude, which is the point - a field that does not exist cannot
 * be logged, rendered or backed up by mistake.
 *
 * @param deviceId the crossing device's id. Part of the identity, never shown to a person.
 * @param deviceName the family's own name for the device, or **null** when the source did not carry
 *   one. Null is meaningful and is never replaced by a guess: a push carries the name as its
 *   notification title, and a server older than this phase's `/v1` addition does not carry it on a
 *   crossings row at all. The UI renders null as a visibly-not-a-name placeholder.
 * @param placeId the Place's id. Part of the identity.
 * @param placeName the family's own name for the Place.
 * @param transition which way.
 * @param instantSeconds the crossing fix's event-time as **epoch seconds**.
 *
 *   This is the normalisation that makes one crossing one row. The two sources state the same
 *   instant in different formats - a push carries a UTC calendar timestamp, `GET /v1/geofence-events`
 *   carries epoch seconds - so the identity has to be the instant they agree on rather than the text
 *   either of them used.
 */
data class Crossing(
    val deviceId: String,
    val deviceName: String?,
    val placeId: String,
    val placeName: String,
    val transition: Transition,
    val instantSeconds: Long,
) {
    /**
     * The crossing's identity: (device, Place, instant).
     *
     * The instant is part of it, and must be. A key of (device, Place) alone cannot separate two
     * crossings of one device at one Place at different instants - leaving for school and coming
     * home again would collapse into one row, and the second arrival would silently disappear from
     * the list that exists precisely to catch what the push backend dropped.
     */
    val identity: CrossingIdentity get() = CrossingIdentity(deviceId, placeId, instantSeconds)
}

/** See [Crossing.identity]. */
data class CrossingIdentity(
    val deviceId: String,
    val placeId: String,
    val instantSeconds: Long,
)

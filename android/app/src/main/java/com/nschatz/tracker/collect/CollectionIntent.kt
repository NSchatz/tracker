package com.nschatz.tracker.collect

/**
 * The operator's last explicit choice about whether this device reports its location.
 *
 * This is a fact about **intent**, and it is deliberately not the same thing as whether collection
 * is running right now. A phone that has just rebooted has an intent and no running collection; a
 * phone whose service the system killed has an intent and no running collection; the whole point of
 * the boot path is that the first can be turned back into the second without a human. Conflating
 * the two is how an app ends up claiming to be collecting because somebody once said it should.
 *
 * Written only by an explicit operator action - the start/stop control on the screen, or the Stop
 * action on the ongoing notification. Nothing the platform does on its own (a process kill, a
 * service restart, a reboot) may touch it, because none of those is the operator changing their
 * mind.
 */
enum class CollectionIntent { ON, OFF }

/**
 * What was actually found where the intent is stored.
 *
 * Three cases, not two, and that is the whole reason this type exists. A stored value that is
 * missing or is not one of the two the codec writes must never be **guessed** into `ON` or `OFF`:
 * the app does not know what the operator wanted, and inventing an answer is the confident wrong
 * answer this repo refuses everywhere else. [Missing] and [Unreadable] are kept apart so the app
 * can say which one it met.
 */
sealed interface IntentReading {

    /** A value the codec wrote, read back intact. */
    data class Known(val intent: CollectionIntent) : IntentReading

    /** Nothing has ever been stored - a fresh install, or the entry was removed. */
    data object Missing : IntentReading

    /** Something is stored and it is not one of the two values the codec writes. */
    data object Unreadable : IntentReading
}

/**
 * Turns a [CollectionIntent] into the string that goes on disk, and back.
 *
 * Pure, so every branch of the read is provable by the gate. The write side is one line; the read
 * side is where the criteria live, and it is unit-tested exhaustively.
 */
object CollectionIntentCodec {

    /** The only two values this codec ever writes. */
    const val STORED_ON: String = "on"
    const val STORED_OFF: String = "off"

    fun encode(intent: CollectionIntent): String = when (intent) {
        CollectionIntent.ON -> STORED_ON
        CollectionIntent.OFF -> STORED_OFF
    }

    /**
     * Reads a stored value.
     *
     * **Exact match only.** `"ON"`, `" on"`, `"true"` and `""` are all [IntentReading.Unreadable],
     * not `ON`. Trimming and case-folding would be a guess dressed up as tolerance: this app only
     * ever writes the two literals above, so anything else in that slot means something other than
     * this app put it there, and the fail-safe is to say so rather than to interpret it.
     */
    fun decode(raw: String?): IntentReading = when (raw) {
        null -> IntentReading.Missing
        STORED_ON -> IntentReading.Known(CollectionIntent.ON)
        STORED_OFF -> IntentReading.Known(CollectionIntent.OFF)
        else -> IntentReading.Unreadable
    }
}

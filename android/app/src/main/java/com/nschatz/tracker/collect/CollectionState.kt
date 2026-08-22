package com.nschatz.tracker.collect

import android.content.Context

/**
 * The durable record of what the operator asked for and why collection is not doing it.
 *
 * Two facts, one owner. The boot receiver, the collection service and the screen all read and write
 * the same intent and the same single recorded reason, and every one of the clearing rules is a
 * rule about their interaction - so they are behind one small object rather than three call sites
 * that each remember two thirds of the rules.
 *
 * It is the framework edge and nothing more: the storage is [ClientPreferences] and every decision
 * it applies comes from [RecordedReasons], which is pure and is covered by the gate. Writes are
 * mirrored into [CollectionStatus] so the screen updates without polling.
 */
class CollectionState(context: Context) {

    private val preferences = ClientPreferences(context)

    /** What is stored where the collection intent lives. Never guessed into a value. */
    fun intent(): IntentReading = preferences.readCollectionIntent()

    /**
     * Records an explicit operator choice.
     *
     * Only ever called from an explicit operator action - the screen's start/stop control, or the
     * Stop action on the ongoing notification. A platform-driven stop (a process kill, a service
     * the system reclaims) must never reach here: the operator has not changed their mind, and
     * flipping the intent would mean the next boot silently honours a decision nobody made.
     *
     * Turning collection OFF clears any recorded reason, because a device whose intent is off holds
     * none.
     */
    fun setIntent(intent: CollectionIntent) {
        preferences.writeCollectionIntent(intent)
        if (intent == CollectionIntent.OFF) clearReasonOn(ReasonClearingEvent.INTENT_TURNED_OFF)
    }

    /** The recorded reason held right now, or null. */
    fun reason(): ReasonCase? = preferences.readRecordedReason()

    /**
     * Writes a reason, replacing whichever one was held.
     *
     * At most one is held at a time by construction: this is a write, not an append.
     */
    fun recordReason(case: ReasonCase) {
        preferences.writeRecordedReason(case)
        CollectionStatus.recordedReason = case
    }

    /**
     * Clears the held reason if [event] means the condition it names has stopped holding.
     *
     * A no-op when nothing is held or when the event does not apply to the case that is - which is
     * what keeps a start that succeeds from erasing the one reason that exists precisely while
     * collection runs.
     */
    fun clearReasonOn(event: ReasonClearingEvent) {
        val held = preferences.readRecordedReason() ?: return
        if (!RecordedReasons.clearedBy(held, event)) return
        preferences.writeRecordedReason(null)
        CollectionStatus.recordedReason = null
    }

    /**
     * Loads the durable reason into the observable status the screen reads.
     *
     * Called when the app is opened. The counters in [CollectionStatus] describe this process's run
     * and are deliberately not persisted; the recorded reason is the opposite kind of thing - it is
     * a fact about a case that happened, possibly in a process that no longer exists - so it is read
     * back from disk rather than starting empty.
     */
    fun publish() {
        CollectionStatus.recordedReason = preferences.readRecordedReason()
    }
}

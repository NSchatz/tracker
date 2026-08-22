package com.nschatz.tracker.collect

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The persisted collection intent, read back.
 *
 * The read is where the criteria are: everything the boot path does starts with what it found in
 * this slot, and the one behaviour that must never appear is a guess. A codec that folded an
 * unrecognised value into `OFF` would look safe and would be wrong for the same reason one that
 * folded it into `ON` would be - both invent an operator decision that was never made, and only one
 * of them is quiet about it.
 */
class CollectionIntentCodecTest {

    @Test
    fun roundTripsBothValues() {
        for (intent in CollectionIntent.values()) {
            assertEquals(
                IntentReading.Known(intent),
                CollectionIntentCodec.decode(CollectionIntentCodec.encode(intent)),
            )
        }
    }

    @Test
    fun nothingStoredIsMissing() {
        assertEquals(IntentReading.Missing, CollectionIntentCodec.decode(null))
    }

    /**
     * Present but not one of the two values. Each of these is a real way the slot gets corrupted -
     * a hand-edited preferences file, a value some other version wrote, an empty string - and none
     * of them may be interpreted.
     */
    @Test
    fun anythingElseIsUnreadableAndIsNeverInterpreted() {
        for (raw in listOf("", " ", "ON", "On", " on", "on ", "true", "1", "yes", "enabled", "OFF")) {
            assertEquals(
                "a stored value of '$raw' must not be guessed into an intent",
                IntentReading.Unreadable,
                CollectionIntentCodec.decode(raw),
            )
        }
    }

    /** The two literals are part of the on-disk format; changing one silently orphans every phone. */
    @Test
    fun theStoredLiteralsAreStable() {
        assertEquals("on", CollectionIntentCodec.encode(CollectionIntent.ON))
        assertEquals("off", CollectionIntentCodec.encode(CollectionIntent.OFF))
    }
}

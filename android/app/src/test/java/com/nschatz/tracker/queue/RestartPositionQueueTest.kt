package com.nschatz.tracker.queue

import com.nschatz.tracker.protocol.Fix
import com.nschatz.tracker.protocol.FixTrigger
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

/**
 * The restart position in the durable queue: an ordinary entry, and deliberately nothing more.
 *
 * The reboot work adds one position per transition into collecting, and the temptation it creates
 * is to protect it - to pin it, reserve room for it, or exempt it from the cap - because it is the
 * evidence that a phone came back. Every one of those is a change to the eviction policy, and the
 * policy is frozen: at the bound the OLDEST entries go, whichever fixes they are.
 *
 * A device offline long enough to fill 5,000 slots will therefore evict its own restart position
 * first, that eviction is counted like any other, and that is correct rather than a defect. Special
 * retention would mean sacrificing a newer position - the one that answers "where are they now" -
 * to keep an older one whose only value was proving a restart that is by then hours old.
 */
class RestartPositionQueueTest {

    @get:Rule
    val temp = TemporaryFolder()

    private fun queueDir(): File = File(temp.root, "fix-queue")

    private fun fix(ts: Long, trigger: String) = Fix(
        lat = 41.9028,
        lon = 12.4964,
        tsEpochSeconds = ts,
        accuracyM = 5.0,
        batteryPct = 88,
        trigger = trigger,
        msgId = "m-$ts",
    )

    /**
     * At the queue's bound the restart position is evicted under the existing oldest-first policy,
     * exactly like any other entry.
     *
     * The restart position is enqueued first, so it is the oldest, so it is what a full queue drops
     * first. The eviction is reported in the same `evicted` count the UI already surfaces as
     * `dropped`, so the loss is visible rather than silent.
     */
    @Test
    fun atTheBoundTheRestartPositionIsEvictedOldestFirstLikeAnyOtherEntry() {
        val queue = FixQueue(queueDir(), capacity = 3)
        val restartTs = 1_752_566_400L

        assertTrue(queue.enqueue(fix(restartTs, FixTrigger.RESTART)) is EnqueueResult.Queued)
        for (offset in 1L..2L) {
            assertTrue(queue.enqueue(fix(restartTs + offset * 60, FixTrigger.PERIODIC)) is EnqueueResult.Queued)
        }
        assertEquals(3, queue.size())
        assertTrue(
            "the restart position is queued like anything else",
            queue.oldest(3).entries.any { it.tsEpochSeconds == restartTs },
        )

        // One more fix takes the queue past its cap.
        val outcome = queue.enqueue(fix(restartTs + 180, FixTrigger.PERIODIC))
        assertTrue(outcome is EnqueueResult.Queued)
        assertEquals(
            "the eviction is counted, not swallowed",
            1,
            (outcome as EnqueueResult.Queued).evicted,
        )
        assertEquals(3, queue.size())
        assertFalse(
            "the restart position is not pinned, reserved or specially preserved at the bound",
            queue.oldest(3).entries.any { it.tsEpochSeconds == restartTs },
        )
        assertEquals(
            "and what remains is still the newest three, oldest first",
            listOf(restartTs + 60, restartTs + 120, restartTs + 180),
            queue.oldest(3).entries.map { it.tsEpochSeconds },
        )
    }

    /**
     * Below the bound it is not dropped, reordered behind a later position, or replaced by one.
     *
     * The other half of the rule: the no-dropping guarantee governs the ordinary case, and the
     * queue's oldest-first drain is what keeps the restart position ahead of everything measured
     * after it even when it is the entry that could not be delivered.
     */
    @Test
    fun belowTheBoundItIsKeptInOrderAheadOfEverythingTakenAfterIt() {
        val queue = FixQueue(queueDir(), capacity = 10)
        val restartTs = 1_752_566_400L

        queue.enqueue(fix(restartTs, FixTrigger.RESTART))
        queue.enqueue(fix(restartTs + 60, FixTrigger.PERIODIC))
        queue.enqueue(fix(restartTs + 120, FixTrigger.PERIODIC))

        val batch = queue.oldest(10)
        assertEquals(0, batch.discarded)
        assertEquals(
            listOf(restartTs, restartTs + 60, restartTs + 120),
            batch.entries.map { it.tsEpochSeconds },
        )
        assertTrue(
            "the stored bytes are the wire body, restart trigger and all",
            batch.entries.first().body.contains("\"trigger\":\"${FixTrigger.RESTART}\""),
        )
    }

    /**
     * A restart position and a steady-state position that share an instant collapse to one entry,
     * on the same `(device_id, ts)` identity the server dedups on. A reboot must not be able to add
     * a duplicate stored position, and this is the client-side half of that: the filesystem cannot
     * hold two files with one name.
     */
    @Test
    fun aRestartPositionSharingAnInstantWithASampleIsNotStoredTwice() {
        val queue = FixQueue(queueDir(), capacity = 10)
        val ts = 1_752_566_400L

        assertTrue(queue.enqueue(fix(ts, FixTrigger.RESTART)) is EnqueueResult.Queued)
        assertTrue(queue.enqueue(fix(ts, FixTrigger.PERIODIC)) is EnqueueResult.AlreadyQueued)
        assertEquals(1, queue.size())
        assertTrue(
            "the first report for an instant wins, matching the server's rule",
            queue.oldest(10).entries.first().body.contains("\"trigger\":\"${FixTrigger.RESTART}\""),
        )
    }
}

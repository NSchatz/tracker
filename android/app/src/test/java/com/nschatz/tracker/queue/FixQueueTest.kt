package com.nschatz.tracker.queue

import com.nschatz.tracker.protocol.Fix
import com.nschatz.tracker.protocol.FixTrigger
import com.nschatz.tracker.protocol.toJsonBody
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

/**
 * The durable queue, exercised against a **real directory on a real filesystem**.
 *
 * This is the half of C2 that a headless gate can prove completely, and it is the half that matters
 * most: the claim "a fix survives an outage and a process death" is a claim about bytes on disk, and
 * `java.io.File` behaves the same in this JVM as it does in ART. Nothing here is mocked, so a
 * regression in the ordering, the dedup or the atomic write fails the build rather than a review.
 */
class FixQueueTest {

    @get:Rule
    val temp = TemporaryFolder()

    private fun queueDir(): File = File(temp.root, "fix-queue")

    private fun queue(capacity: Int = FixQueue.DEFAULT_CAPACITY) = FixQueue(queueDir(), capacity)

    private fun fix(ts: Long, msgId: String = "m-$ts") = Fix(
        lat = 41.9028,
        lon = 12.4964,
        tsEpochSeconds = ts,
        accuracyM = 5.0,
        batteryPct = 88,
        trigger = FixTrigger.PERIODIC,
        msgId = msgId,
    )

    private fun bodies(batch: BatchResult): List<String> = batch.entries.map { it.body }

    // --- durability -----------------------------------------------------------------------------

    /**
     * The whole point of C2. C1 held fixes in an in-memory executor, so a process death lost every
     * one of them; here a **different instance** on the same directory sees them, which is what the
     * app does after being killed and restarted.
     */
    @Test
    fun queuedFixesSurviveTheProcessThatWroteThem() {
        val writer = queue()
        writer.enqueue(fix(1_752_566_400))
        writer.enqueue(fix(1_752_566_460))

        val afterRestart = FixQueue(queueDir())
        assertEquals(2, afterRestart.size())
        assertEquals(
            listOf(1_752_566_400L, 1_752_566_460L),
            afterRestart.oldest(10).entries.map { it.tsEpochSeconds },
        )
    }

    /** The queue creates its own directory; a first-ever fix must not be lost to a missing folder. */
    @Test
    fun theDirectoryIsCreatedOnDemand() {
        assertFalse(queueDir().exists())
        assertTrue(queue().enqueue(fix(1_752_566_400)) is EnqueueResult.Queued)
        assertTrue(queueDir().isDirectory)
    }

    // --- what is stored -------------------------------------------------------------------------

    /**
     * The stored bytes are the wire body, unchanged.
     *
     * This is the property that lets delivery skip re-encoding: what was validated at enqueue is
     * what goes on the wire. If this drifts, the payload tests in `FixReporterTest` stop covering
     * what is actually sent.
     */
    @Test
    fun theStoredBytesAreExactlyTheWireBody() {
        val f = fix(1_752_566_400)
        queue().enqueue(f)
        assertEquals(listOf(f.toJsonBody()), bodies(queue().oldest(10)))
    }

    /**
     * The `msg_id` is fixed at enqueue and persists, so a replay carries the same correlator rather
     * than inventing a new one. `SPEC.md` makes `msg_id` a *secondary* correlator — the server dedups
     * on `(device_id, ts)` — which is exactly why it must stay stable to be worth anything in a log.
     */
    @Test
    fun theCorrelatorIsPersistedWithTheFixRatherThanRegenerated() {
        queue().enqueue(fix(1_752_566_400, msgId = "correlator-42"))
        val first = queue().oldest(1).entries.single().body
        val second = queue().oldest(1).entries.single().body
        assertTrue("""the msg_id is missing: $first""", first.contains(""""msg_id":"correlator-42""""))
        assertEquals(first, second)
    }

    // --- ordering -------------------------------------------------------------------------------

    /** Oldest first, whatever order they arrived in — the server's history is ordered by `ts`. */
    @Test
    fun entriesComeBackOldestFirstRegardlessOfEnqueueOrder() {
        val q = queue()
        q.enqueue(fix(1_752_566_520))
        q.enqueue(fix(1_752_566_400))
        q.enqueue(fix(1_752_566_460))
        assertEquals(
            listOf(1_752_566_400L, 1_752_566_460L, 1_752_566_520L),
            q.oldest(10).entries.map { it.tsEpochSeconds },
        )
    }

    /**
     * The zero-padding, pinned.
     *
     * Ordering is a **filename sort**, so without a fixed width `"10"` sorts before `"9"` and the
     * queue silently flushes out of order. These timestamps are absurd for a real device and that is
     * the point: the bug only shows up when the decimal widths differ, which they never do inside a
     * single decade of epoch seconds — so a test using realistic values could not catch it.
     */
    @Test
    fun shorterTimestampsStillSortBeforeLongerOnes() {
        val q = queue()
        q.enqueue(fix(10L))
        q.enqueue(fix(9L))
        q.enqueue(fix(100L))
        assertEquals(listOf(9L, 10L, 100L), q.oldest(10).entries.map { it.tsEpochSeconds })
    }

    @Test
    fun oldestReturnsAtMostTheRequestedNumber() {
        val q = queue()
        for (i in 0 until 5) q.enqueue(fix(1_752_566_400L + i))
        assertEquals(2, q.oldest(2).entries.size)
        assertEquals(5, q.size())
    }

    // --- idempotency ----------------------------------------------------------------------------

    /**
     * The client half of idempotency: a second fix for an instant already queued is absorbed.
     *
     * The identity is `ts`, deliberately the same identity the server dedups on (`SPEC.md`), so the
     * queue cannot hold two entries that the server would collapse into one row.
     */
    @Test
    fun enqueueingTheSameInstantTwiceIsAnIdempotentNoOp() {
        val q = queue()
        assertTrue(q.enqueue(fix(1_752_566_400, msgId = "first")) is EnqueueResult.Queued)
        assertEquals(EnqueueResult.AlreadyQueued, q.enqueue(fix(1_752_566_400, msgId = "second")))
        assertEquals(1, q.size())
        // The FIRST report for an instant wins, matching the server's own rule.
        assertTrue(q.oldest(1).entries.single().body.contains(""""msg_id":"first""""))
    }

    /** A re-enqueue after delivery is a fresh entry — the queue is not a permanent dedup ledger. */
    @Test
    fun anInstantCanBeQueuedAgainOnceItsEntryIsRemoved() {
        val q = queue()
        q.enqueue(fix(1_752_566_400))
        q.remove(q.oldest(1).entries.single())
        assertTrue(q.enqueue(fix(1_752_566_400)) is EnqueueResult.Queued)
        assertEquals(1, q.size())
    }

    // --- removal --------------------------------------------------------------------------------

    @Test
    fun removeDeletesTheEntry() {
        val q = queue()
        q.enqueue(fix(1_752_566_400))
        val entry = q.oldest(1).entries.single()
        assertTrue(q.remove(entry))
        assertEquals(0, q.size())
    }

    /** Removing something already gone is a no-op, not a failure — an eviction can beat a flush. */
    @Test
    fun removingAnEntryThatIsAlreadyGoneIsNotAnError() {
        val q = queue()
        q.enqueue(fix(1_752_566_400))
        val entry = q.oldest(1).entries.single()
        assertTrue(q.remove(entry))
        assertFalse(q.remove(entry))
    }

    // --- the cap --------------------------------------------------------------------------------

    /**
     * At the cap the **oldest** entries go, and the count comes back so the loss can be shown.
     *
     * An unbounded queue on a phone offline for a month is a disk-full bug, so a cap has to exist;
     * what must not exist is a cap that forgets quietly. The returned count is what
     * `CollectionStatus.dropped` is fed from.
     */
    @Test
    fun aFullQueueEvictsTheOldestAndReportsHowMany() {
        val q = queue(capacity = 3)
        for (i in 0 until 3) q.enqueue(fix(1_000L + i))

        val outcome = q.enqueue(fix(1_003L))
        assertEquals(EnqueueResult.Queued(evicted = 1), outcome)
        assertEquals(3, q.size())
        // The oldest went; what is left is still in order and still contiguous.
        assertEquals(listOf(1_001L, 1_002L, 1_003L), q.oldest(10).entries.map { it.tsEpochSeconds })
    }

    @Test
    fun anUnderfullQueueEvictsNothing() {
        val q = queue(capacity = 3)
        assertEquals(EnqueueResult.Queued(evicted = 0), q.enqueue(fix(1_000L)))
        assertEquals(EnqueueResult.Queued(evicted = 0), q.enqueue(fix(1_001L)))
    }

    // --- crash safety ---------------------------------------------------------------------------

    /**
     * A torn write is invisible.
     *
     * Content goes to a `.tmp` and is renamed into place, so a crash mid-write can only ever leave a
     * `.tmp` — which no reader accepts. Writing in place instead would leave a half-written body
     * that the server answers with a `400 malformed`, and the fix would be discarded as
     * "permanently refused" when in truth it was corrupted locally.
     */
    @Test
    fun aPartialWriteIsNeverVisibleAsAQueuedFix() {
        val q = queue()
        q.enqueue(fix(1_752_566_400))
        // Simulate a crash mid-write: a leftover temp file for a *newer*, unfinished fix.
        File(queueDir(), "0000000001752566460.fix.tmp").writeText("""{"lat":41.9,"lo""")

        assertEquals(1, q.size())
        assertEquals(listOf(1_752_566_400L), q.oldest(10).entries.map { it.tsEpochSeconds })
    }

    /** And the debris is cleaned up, so repeated crashes cannot fill the app's data directory. */
    @Test
    fun partialWritesAreSweptAway() {
        val q = queue()
        q.enqueue(fix(1_752_566_400))
        val debris = File(queueDir(), "0000000001752566460.fix.tmp")
        debris.writeText("half a fix")

        q.oldest(10)
        assertFalse(debris.exists())
    }

    /**
     * An entry that cannot be read is discarded **and counted**.
     *
     * It can never be delivered, and a FIFO whose head is undeliverable blocks every fix behind it
     * forever — so it has to go. The count is what keeps that from being silent: it is real data
     * loss, and it surfaces in `CollectionStatus.dropped`.
     */
    @Test
    fun anUnreadableEntryIsDiscardedAndCountedRatherThanBlockingTheQueue() {
        val q = queue()
        q.enqueue(fix(1_752_566_460))
        // A zero-length entry — what a rename that outran an unsynced write would leave behind.
        File(queueDir(), "0000000001752566400.fix").writeText("")

        val batch = q.oldest(10)
        assertEquals(1, batch.discarded)
        assertEquals(listOf(1_752_566_460L), batch.entries.map { it.tsEpochSeconds })
        assertEquals(1, q.size())
    }

    // --- refusals -------------------------------------------------------------------------------

    /**
     * A pre-epoch `ts` is refused rather than stored.
     *
     * It cannot be zero-padded into a name that sorts correctly, so storing it would silently put it
     * in the wrong place in the queue. It is also 25 years outside the server's 90-day ingest window,
     * so nothing is lost by refusing — and the refusal is typed, which the fail-safe stance requires.
     */
    @Test
    fun aFixTimestampedBeforeTheEpochIsRefusedRatherThanMisfiled() {
        val outcome = queue().enqueue(fix(-1L))
        assertTrue(outcome is EnqueueResult.Failed)
        assertEquals(0, queue().size())
    }

    @Test
    fun aZeroCapacityQueueIsRefusedAtConstruction() {
        val error = runCatching { FixQueue(queueDir(), capacity = 0) }.exceptionOrNull()
        assertNotNull("a queue that can hold nothing should not be constructible", error)
        assertTrue(error is IllegalArgumentException)
    }

    // --- name/timestamp round trip ---------------------------------------------------------------

    @Test
    fun entryNamesRoundTripToTheirTimestamp() {
        val name = FixQueue.entryName(1_752_566_400)
        assertEquals(1_752_566_400L, FixQueue.timestampOf(name))
        assertNull(FixQueue.timestampOf("not-a-timestamp.fix"))
    }
}

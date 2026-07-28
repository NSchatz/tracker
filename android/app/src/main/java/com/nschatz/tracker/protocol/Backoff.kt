package com.nschatz.tracker.protocol

/**
 * The retry schedule for a [ReportOutcome.Retryable] report: exponential, capped, and jittered.
 *
 * ### Why each part is there
 *
 * - **Exponential** — a phone that just came out of a tunnel and a server that is briefly down want
 *   opposite things from a client. Growing the gap serves both: quick recovery when it was a blip,
 *   restraint when it is an outage.
 * - **Capped** ([maxDelayMillis]) — without a ceiling the delay doubles into hours and the client
 *   silently stops reporting while still calling itself "retrying". The cap is what keeps a long
 *   outage recoverable without a restart.
 * - **Jittered** — this is the part that is easy to leave out and matters most here. tracker's own
 *   roadmap names the **post-outage reconnect storm** as a load risk (§6): every phone in every
 *   family retries on the same schedule after the same outage, so an unjittered backoff
 *   synchronises them into a thundering herd that re-creates the outage it is recovering from.
 *   Full jitter — a uniform draw over `[0, delay]` rather than a fixed `delay` — spreads them out.
 * - **Bounded attempts** ([maxAttempts]) — so a fix that cannot be delivered is eventually
 *   surfaced as a failure rather than retried forever behind a green-looking UI.
 *
 * The policy is **pure**: [delayMillis] takes the random draw as an argument instead of calling a
 * generator, so the schedule is exactly assertable in a unit test rather than "roughly right".
 *
 * @param baseDelayMillis the delay after the first failure, before jitter.
 * @param multiplier growth factor per attempt.
 * @param maxDelayMillis ceiling on the pre-jitter delay.
 * @param maxAttempts how many attempts (including the first) before giving up.
 */
data class BackoffPolicy(
    val baseDelayMillis: Long = 2_000,
    val multiplier: Double = 2.0,
    val maxDelayMillis: Long = 5 * 60_000,
    val maxAttempts: Int = 6,
) {
    init {
        require(baseDelayMillis > 0) { "baseDelayMillis must be positive" }
        require(multiplier >= 1.0) { "multiplier must be at least 1.0" }
        require(maxDelayMillis >= baseDelayMillis) { "maxDelayMillis must be at least baseDelayMillis" }
        require(maxAttempts >= 1) { "maxAttempts must be at least 1" }
    }

    /**
     * The un-jittered delay to wait *after* [attempt] failed attempts, capped at [maxDelayMillis].
     *
     * @param attempt 1-based: `1` is the delay after the first failure.
     */
    fun uncappedFreeDelayMillis(attempt: Int): Long {
        require(attempt >= 1) { "attempt is 1-based" }
        // Computed in Double then capped, so a large attempt count saturates at the ceiling instead
        // of overflowing a Long into a negative delay (which would retry instantly, forever).
        val grown = baseDelayMillis * Math.pow(multiplier, (attempt - 1).toDouble())
        return if (grown >= maxDelayMillis.toDouble()) maxDelayMillis else grown.toLong()
    }

    /**
     * The actual delay to wait after [attempt] failures, with **full jitter** applied.
     *
     * @param randomFraction a uniform draw in `[0, 1)`, supplied by the caller so this stays pure.
     *   In production it comes from [kotlin.random.Random.nextDouble]; in a test it is whatever the
     *   test needs it to be.
     */
    fun delayMillis(attempt: Int, randomFraction: Double): Long {
        require(randomFraction in 0.0..1.0) { "randomFraction must be in [0, 1]" }
        return (uncappedFreeDelayMillis(attempt) * randomFraction).toLong()
    }

    /** Whether another attempt is allowed after [attempt] failures. */
    fun shouldRetry(attempt: Int): Boolean = attempt in 1 until maxAttempts
}

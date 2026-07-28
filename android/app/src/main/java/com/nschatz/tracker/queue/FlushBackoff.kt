package com.nschatz.tracker.queue

/**
 * The **initial** retry delay handed to WorkManager when a flush is deferred — jittered, per device.
 *
 * ### What WorkManager already does, and the one thing it does not
 *
 * WorkManager owns the retry loop: a `Result.retry()` is rescheduled with an exponential policy that
 * doubles the initial delay per attempt up to a five-hour ceiling, indefinitely, and only while the
 * `NetworkType.CONNECTED` constraint holds. That is strictly better than the sleep-in-a-thread
 * backoff C1 carried, which held a wakelock for the whole delay and gave up after six attempts.
 *
 * What WorkManager does **not** do is jitter, and jitter is not a detail here. tracker's roadmap
 * names the **post-outage reconnect storm** as a load risk (§6): every phone in every family fails
 * against the same outage at the same moment, so an identical retry schedule synchronises them into
 * a thundering herd that re-creates the outage it is recovering from. Because WorkManager doubles
 * whatever initial delay it is given, spreading *that* one number spreads the entire schedule — two
 * devices starting at 30 s and 60 s are still an octave apart eight retries later.
 *
 * ### Pure on purpose
 *
 * [initialDelaySeconds] takes the random draw as an argument rather than calling a generator, so the
 * window it produces is exactly assertable in a unit test instead of "roughly right".
 */
object FlushBackoff {

    /**
     * The floor of the jitter window, in seconds.
     *
     * Half a minute: long enough that a phone briefly out of coverage does not re-attempt while the
     * radio is still down, short enough that a blip is recovered from before the next fix is even
     * measured (C1's cadence is one a minute).
     */
    const val BASE_SECONDS: Long = 30

    /** The width of the jitter window, in seconds. The draw spreads devices across `[30, 60]`. */
    const val JITTER_SECONDS: Long = 30

    /**
     * WorkManager's own minimum backoff (`WorkRequest.MIN_BACKOFF_MILLIS`, 10 s).
     *
     * Restated as a floor this class enforces itself, so that changing the two constants above can
     * never silently produce a value WorkManager would clamp — a clamp would remove the jitter
     * without removing the code that looks like it is applying some.
     */
    const val MIN_SECONDS: Long = 10

    /**
     * The initial retry delay for one flush request.
     *
     * @param randomFraction a uniform draw in `[0, 1]`, supplied by the caller. In production it
     *   comes from [kotlin.random.Random.nextDouble]; in a test it is whatever the test needs.
     */
    fun initialDelaySeconds(randomFraction: Double): Long {
        require(randomFraction in 0.0..1.0) { "randomFraction must be in [0, 1]" }
        val delay = BASE_SECONDS + (JITTER_SECONDS * randomFraction).toLong()
        return if (delay < MIN_SECONDS) MIN_SECONDS else delay
    }
}

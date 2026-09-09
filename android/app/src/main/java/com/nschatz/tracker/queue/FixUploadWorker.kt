package com.nschatz.tracker.queue

import android.content.Context
import android.util.Log
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import com.nschatz.tracker.BuildConfig
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.CollectionStatus
import com.nschatz.tracker.collect.ConfigStatus
import com.nschatz.tracker.collect.TroubleKind
import com.nschatz.tracker.protocol.FixReporter
import java.util.concurrent.TimeUnit
import kotlin.random.Random

/**
 * The **WorkManager flush job**: sends whatever [FixQueue] is holding, whenever there is a network.
 *
 * ### Why WorkManager and not a thread
 *
 * C1 delivered from a thread pool inside the foreground service, which meant delivery lived and died
 * with the service. WorkManager is the platform's answer to the two things that broke: its work is
 * **persisted** (an enqueued job survives process death and reboot) and it is **constrained** —
 * `NetworkType.CONNECTED` means a flush enqueued in airplane mode is not attempted, not retried, and
 * not failed; it simply waits, costing nothing, and runs the moment connectivity returns. That is
 * exactly the airplane-mode→online behaviour C2 has to deliver, and it is the platform's job, not
 * something worth re-implementing with a `ConnectivityManager` callback and a wakelock.
 *
 * ### Where the retry schedule went
 *
 * C1 carried its own `BackoffPolicy` — exponential, jittered, bounded — and slept inside the report
 * thread between attempts. That is gone, replaced by WorkManager's own exponential backoff, for two
 * reasons: sleeping in a worker holds a wakelock and keeps the CPU up for the whole delay, and a
 * *bounded* attempt count is precisely what C2 must not have — a fix that keeps failing transiently
 * must keep waiting, not eventually be dropped. WorkManager retries a `Result.retry()` indefinitely,
 * doubling the initial delay per attempt up to the platform's five-hour cap, and only while the
 * network constraint holds. The one thing it does not supply is jitter, which [FlushBackoff] adds to
 * the initial delay — see there for why that matters more than it looks.
 *
 * ### What the gate proves here
 *
 * Nothing in this class, and that is why there is so little of it. WorkManager needs a `Context` and
 * a real (or Robolectric-simulated) Android runtime, so this file is a thin translation layer with
 * no decisions in it: it reads config, builds a [QueueFlusher], and maps a [FlushReport] onto a
 * WorkManager `Result`. Every decision it appears to make was made in [QueueFlusher], which is pure
 * and is tested against a real queue and a real socket. See `android/README.md`.
 */
class FixUploadWorker(
    appContext: Context,
    params: WorkerParameters,
) : Worker(appContext, params) {

    override fun doWork(): Result {
        val queue = FixQueues.of(applicationContext)
        if (queue.size() == 0) return Result.success()

        when (val config = ClientPreferences(applicationContext).readConfig()) {
            is ConfigStatus.Incomplete -> {
                // No server URL or no token. The queued fixes are NOT touched — they stay on disk
                // until there is somewhere to send them. `failure()` rather than `retry()` because
                // nothing WorkManager can do changes this: only a human entering the configuration
                // does, and every path that changes it (the settings screen, starting collection,
                // enqueueing a fix) calls `enqueueFlush` again. Retrying on a schedule would just
                // wake the device to re-read the same empty preference.
                CollectionStatus.recordBlocked(TroubleKind.NOT_CONFIGURED, config.reason)
                return Result.failure()
            }

            is ConfigStatus.Configured -> {
                val flusher = QueueFlusher(
                    queue = queue,
                    reporter = FixReporter(
                        baseUrl = config.config.baseUrl,
                        deviceToken = config.config.deviceToken,
                    ),
                )
                // One flush handles one batch, so a long backlog takes several — driven here, in a
                // loop, rather than by re-enqueueing this same unique work from inside itself
                // (which would mean cancelling the running worker to schedule its own successor).
                // `nanoTime` is monotonic, so the budget is not fooled by a clock adjustment.
                val deadline = System.nanoTime() + RUN_BUDGET_NANOS
                var report: FlushReport
                do {
                    report = flusher.flush()
                    publish(report)
                } while (report.outcome is FlushOutcome.MoreQueued && System.nanoTime() < deadline)
                return resultFor(report.outcome)
            }
        }
    }

    /** Puts what the flush achieved in front of the user. */
    private fun publish(report: FlushReport) {
        CollectionStatus.recordFlush(
            delivered = report.delivered,
            discarded = report.discarded,
            queued = FixQueues.of(applicationContext).size(),
            reason = report.reason,
        )
        logDebug("flush: ${report.outcome} delivered=${report.delivered} discarded=${report.discarded}")
    }

    /** Translates the last flush's outcome into WorkManager's vocabulary. */
    private fun resultFor(outcome: FlushOutcome): Result = when (outcome) {
        is FlushOutcome.Drained -> Result.success()

        // The run budget expired with fixes still owed — a very large backlog on a slow link. They
        // are all still on disk; WorkManager reschedules and the drain resumes where it stopped.
        is FlushOutcome.MoreQueued -> Result.retry()

        // Transient. Everything still owed is still on disk; WorkManager backs off and tries again,
        // forever, which is the guarantee C2 exists to provide.
        is FlushOutcome.Deferred -> Result.retry()

        is FlushOutcome.CredentialRejected -> {
            CollectionStatus.recordBlocked(TroubleKind.CREDENTIAL_REJECTED, outcome.reason)
            // Also a retry, deliberately. The fixes are kept, and a token can be re-issued
            // server-side without the app being touched, so giving up would strand a queue that may
            // become deliverable on its own. The exponential backoff means a permanently wrong token
            // costs one request every few hours, not a loop.
            Result.retry()
        }
    }

    private fun logDebug(message: String) {
        if (BuildConfig.VERBOSE_LOGGING) Log.d(TAG, message)
    }

    companion object {
        private const val TAG = "TrackerFlush"

        /** The unique work name. One flush at a time, process-wide. */
        const val UNIQUE_WORK_NAME: String = "tracker-fix-upload"

        /**
         * How long one worker run will keep draining before handing the rest back to WorkManager.
         *
         * Eight minutes, inside the ten a worker is given before the system may stop it. Reached
         * only by a backlog of thousands on a slow link; nothing is lost when it is, because the
         * remainder is still on disk and the rescheduled run picks up where this one stopped.
         */
        private val RUN_BUDGET_NANOS: Long = TimeUnit.MINUTES.toNanos(8)

        /**
         * Asks for the queue to be flushed.
         *
         * Called on every enqueued fix, when the app opens, and when the server configuration is
         * saved — all cheap, because the work is **unique** and [ExistingWorkPolicy.KEEP] means a
         * flush that is already pending or running absorbs the request instead of restarting it.
         * Restarting would be actively harmful: it would reset the exponential backoff on every new
         * fix, so a phone that keeps collecting during an outage would hammer a dead server once a
         * minute.
         *
         * The cost of KEEP is a narrow race — a fix enqueued in the instant between a running
         * flush's final queue check and WorkManager marking that work finished gets no follow-up
         * request of its own. It is not a loss: the fix is on disk, and the next fix (or the next
         * app open) schedules the flush that sends it.
         */
        fun enqueueFlush(context: Context) {
            val constraints = Constraints.Builder()
                // The whole airplane-mode story in one line: with no network the job is not run and
                // not failed, it is simply not yet eligible.
                .setRequiredNetworkType(NetworkType.CONNECTED)
                .build()
            val request = OneTimeWorkRequest.Builder(FixUploadWorker::class.java)
                .setConstraints(constraints)
                // The draw is taken per request, so two devices recovering from the same outage do
                // not retry in lockstep — see FlushBackoff.
                .setBackoffCriteria(
                    BackoffPolicy.EXPONENTIAL,
                    FlushBackoff.initialDelaySeconds(Random.nextDouble()),
                    TimeUnit.SECONDS,
                )
                .build()
            WorkManager.getInstance(context.applicationContext).enqueueUniqueWork(
                UNIQUE_WORK_NAME,
                ExistingWorkPolicy.KEEP,
                request,
            )
        }
    }
}

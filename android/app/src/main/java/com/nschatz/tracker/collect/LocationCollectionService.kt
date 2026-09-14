package com.nschatz.tracker.collect

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.location.Location
import android.os.BatteryManager
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import com.google.android.gms.location.FusedLocationProviderClient
import com.google.android.gms.location.LocationCallback
import com.google.android.gms.location.LocationRequest
import com.google.android.gms.location.LocationResult
import com.google.android.gms.location.LocationServices
import com.google.android.gms.location.Priority
import com.nschatz.tracker.BuildConfig
import com.nschatz.tracker.R
import com.nschatz.tracker.protocol.FixFactory
import com.nschatz.tracker.protocol.FixResult
import com.nschatz.tracker.protocol.FixTrigger
import com.nschatz.tracker.protocol.LocationReading
import com.nschatz.tracker.queue.EnqueueResult
import com.nschatz.tracker.queue.FixQueue
import com.nschatz.tracker.queue.FixQueues
import com.nschatz.tracker.queue.FixUploadWorker
import com.nschatz.tracker.ui.MainActivity
import java.util.UUID

/**
 * The **foreground service** that collects location continuously and reports each fix to
 * `POST /v1/fixes`.
 *
 * ### Why a foreground service, and not anything else
 *
 * Android does not offer a way to collect location continuously in the background without one. A
 * `WorkManager` job cannot: its minimum periodic interval is 15 minutes and it is subject to Doze
 * deferral, so it produces a sparse, unpredictable trail rather than a track. A plain background
 * service is killed. The foreground service, with its **mandatory ongoing notification**, is the
 * platform's deliberate bargain: an app may keep the GPS running as long as the person carrying the
 * phone can always see that it is. For a product that tracks family members — including minors —
 * that visibility is not a tax to be minimised, it is the honest part of the design.
 *
 * From **Android 14 (API 34)**, which this app targets, `android:foregroundServiceType` is
 * **required**, and `location` demands `FOREGROUND_SERVICE_LOCATION` in the manifest plus at least
 * one location runtime permission granted before `startForeground` is called. All three are wired:
 * see `AndroidManifest.xml` and [LocationPermissionFlow][com.nschatz.tracker.permission.LocationPermissionFlow].
 *
 * ### What this class is NOT covered by
 *
 * Everything in this file is **device behaviour**, and the gate cannot prove any of it. A headless
 * build cannot grant a runtime permission, cannot start a real service, cannot produce a GPS fix,
 * and cannot observe what happens when the screen goes off or an OEM battery manager decides to
 * intervene. That is why the logic worth testing was pushed *out* of here — into
 * `protocol/` and `permission/`, which are pure and are covered — and why the residue that
 * genuinely needs a device is documented as an **operator check**, not asserted by a test that
 * mocks the platform and proves only that the mock was configured. See `android/README.md`,
 * "What the gate proves and what it cannot".
 */
class LocationCollectionService : Service() {

    private var fusedClient: FusedLocationProviderClient? = null

    /**
     * The durable queue every measured fix is written to before anything tries to send it.
     *
     * Since **C2** the service does no networking at all. It measures, validates, persists, and asks
     * WorkManager to flush — which is what makes a fix survive an outage, a process death and a
     * reboot. C1's in-memory bounded executor is gone with it: its overflow was a counted but real
     * loss, and there is nothing left for it to do now that "hold the fix" means "write it to disk".
     */
    private val queue: FixQueue by lazy { FixQueues.of(this) }

    private val locationCallback = object : LocationCallback() {
        override fun onLocationResult(result: LocationResult) {
            for (location in result.locations) {
                handleLocation(location)
            }
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        fusedClient = LocationServices.getFusedLocationProviderClient(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            // A person pressed Stop in the notification shade, which is one of exactly two places
            // the ask is expressed (the other is the screen's control). Clearing it here is what
            // makes the next boot honour the stop rather than restart collection on a phone whose
            // owner turned it off.
            ClientPreferences(this).collectionEnabled = false
            stopSelf()
            return START_NOT_STICKY
        }

        // `startForeground` comes FIRST — before the config check, before any work.
        //
        // This ordering is not cosmetic. The service is launched with `startForegroundService`, and
        // Android then requires `startForeground` within a few seconds; an app that instead decides
        // it has nothing to do and calls `stopSelf()` is killed with a
        // ForegroundServiceDidNotStartInTimeException. So the "refuse to run on bad config" path
        // below has to post the notification first and *then* stop — the alternative is a crash on
        // exactly the misconfiguration the check exists to handle gracefully.
        // Built OUTSIDE the try. The catch below reports everything it sees as "Android refused the
        // location foreground service", which is true of every way startForeground itself fails —
        // but building the notification touches resources, and a Resources.NotFoundException raised
        // in there would be reported to the user as a permission problem they cannot fix. Keeping
        // the try around exactly the call whose failures the message describes is what keeps the
        // message honest.
        val notification = buildNotification()
        try {
            ServiceCompat.startForeground(
                this,
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_LOCATION,
            )
        } catch (e: RuntimeException) {
            // Deliberately the whole RuntimeException family, not just SecurityException.
            // `startForeground` refuses in at least four distinct, unrelated ways:
            //
            //   SecurityException                     — a location runtime permission was revoked
            //                                           between the UI's check and this line.
            //   ForegroundServiceStartNotAllowedException (API 31+, extends IllegalStateException)
            //                                         — the service was started from the background,
            //                                           which is exactly what a START_STICKY restart
            //                                           does after the system reclaims the process.
            //   MissingForegroundServiceTypeException /
            //   InvalidForegroundServiceTypeException (API 34)
            //                                         — the declared type is absent or not permitted.
            //
            // They share no common subclass below RuntimeException, so enumerating them by name
            // would need version guards and would still miss the next one Android adds. Every one of
            // them means the same thing to this service — it cannot enter the foreground — and C1's
            // fail-safe is that this becomes a clear disabled state, never a crash. An uncaught
            // throw here kills the process before `recordBlocked` runs, so the user is left with a
            // crash loop and no explanation, which is the one outcome worse than not collecting.
            CollectionStatus.recordBlocked(
                TroubleKind.SERVICE_REFUSED,
                "Collection could not start: Android refused the location foreground service " +
                    "(${e.javaClass.simpleName}). Check that location permission is granted, then " +
                    "start it again from this screen.",
            )
            logDebug("startForeground refused: ${e.javaClass.simpleName}: ${e.message}")
            stopSelf()
            return START_NOT_STICKY
        }

        val prefs = ClientPreferences(this)
        val status = prefs.readConfig()
        if (status is ConfigStatus.Incomplete) {
            // The fail-safe: refuse to run rather than run and quietly report nowhere. A service
            // showing "sharing your location" while posting to an unset URL is precisely the
            // looks-like-it's-working failure this project treats as worse than an outage.
            CollectionStatus.recordBlocked(TroubleKind.NOT_CONFIGURED, status.reason)
            stopSelf()
            return START_NOT_STICKY
        }
        CollectionStatus.reset()
        // Collection is genuinely running, so a reason a previous boot recorded for not starting it
        // is history. It is cleared HERE rather than by the caller that asked for the service,
        // because `startForegroundService` returns before the service has entered the foreground:
        // clearing it on the strength of a call that returned would discard the explanation for a
        // start that then failed.
        prefs.bootRestartReason = null
        // The queue may already hold fixes from a previous run — that is exactly what C2 buys — so
        // the depth is read from disk rather than assumed to be zero, and a flush is asked for
        // straight away so a backlog left by a killed process starts draining without waiting for
        // the next fix.
        CollectionStatus.recordQueued(queue.depth())
        FixUploadWorker.enqueueFlush(this)
        CollectionStatus.running = true
        startLocationUpdates()
        // START_STICKY: if the system kills us for memory, ask to be restarted. It is a request,
        // not a guarantee — and on Android 12+ a background restart cannot re-enter the foreground
        // without ACCESS_BACKGROUND_LOCATION, which is exactly why the two-step permission flow
        // treats background as required rather than nice-to-have.
        return START_STICKY
    }

    override fun onDestroy() {
        fusedClient?.removeLocationUpdates(locationCallback)
        // Nothing to abandon. C1 had to count the fixes its in-memory executor still held when the
        // service stopped, because they were lost at that moment. Since C2 an undelivered fix is on
        // disk and the flush job is WorkManager's, so stopping collection ends measurement and
        // leaves delivery running — a stop is no longer a loss.
        CollectionStatus.running = false
        super.onDestroy()
    }

    private fun startLocationUpdates() {
        val policy = CollectionPolicy()
        val request = LocationRequest.Builder(Priority.PRIORITY_HIGH_ACCURACY, policy.intervalMillis)
            .setMinUpdateIntervalMillis(policy.minUpdateIntervalMillis)
            .setMinUpdateDistanceMeters(policy.minUpdateDistanceMeters)
            .setMaxUpdateDelayMillis(policy.maxUpdateDelayMillis)
            .setWaitForAccurateLocation(false)
            .build()
        try {
            fusedClient?.requestLocationUpdates(request, locationCallback, mainLooper)
        } catch (e: SecurityException) {
            // The permission was revoked between the UI check and here — a real race, because the
            // user can revoke from Settings while the service runs. Stop rather than sit alive
            // producing nothing.
            CollectionStatus.recordBlocked(
                TroubleKind.PERMISSION_LOST,
                "Location permission was revoked, so collection stopped.",
            )
            logDebug("requestLocationUpdates denied: ${e.message}")
            stopSelf()
        }
    }

    private fun handleLocation(location: Location) {
        val reading = readingFrom(location)
        val nowSeconds = System.currentTimeMillis() / 1000
        val result = FixFactory.build(
            reading = reading,
            nowEpochSeconds = nowSeconds,
            batteryPct = readBatteryPercent(),
            trigger = FixTrigger.PERIODIC,
            // The correlator is generated HERE, once, and is then persisted with the fix. It must
            // not be regenerated on a retry: `msg_id` is how a human correlates one report across
            // the client log and the server's history, and a replayed fix that invented a new one
            // would look like a second, different report of the same instant.
            msgId = UUID.randomUUID().toString(),
        )
        when (result) {
            is FixResult.Refused -> {
                // Refused locally: the coordinate or the clock is wrong. Dropping it is correct —
                // the server would answer 400 and store nothing anyway — but it is COUNTED, so a
                // device producing garbage is visible rather than merely quiet.
                CollectionStatus.recordDropped(result.rejection.message)
                logDebug("refusing a fix: ${result.rejection.kind} ${result.rejection.message}")
            }

            is FixResult.Valid -> {
                CollectionStatus.lastFixAtMillis = System.currentTimeMillis()
                enqueue(result)
            }
        }
    }

    /**
     * Persists one fix and asks for a flush.
     *
     * This is the whole of C2's write path in the service: measure, store, ask. Nothing here blocks
     * on the network, so a dead radio no longer backs up behind the location callback, and nothing
     * is held in memory that a process death could take.
     */
    private fun enqueue(result: FixResult.Valid) {
        when (val outcome = queue.enqueue(result.fix)) {
            is EnqueueResult.Queued -> {
                if (outcome.evicted > 0) {
                    // The queue was full, so the oldest fixes were sacrificed for this one. Real
                    // loss, and the only kind an outage can still cause — say so.
                    CollectionStatus.recordDroppedBatch(
                        outcome.evicted,
                        "The offline queue is full, so ${outcome.evicted} of the oldest waiting " +
                            "fix(es) were discarded. The server has not been reachable for a long time.",
                    )
                }
                CollectionStatus.recordQueued(queue.depth())
                FixUploadWorker.enqueueFlush(this)
            }

            // Same instant, already waiting. The server would dedup it anyway; absorbing it here is
            // the identical outcome without the round trip.
            is EnqueueResult.AlreadyQueued -> logDebug("a fix for this instant was already queued")

            is EnqueueResult.Failed -> {
                CollectionStatus.recordDropped(outcome.reason)
                logDebug("failed to queue a fix: ${outcome.reason}")
            }
        }
    }

    /**
     * Reads the battery level, or null when the platform will not say.
     *
     * `BATTERY_PROPERTY_CAPACITY` returns `Integer.MIN_VALUE` on a device that does not support it,
     * and that must become **absent**, not a fabricated reading — the server stores a missing
     * battery as SQL `NULL` and a present one as a real measurement, and there is no honest way to
     * turn "the device would not tell us" into a percentage.
     */
    private fun readBatteryPercent(): Int? {
        val manager = ContextCompat.getSystemService(this, BatteryManager::class.java) ?: return null
        val level = manager.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)
        return if (level in 0..100) level else null
    }

    private fun createNotificationChannel() {
        val manager = ContextCompat.getSystemService(this, NotificationManager::class.java) ?: return
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.collection_channel_name),
            // LOW: the notification must be permanently visible, but it is a status indicator, not
            // an alert. IMPORTANCE_LOW shows it without sound or heads-up intrusion.
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = getString(R.string.collection_channel_description)
            setShowBadge(false)
        }
        manager.createNotificationChannel(channel)
    }

    private fun buildNotification(): Notification {
        val tapIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val pending = PendingIntent.getActivity(
            this,
            0,
            tapIntent,
            // IMMUTABLE because nothing downstream needs to fill anything in, and a mutable
            // PendingIntent handed to the notification shade is a capability given away for free.
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        // A Stop action in the shade. For a product that tracks people, being able to turn the
        // tracking off from the same notification that announces it is part of the bargain the
        // foreground service makes — not a convenience. It is safe to deliver ACTION_STOP this way
        // because this PendingIntent only exists while the service is already in the foreground.
        val stopIntent = Intent(this, LocationCollectionService::class.java).apply {
            action = ACTION_STOP
        }
        val stopPending = PendingIntent.getService(
            this,
            1,
            stopIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.collection_notification_title))
            .setContentText(getString(R.string.collection_notification_body))
            .setSmallIcon(R.drawable.ic_collection)
            .setOngoing(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .addAction(0, getString(R.string.collection_stop), stopPending)
            // No coordinate, ever, in a notification: it is readable on a lock screen by anyone
            // holding the phone. The same rule the server applies to push payloads (SPEC.md, S6).
            .setVisibility(NotificationCompat.VISIBILITY_SECRET)
            .setContentIntent(pending)
            .build()
    }

    /**
     * Debug-only logging.
     *
     * Gated on `BuildConfig.VERBOSE_LOGGING`, which is false in release. **No coordinate, token or
     * timestamp of a real person ever goes through here** — a release logcat is readable by other
     * apps on some devices and by anyone with adb, and location is the one thing this product must
     * not leak. C3 tightens this into a tested guarantee; the rule starts now, with the first code
     * that has anything sensitive to leak.
     */
    private fun logDebug(message: String) {
        if (BuildConfig.VERBOSE_LOGGING) Log.d(TAG, message)
    }

    companion object {
        private const val TAG = "TrackerCollection"
        private const val CHANNEL_ID = "tracker_collection"
        private const val NOTIFICATION_ID = 1001

        /**
         * Stops collection, delivered by the **notification's Stop action**.
         *
         * Only ever sent to a service that is already running in the foreground, which is what makes
         * it safe: `onStartCommand` answers it with a bare `stopSelf()`, and a `stopSelf()` without a
         * preceding `startForeground` is the platform's
         * "did not then call Service.startForeground()" kill. The UI's stop button deliberately does
         * **not** use this — see [stop].
         */
        const val ACTION_STOP = "com.nschatz.tracker.action.STOP_COLLECTION"

        /**
         * Starts collection.
         *
         * `startForegroundService` (not `startService`): from Android 8 a background start of a
         * service that intends to go foreground must use this, and the service then has a few
         * seconds to call `startForeground` or be killed with an ANR-style crash.
         */
        fun start(context: Context) {
            val intent = Intent(context, LocationCollectionService::class.java)
            ContextCompat.startForegroundService(context, intent)
        }

        /**
         * Stops collection.
         *
         * `stopService`, **not** `startForegroundService(ACTION_STOP)`. The latter is a trap: if the
         * service happens not to be running — the UI's notion of "running" is an in-memory flag that
         * a process death silently resets — the platform starts it, waits for a `startForeground`
         * that the stop path deliberately never makes, and kills the app with
         * "Context.startForegroundService() did not then call Service.startForeground()". Asking a
         * service to stop must not be able to launch it first.
         *
         * `stopService` on a service that is not running is simply a no-op.
         */
        fun stop(context: Context) {
            context.stopService(Intent(context, LocationCollectionService::class.java))
        }
    }
}

/**
 * The framework edge: pulls the fields tracker uses out of a platform [Location].
 *
 * Kept as a tiny top-level function, separate from the pure [LocationReading] it produces, so that
 * the only code touching an un-mockable framework class is these few lines and everything that
 * *reasons* about a location is testable. `hasAccuracy()`/`hasSpeed()` are honoured rather than
 * reading the raw fields: an unset accuracy reads back as `0.0`, and 0 m of accuracy is a claim of
 * perfect precision, not the absence of a claim.
 */
internal fun readingFrom(location: Location): LocationReading = LocationReading(
    lat = location.latitude,
    lon = location.longitude,
    elapsedMillis = location.time,
    accuracyM = if (location.hasAccuracy()) location.accuracy.toDouble() else null,
    speedMps = if (location.hasSpeed()) location.speed.toDouble() else null,
)

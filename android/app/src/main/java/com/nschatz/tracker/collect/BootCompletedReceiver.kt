package com.nschatz.tracker.collect

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import androidx.core.content.ContextCompat
import com.nschatz.tracker.BuildConfig
import com.nschatz.tracker.permission.LocationGrants
import com.nschatz.tracker.permission.LocationPermissionFlow

/**
 * Starts collection again after the phone boots, when that is what somebody asked for.
 *
 * ### Why a boot receiver may do this at all
 *
 * Android names the foreground-service types a `BOOT_COMPLETED` receiver may NOT launch - `dataSync`,
 * `camera`, `mediaPlayback`, `phoneCall`, `mediaProjection`, `microphone` - and throws
 * `ForegroundServiceStartNotAllowedException` at a receiver that tries one. `location` is not on that
 * list, and a `location` service has no platform timeout once it is running, so restarting continuous
 * collection from here is supported rather than a trick.
 *
 * The one hard condition is `ACCESS_BACKGROUND_LOCATION`: the platform will not create a `location`
 * foreground service from the background without it, and a receiver IS the background. So the grant
 * is checked before the attempt instead of the attempt being made and its failure swallowed - which
 * would reach the same outcome with the reason lost.
 *
 * ### What it cannot do anything about
 *
 * An app in the STOPPED state is not delivered `ACTION_BOOT_COMPLETED` at all until a user action
 * takes it out of that state, whatever it targets. A force-stopped app therefore does not run this
 * code after a reboot and no code here could make it. That case is surfaced where it can be - the
 * screen reads collection as not running, because running is a live signal and not a stored setting -
 * and it is written down in `android/README.md` rather than claimed as covered.
 *
 * ### Nothing here decides anything
 *
 * The judgement is [BootRestartDecision], which is pure and covered exhaustively by a JVM test. This
 * class reads three values off the platform and does what it is told, which is the only shape that
 * makes boot behaviour gate-provable at all: a `BroadcastReceiver` is not something a headless build
 * can construct, deliver an intent to, or observe, and the unit-test classpath here is `junit` alone.
 */
class BootCompletedReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent?) {
        // Strictly the one action, and that guard is load-bearing rather than defensive. The receiver
        // has to be exported, because a protected system broadcast is delivered from outside the app;
        // acting on whatever it was handed would make it a way for anything on the device to ask this
        // phone to start reporting where its owner is.
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED) return

        val prefs = ClientPreferences(context)
        val decision = BootRestartDecision.decide(
            collectionEnabled = prefs.collectionEnabled,
            capability = LocationPermissionFlow.capability(grantsOf(context)),
            config = prefs.readConfig(),
        )
        when (decision) {
            is BootRestartAction.DoNotStart -> {
                // Nothing is started and nothing pretends to be running: what the screen shows for
                // running is read from a live signal, and no session exists. The only thing owed
                // here is the reason, and only when there is one - a person who turned collection
                // off is owed silence, not a complaint about a choice they made.
                prefs.bootRestartReason = decision.reason
                logDebug("boot: not starting collection (${decision.reason?.name ?: "nobody asked"})")
            }

            is BootRestartAction.Start -> start(context, prefs)
        }
    }

    /**
     * Asks for the collection service, and records a platform refusal as its own reason.
     *
     * The success path writes nothing, because there is nothing yet to write down:
     * `startForegroundService` returns before the service has entered the foreground, so a call that
     * returned is not evidence of collection. The service clears the previous reason itself once it
     * is actually collecting.
     */
    private fun start(context: Context, prefs: ClientPreferences) {
        try {
            LocationCollectionService.start(context)
            logDebug("boot: asked for collection")
        } catch (e: RuntimeException) {
            // Deliberately the whole RuntimeException family, for the reason the service's own
            // comment gives: the refusals share no common subclass below it, enumerating them by
            // name would need version guards and would still miss the next one Android adds, and
            // every one of them means the same thing here. What must not happen is a throw out of
            // onReceive, which is a crash at boot with no collection and no explanation.
            //
            // A refusal in spite of the grants is reported as the missing grant, which is the
            // honest reading: the platform refusing a background start of a `location` service is
            // telling us the background-location condition is not met, whatever `checkSelfPermission`
            // said a moment earlier, and it is the one thing a person can act on.
            prefs.bootRestartReason = BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED
            logDebug("boot: start refused: ${e.javaClass.simpleName}")
        }
    }

    private fun logDebug(message: String) {
        if (BuildConfig.VERBOSE_LOGGING) Log.d(TAG, message)
    }

    private companion object {
        const val TAG = "TrackerBoot"
    }
}

/**
 * The framework edge: what the OS grants this app right now, as plain data.
 *
 * A top-level function rather than a method so the only Android call in the boot path's judgement -
 * `checkSelfPermission` - sits on its own, exactly as `readGrants` does for the screen.
 * `POST_NOTIFICATIONS` is treated as granted below API 33, where it does not exist.
 */
internal fun grantsOf(context: Context): LocationGrants = LocationGrants(
    fineLocation = context.holds(Manifest.permission.ACCESS_FINE_LOCATION),
    coarseLocation = context.holds(Manifest.permission.ACCESS_COARSE_LOCATION),
    backgroundLocation = context.holds(Manifest.permission.ACCESS_BACKGROUND_LOCATION),
    postNotifications = Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
        context.holds(LocationPermissionFlow.POST_NOTIFICATIONS),
)

private fun Context.holds(permission: String): Boolean =
    ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED

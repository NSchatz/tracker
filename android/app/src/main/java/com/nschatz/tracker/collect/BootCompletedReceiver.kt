package com.nschatz.tracker.collect

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import com.nschatz.tracker.BuildConfig
import com.nschatz.tracker.permission.LocationPermissionFlow
import com.nschatz.tracker.permission.readLocationGrants

/**
 * Restarts collection after the device boots, if that is what the operator asked for.
 *
 * ### Why this is allowed to exist
 *
 * Android names the foreground-service types a `BOOT_COMPLETED` receiver may **not** launch -
 * `dataSync`, `camera`, `mediaPlayback`, `phoneCall`, `mediaProjection`, `microphone` - and throws
 * `ForegroundServiceStartNotAllowedException` at a receiver that tries. `location` is not on that
 * list, and a `location` service has no platform timeout, so restarting continuous collection from
 * here is a supported thing to do rather than a trick. The one hard condition is
 * `ACCESS_BACKGROUND_LOCATION`: without it the platform will not create a `location` foreground
 * service from the background, and a receiver is the background. That is why the permission is
 * checked *before* the attempt instead of the attempt being made and its failure swallowed.
 *
 * ### What it deliberately does not cover
 *
 * A **stopped** app is not delivered `ACTION_BOOT_COMPLETED` at all until a user action removes it
 * from that state. So a force-stopped app, or one an OEM battery manager has stopped, does not run
 * this code after a reboot and there is nothing here that could make it. That case is surfaced
 * where it can be - the screen shows collection as not running when it is opened, because running
 * is read from a live signal and not from a stored setting - and it is written down as a known
 * limitation rather than claimed as covered.
 *
 * ### Repeated deliveries
 *
 * The platform may deliver the boot signal more than once for a single boot, and this receiver has
 * no id, latch or timestamp to notice it. It does not need one: every delivery asks the service to
 * start, and a service that is already collecting absorbs the second ask without opening a new
 * session - see [LocationCollectionService]. So duplicates collapse into one transition and one
 * restart position, while an operator who turns collection off and on again later in the same boot
 * still gets a genuine second transition, which a per-boot latch would have suppressed. Keeping the
 * dedup at the service, where the session actually is, is what makes both true at once.
 */
class BootCompletedReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent?) {
        // Strictly the one action. The receiver is exported because a protected system broadcast is
        // delivered from outside the app, and a receiver that acted on whatever it was handed would
        // be a way to ask this phone to start reporting its location.
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED) return

        val state = CollectionState(context)
        val capability = LocationPermissionFlow.capability(readLocationGrants(context))

        when (val action = BootStartDecision.decide(state.intent(), capability)) {
            is BootAction.DoNotStart -> {
                // Nothing is started and nothing pretends to be running: the running state the app
                // shows is a live signal, and no session exists, so it reads not running on its
                // own. The only thing owed here is the reason, and only when there is one - an
                // operator who turned collection off is owed silence.
                action.reason?.let { state.recordReason(it) }
                logDebug("boot: not starting collection (${action.reason})")
            }

            is BootAction.Start -> start(context, state)
        }
    }

    /**
     * Asks for the collection service, and records what happened if the platform says no.
     *
     * The success path writes nothing. A start is not proof of collection - `startForegroundService`
     * returns before the service has entered the foreground - so the reason for a previous failure
     * is cleared by the service when it actually begins collecting, not here on the strength of a
     * call that returned.
     */
    private fun start(context: Context, state: CollectionState) {
        try {
            LocationCollectionService.start(context)
            logDebug("boot: asked for collection")
        } catch (e: RuntimeException) {
            // Deliberately the whole RuntimeException family. The refusals share no common subclass
            // below it, enumerating them by name would need version guards and would still miss the
            // next one Android adds, and every one of them means the same thing here: collection
            // did not restart. What must not happen is a throw out of onReceive, which is a crash
            // at boot with no explanation and no collection - the one outcome worse than not
            // collecting. RecordedReasons tells a platform refusal from an unrecognised failure.
            state.recordReason(RecordedReasons.refusalReason(e))
            logDebug("boot: start refused: ${e.javaClass.simpleName}: ${e.message}")
        }
    }

    private fun logDebug(message: String) {
        if (BuildConfig.VERBOSE_LOGGING) Log.d(TAG, message)
    }

    private companion object {
        const val TAG = "TrackerBoot"
    }
}

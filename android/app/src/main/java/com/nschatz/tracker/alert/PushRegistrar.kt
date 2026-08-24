package com.nschatz.tracker.alert

import android.content.Context
import android.util.Log
import com.google.firebase.messaging.FirebaseMessaging
import com.nschatz.tracker.BuildConfig
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.ViewerConfigStatus
import java.util.concurrent.Executors

/**
 * Registers this phone's push endpoint with the family's server, and drives the alert delivery
 * status from what it learns.
 *
 * ### The ordered procedure lives elsewhere
 *
 * This class GATHERS the four inputs and hands them to [AlertStatusPolicy], which decides. Splitting
 * them is what makes the state machine testable at all: every branch of it, including all four
 * not-receivable reasons, is driven from a JVM unit test with no phone, no permission dialog and no
 * push backend, while what is left here is "ask Firebase for a token" and "make an HTTP call".
 *
 * ### No Firebase project configuration is required to BUILD
 *
 * This module depends on the FCM client library and deliberately does NOT apply the Google Services
 * Gradle plugin, so no per-project `google-services.json` is needed to compile, assemble, lint or
 * test - and the repo's gate builds exactly that way, on a checkout that has never seen a Firebase
 * project. The cost is paid honestly at RUNTIME: with no project configured, Firebase has no default
 * app, asking for a registration token throws, and this class catches that and reports "this phone
 * has no usable push configuration" rather than crashing. An app in that state still reads and shows
 * the family's crossings; it just cannot be pushed to.
 */
object PushRegistrar {

    private const val TAG = "PushRegistrar"

    /** The one backend this client has a receive path for. */
    const val PROVIDER: String = AlertStatusPolicy.RECEIVABLE_PROVIDER

    /**
     * A single background thread. Registration is one short HTTP call at app start and again on a
     * token rotation; a pool would be machinery for a workload that is never concurrent with itself.
     */
    private val executor = Executors.newSingleThreadExecutor { r ->
        Thread(r, "tracker-push-registrar").apply { isDaemon = true }
    }

    /**
     * Evaluates the alert delivery status and, when there is something to register, registers it.
     *
     * Safe to call on the main thread: it returns immediately and does its work on [executor].
     *
     * @param routingAddress the address to register, when the caller already has one (a token
     *   rotation). Null means "ask the push client for this phone's current one".
     */
    fun registerInBackground(context: Context, routingAddress: String? = null) {
        val appContext = context.applicationContext
        executor.execute { refreshBlocking(appContext, routingAddress) }
    }

    /**
     * The whole procedure, synchronously. Exposed for the background call above and for a caller
     * that is already off the main thread.
     */
    internal fun refreshBlocking(context: Context, routingAddress: String? = null) {
        val prefs = ClientPreferences(context)

        // Step 0. No viewer credential: nothing is attempted, and the status says so.
        val viewer = prefs.readViewerConfig()
        if (viewer !is ViewerConfigStatus.Configured) {
            AlertSurface.recordStatus(AlertDeliveryStatus.NotConfigured)
            return
        }

        // Step 1. Notifications blocked: an alert that cannot be shown is not an alert. The status is
        // decided by the policy, not here, so the ORDER stays in one place.
        val notificationsPermitted = AlertNotifications.canPost(context)

        // Step 2. This phone's routing address, or null when there is none to be had.
        val address = routingAddress?.takeIf { it.isNotBlank() } ?: currentRoutingAddress()

        // Steps 3 to 6 need the server's answer, and there is only an answer if there is something to
        // register. The policy short-circuits before the outcome when an earlier step holds, so a
        // null here is correct rather than merely convenient.
        val outcome = if (notificationsPermitted && !address.isNullOrBlank()) {
            val previous = prefs.registeredRoutingAddress
            val result = AlertClient(viewer.config.baseUrl, viewer.config.viewerToken).register(
                provider = PROVIDER,
                routingAddress = address,
                replacesRoutingAddress = previous?.takeIf { it != address },
            )
            if (result is RegistrationOutcome.Accepted) {
                // Only remember an address the server actually took. Remembering a refused one would
                // make the NEXT registration claim to supersede something that was never stored.
                prefs.registeredRoutingAddress = address
            }
            result
        } else {
            null
        }

        AlertSurface.recordStatus(
            AlertStatusPolicy.evaluate(
                AlertStatusInputs(
                    viewerCredentialPresent = true,
                    notificationsPermitted = notificationsPermitted,
                    routingAddress = address,
                    registration = outcome,
                ),
            ),
        )
    }

    /**
     * This phone's FCM registration token, or null when there is none to be had.
     *
     * Null is the ordinary case for a build with no Firebase project configured, which is how this
     * repo's gate builds the app and how a fork without a Firebase account would run it. It is
     * reported as "this phone has no usable push configuration" and never as a crash: a location
     * client that will not start because push is unconfigured would have traded a missing alert for a
     * missing map, which is a worse failure.
     */
    private fun currentRoutingAddress(): String? = try {
        // Blocking: this runs on the registrar's own background thread, never the main one.
        val task = FirebaseMessaging.getInstance().token
        awaitToken(task)
    } catch (e: Exception) {
        // IllegalStateException with no default FirebaseApp is the expected case; anything else here
        // is equally not-a-routing-address. The message is a library diagnostic and carries no
        // credential and no location, so it is safe to log in a debug build.
        if (BuildConfig.VERBOSE_LOGGING) {
            Log.i(TAG, "no push routing address on this phone: " + e.javaClass.simpleName)
        }
        null
    }

    /**
     * Waits for the token task. Split out so [currentRoutingAddress]'s catch covers the wait as well
     * as the call: a task that fails throws from `await`, not from `getInstance`.
     */
    private fun awaitToken(task: com.google.android.gms.tasks.Task<String>): String? {
        val latch = java.util.concurrent.CountDownLatch(1)
        var value: String? = null
        task.addOnCompleteListener { completed ->
            if (completed.isSuccessful) value = completed.result
            latch.countDown()
        }
        // Finite: a token request that never completes must not wedge the registrar forever. A
        // timeout reads as "no routing address", which is the honest answer and the safe direction -
        // it can never produce `armed`.
        latch.await(TOKEN_TIMEOUT_SECONDS, java.util.concurrent.TimeUnit.SECONDS)
        return value?.takeIf { it.isNotBlank() }
    }

    private const val TOKEN_TIMEOUT_SECONDS = 15L
}

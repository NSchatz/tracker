package com.nschatz.tracker.alert

import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * The first-party receive path: what happens when the server's crossing alert reaches this phone.
 *
 * This is the thing that did not exist before this phase. The server has delivered a crossing to a
 * push backend since S6, and nothing in this repo could receive one, so "get an alert when someone
 * arrives" was a server capability and not a product one.
 *
 * ### Deliberately almost empty
 *
 * Every decision is in [AlertIntake] and [AlertText], which are pure and unit-tested. What is left
 * here is the two framework callbacks and the `notify` call - the parts a headless gate genuinely
 * cannot exercise, and which `android/README.md` lists as operator device checks rather than pretends
 * to test.
 *
 * ### Why the title, and not a data field, carries the device name
 *
 * `SPEC.md` puts the device's name in the notification TITLE and keeps it out of the data map (which
 * carries `type`, `device_id`, `place_id`, `place_name`, `transition`, `ts` and nothing else). So the
 * intake takes both halves, and a push whose title is missing is an incomplete crossing - discarded
 * and counted, never rendered with a guessed name.
 */
class TrackerMessagingService : FirebaseMessagingService() {

    /**
     * A crossing arrived.
     *
     * The title is read from the notification block when the backend delivered one, and from the
     * data map's own fallback otherwise, so a message that arrives data-only still carries a device
     * name to render. Anything incomplete renders nothing and increments the discard count.
     */
    override fun onMessageReceived(message: RemoteMessage) {
        val title = message.notification?.title ?: message.data[DATA_TITLE_FALLBACK]
        val crossing = AlertIntake.receive(title, message.data) ?: return
        AlertNotifications.show(applicationContext, crossing)
    }

    /**
     * The FCM registration token rotated, so this phone's routing address changed.
     *
     * Re-registering the new address is not enough on its own: registration is idempotent on
     * (provider, address), so the OLD address stays a live row and this phone would receive every
     * crossing twice until something removed it. [PushRegistrar] therefore names the address being
     * replaced, and the server removes exactly that one, under the same viewer.
     */
    override fun onNewToken(token: String) {
        PushRegistrar.registerInBackground(applicationContext, routingAddress = token)
    }

    private companion object {
        /**
         * The data-map key a data-only message may carry the device name under.
         *
         * The server does not send it today (the name is the notification title), so this is a
         * forward-compatible read and NOT a fabrication: when it is absent, the title is absent, and
         * an absent title is an incomplete crossing that gets discarded rather than guessed at.
         */
        const val DATA_TITLE_FALLBACK = "device_name"
    }
}

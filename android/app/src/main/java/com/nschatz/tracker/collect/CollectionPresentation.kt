package com.nschatz.tracker.collect

/**
 * What the screen shows about collection when the app is opened.
 *
 * @param running whether to show collection as running.
 * @param reason the recorded reason to present alongside that state, or null when none is held.
 * @param unexplainedStop true when the operator asked for collection, it is not running, and
 *   nothing was recorded to say why. That is what a force-stop or an OEM battery-killer looks like
 *   from inside the app: no code of ours ran, so no code of ours could write a reason. Naming it is
 *   the difference between an honest "it is not running and I cannot tell you why" and a blank space
 *   the operator reads as "fine".
 */
data class CollectionPresentation(
    val running: Boolean,
    val reason: ReasonCase?,
    val unexplainedStop: Boolean,
)

/**
 * Decides what the screen shows about collection, from a live liveness signal and the durable
 * record.
 *
 * ### How "running" is determined, and why it is not the stored setting
 *
 * **`running` is [liveSessionRunning] and nothing else.** It is never the persisted collection
 * intent, and it is never a persisted "was running" flag. That is the load-bearing decision in this
 * file, so here is the case that forces it:
 *
 * > Intent is ON and collection is running, so any persisted running flag says running. The operator
 * > force-stops the app from Settings, or an OEM battery manager kills it. The device reboots. A
 * > stopped app is not delivered `ACTION_BOOT_COMPLETED` until user action removes it from that
 * > state, so nothing starts. The operator opens the app.
 *
 * A screen rendering the persisted flag shows **running** to a phone that has not reported since
 * yesterday. That is precisely the failure this product cannot have - silence that looks like
 * presence - and it is worse than not collecting, because the operator stops looking.
 *
 * The caller passes [CollectionStatus.running], which is process-scoped memory owned by the
 * collection service: it is set when the service starts collecting and cleared when it is
 * destroyed. The service runs **in this process**, so the two die together. A force-stop, an OEM
 * kill and an ordinary process death all take the flag with them, and the app that opens afterwards
 * opens in a new process where it reads false. It is a liveness signal, not a memory of one, and
 * that is exactly the property required of it. A future build that moved collection into a separate
 * process would have to replace it with something that still answers "is a session alive right now",
 * never with something read off disk.
 *
 * Opening the app also never starts collection. Nothing in this object or its caller has a start in
 * it: collection resumes on an explicit operator action or at the next boot the platform permits.
 * An open-time auto-start would make the not-running state unobservable in the one place it has to
 * be observable.
 */
object CollectionPresentationPolicy {

    /**
     * @param liveSessionRunning whether a collection session is alive in this process right now.
     * @param reading the persisted collection intent, as read.
     * @param recorded the recorded reason held right now, or null.
     */
    fun forOpen(
        liveSessionRunning: Boolean,
        reading: IntentReading,
        recorded: ReasonCase?,
    ): CollectionPresentation {
        // A device whose collection intent is simply OFF holds no recorded reason and presents
        // none. It is not broken, it is obeying, and there is nothing to explain. Anything left in
        // the durable record from before the operator turned it off is suppressed here as well as
        // cleared at the write - belt and braces on the one state that must be quiet.
        val intentOff = reading == IntentReading.Known(CollectionIntent.OFF)
        val reason = if (intentOff) null else recorded
        return CollectionPresentation(
            // The reason is presented ALONGSIDE the running state whether that state is running or
            // not. A device that IS collecting but could not take a restart position shows running
            // and presents that reason; a device whose automatic start was refused shows not
            // running and presents that one. The two are independent, and a build that only shows a
            // reason when it is stopped hides the case this criterion exists for.
            running = liveSessionRunning,
            reason = reason,
            unexplainedStop = !liveSessionRunning &&
                reading == IntentReading.Known(CollectionIntent.ON) &&
                reason == null,
        )
    }
}

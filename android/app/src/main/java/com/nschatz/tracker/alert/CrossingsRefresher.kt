package com.nschatz.tracker.alert

import android.content.Context
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.ViewerConfigStatus
import java.util.concurrent.Executors

/**
 * Reads the family's crossings back from the server when a viewer opens the app.
 *
 * This is the fail-safe for the push, not a nicety. The backend stores four collapsible messages per
 * phone, one per collapse key, and then discards - so a phone that was off the network past four
 * distinct device-and-Place pairs has lost the rest, permanently, with nothing telling anyone. The
 * server's own crossing log still has every one of them, and this is what puts it in front of the
 * person who needed it.
 *
 * The result, including every way it can fail, goes into [AlertSurface]; the failures stay separate
 * from an empty family all the way to the screen (see [CrossingListView]).
 */
object CrossingsRefresher {

    private val executor = Executors.newSingleThreadExecutor { r ->
        Thread(r, "tracker-crossings-read").apply { isDaemon = true }
    }

    /** Reads on a background thread. Safe to call from the main thread; returns immediately. */
    fun refreshInBackground(context: Context) {
        val appContext = context.applicationContext
        executor.execute { refreshBlocking(appContext) }
    }

    internal fun refreshBlocking(context: Context) {
        val viewer = ClientPreferences(context).readViewerConfig()
        if (viewer !is ViewerConfigStatus.Configured) {
            // No credential to read with. That is not a read failure and must not be recorded as one:
            // the alert delivery status already says the app is not configured, and reporting
            // "unreachable" here would send someone looking at their network.
            return
        }
        val result = AlertClient(viewer.config.baseUrl, viewer.config.viewerToken).crossings()
        AlertSurface.recordRead(result)
    }
}

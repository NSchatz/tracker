package com.nschatz.tracker.alert

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.nschatz.tracker.R

/**
 * Posts a crossing as a notification.
 *
 * The framework edge, and nothing else: the decision about WHETHER there is a crossing to show, and
 * what it says, is [AlertIntake] and [AlertText]. What is left here is the channel, the permission
 * check and the `notify` call - the parts a JVM test genuinely cannot exercise and which are named
 * as operator device checks in `android/README.md` rather than mocked into a green test.
 */
internal object AlertNotifications {

    /** The channel crossings are posted on. Separate from collection's ongoing notice. */
    const val CHANNEL_ID: String = "crossings"

    /**
     * Notification ids are per (device, Place), the same grouping the server's collapse key uses.
     *
     * So a rapid arrive-then-leave at one Place REPLACES the earlier notification rather than
     * stacking two, which matches what the backend would have done to the messages anyway - and, more
     * to the point, matches what a person wants: the latest state, not a history of buzzes.
     */
    private fun notificationId(crossing: Crossing): Int =
        (crossing.deviceId + ":" + crossing.placeId).hashCode()

    fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        val channel = NotificationChannel(
            CHANNEL_ID,
            context.getString(R.string.alert_channel_name),
            NotificationManager.IMPORTANCE_HIGH,
        ).apply {
            description = context.getString(R.string.alert_channel_description)
        }
        manager.createNotificationChannel(channel)
    }

    /**
     * Posts the crossing, if this app may.
     *
     * @return true when a notification was posted. False means the permission is not granted, which
     *   is not an error here: the app says so on its own screen (the alert delivery status reports
     *   that alerts cannot be shown) and the crossing is still in the in-app list.
     */
    fun show(context: Context, crossing: Crossing): Boolean {
        if (!canPost(context)) return false
        ensureChannel(context)

        val notification = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_collection)
            .setContentTitle(AlertText.notificationTitle(crossing))
            .setContentText(AlertText.notificationBody(crossing))
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_STATUS)
            .setAutoCancel(true)
            .build()

        return try {
            NotificationManagerCompat.from(context).notify(notificationId(crossing), notification)
            true
        } catch (_: SecurityException) {
            // The permission was revoked between the check and the post. Not a crash: the crossing is
            // still in the list, and the status already says alerts cannot be shown.
            false
        }
    }

    /** Whether this app may post a notification at all. Below API 33 it always may. */
    fun canPost(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return true
        return ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
    }
}

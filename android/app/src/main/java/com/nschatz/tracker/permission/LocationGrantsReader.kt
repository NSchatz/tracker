package com.nschatz.tracker.permission

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat

/**
 * Reads the current grant state from the OS.
 *
 * The framework edge of the permission flow, in the same spirit as `queue/FixQueues`:
 * `checkSelfPermission` is the only Android call involved, and everything decided from its result
 * lives in the pure [LocationPermissionFlow] next door.
 *
 * It sits here, rather than privately inside the screen, because the screen is no longer the only
 * caller: the boot receiver has to answer the same question, before the app has been opened at all,
 * to decide whether starting collection is even permitted. Two copies of a permission read is how
 * the two answers drift apart.
 */
fun readLocationGrants(context: Context): LocationGrants = LocationGrants(
    fineLocation = context.isGranted(Manifest.permission.ACCESS_FINE_LOCATION),
    coarseLocation = context.isGranted(Manifest.permission.ACCESS_COARSE_LOCATION),
    backgroundLocation = context.isGranted(Manifest.permission.ACCESS_BACKGROUND_LOCATION),
    // Below API 33 the permission does not exist and notifications are always allowed, so reporting
    // "granted" is the accurate answer rather than a convenient default.
    postNotifications = Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
        context.isGranted(LocationPermissionFlow.POST_NOTIFICATIONS),
)

private fun Context.isGranted(permission: String): Boolean =
    ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED

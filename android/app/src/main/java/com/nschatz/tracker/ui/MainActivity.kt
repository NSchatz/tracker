package com.nschatz.tracker.ui

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.app.ActivityCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import com.nschatz.tracker.R
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.CollectionIntent
import com.nschatz.tracker.collect.CollectionPresentation
import com.nschatz.tracker.collect.CollectionPresentationPolicy
import com.nschatz.tracker.collect.CollectionState
import com.nschatz.tracker.collect.CollectionStatus
import com.nschatz.tracker.collect.ConfigStatus
import com.nschatz.tracker.collect.LocationCollectionService
import com.nschatz.tracker.collect.RecordedReasons
import com.nschatz.tracker.permission.CollectionCapability
import com.nschatz.tracker.permission.LocationPermissionFlow
import com.nschatz.tracker.permission.PermissionStep
import com.nschatz.tracker.permission.SettingsReason
import com.nschatz.tracker.permission.readLocationGrants
import com.nschatz.tracker.queue.FixQueues
import com.nschatz.tracker.queue.FixUploadWorker

/**
 * The client's single screen: the **two-step permission flow**, the server configuration, and an
 * honest status readout.
 *
 * The screen exists to make the flow's steps *visible one at a time*. Android's background-location
 * grant genuinely cannot be obtained in one prompt, and an app that fires both requests and shows a
 * single "Enable" button leaves the user in a half-granted state with no idea why the trail has
 * gaps. So this shows exactly one next action, drawn from [LocationPermissionFlow.nextStep] — which
 * is pure, and unit-tested, precisely so that the decision of *which* step comes next is not buried
 * in a composable that only a device can run.
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Show the truth about the durable queue, and give a backlog left by a killed process a
        // chance to drain just because the app was opened. Both are cheap: the depth is a directory
        // listing, and the flush is unique work that a pending one absorbs.
        CollectionStatus.recordQueued(FixQueues.of(this).size())
        FixUploadWorker.enqueueFlush(this)
        // The durable recorded reason, loaded so the screen can present it. This is the ONLY thing
        // opening the app restores from disk about collection: opening it deliberately does NOT
        // start collection, and the running state it shows is a live signal, never a stored one.
        // See CollectionPresentationPolicy for why that distinction is the load-bearing one.
        CollectionState(this).publish()
        enableEdgeToEdge()
        setContent {
            TrackerTheme {
                Scaffold(modifier = Modifier.fillMaxSize()) { innerPadding ->
                    HomeScreen(modifier = Modifier.padding(innerPadding))
                }
            }
        }
    }
}

@Composable
private fun TrackerTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    val colorScheme = when {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ->
            if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)

        dark -> darkColorScheme()
        else -> lightColorScheme()
    }
    MaterialTheme(colorScheme = colorScheme, content = content)
}

@Composable
private fun HomeScreen(modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val activity = context as? Activity

    var grants by remember { mutableStateOf(readLocationGrants(context)) }

    val collectionState = remember { CollectionState(context) }
    var intentReading by remember { mutableStateOf(collectionState.intent()) }

    // rememberSaveable, NOT remember.
    //
    // This flag is the only thing that distinguishes "never asked" from "asked and permanently
    // denied" — `shouldShowRequestPermissionRationale` is false in both cases, which is precisely
    // why LocationPermissionFlow.nextStep takes it as a separate input. With a plain `remember` it
    // is lost whenever the Activity is recreated, and a screen rotation does exactly that (no
    // android:configChanges is declared, deliberately). The user who has just denied with
    // "Don't ask again" would then rotate the phone and be shown the "Allow location" button again
    // instead of the "Open settings" card — and that button is dead, because the OS has stopped
    // showing the dialog. Surviving recreation is what keeps the flow's most important branch
    // correct at the moment it matters.
    var foregroundRequested by rememberSaveable { mutableStateOf(false) }

    // Re-read the grants every time the screen comes back to the foreground. This is what makes the
    // settings round-trip work at all: on Android 11+ background location is granted on a system
    // page, so the app learns about it by observing that it resumed, never from a callback.
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) {
                grants = readLocationGrants(context)
                // The intent and the recorded reason are re-read on every resume for the same
                // reason the grants are: both can have been changed by something outside this
                // screen while it was away - the notification's Stop action, or a boot receiver in
                // a process that has since been reclaimed.
                intentReading = collectionState.intent()
                collectionState.publish()
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions(),
    ) {
        grants = readLocationGrants(context)
    }

    val sdkInt = Build.VERSION.SDK_INT
    val step = LocationPermissionFlow.nextStep(
        grants = grants,
        sdkInt = sdkInt,
        foregroundRequestedAtLeastOnce = foregroundRequested,
        foregroundRationaleAvailable = activity?.let {
            ActivityCompat.shouldShowRequestPermissionRationale(it, Manifest.permission.ACCESS_FINE_LOCATION)
        } ?: false,
    )
    val capability = LocationPermissionFlow.capability(grants)

    Column(
        modifier = modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text(stringResource(R.string.app_title), style = MaterialTheme.typography.headlineMedium)
        Text(stringResource(R.string.app_subtitle), style = MaterialTheme.typography.bodyMedium)

        PermissionCard(
            step = step,
            onRequest = { permissions ->
                foregroundRequested = true
                permissionLauncher.launch(permissions.toTypedArray())
            },
            onOpenSettings = { context.openAppSettings() },
        )

        // Degraded states, each named rather than silently tolerated.
        if (capability == CollectionCapability.FOREGROUND_ONLY) {
            WarningText(stringResource(R.string.warning_foreground_only))
        }
        // The precise-location upgrade. Both the CONDITION and the PERMISSION LIST come from
        // LocationPermissionFlow rather than being reassembled here: requesting ACCESS_FINE_LOCATION
        // without ACCESS_COARSE_LOCATION is ignored outright by Android 12+, so a hand-rolled array
        // at this call site is a button that silently does nothing on every modern phone.
        if (LocationPermissionFlow.canUpgradeToPrecise(grants)) {
            WarningText(stringResource(R.string.warning_approximate))
            TextButton(onClick = {
                foregroundRequested = true
                permissionLauncher.launch(LocationPermissionFlow.preciseUpgradePermissions().toTypedArray())
            }) { Text(stringResource(R.string.action_upgrade_precise)) }
        }
        if (!LocationPermissionFlow.notificationVisible(grants, sdkInt)) {
            WarningText(stringResource(R.string.warning_notifications_blocked))
        }

        ServerConfigCard()
        CollectionCard(
            canCollect = capability != CollectionCapability.NONE,
            // The whole of the "what is collection doing" decision, taken by a pure function that
            // is unit-tested. `CollectionStatus.running` is process-scoped memory owned by the
            // service, so it is a LIVE signal: a force-stopped or system-killed app opens in a new
            // process where it reads false, which is what stops this screen from claiming to be
            // collecting on the strength of a setting nobody has checked.
            presentation = CollectionPresentationPolicy.forOpen(
                liveSessionRunning = CollectionStatus.running,
                reading = intentReading,
                recorded = CollectionStatus.recordedReason,
            ),
            onIntent = { intent ->
                collectionState.setIntent(intent)
                intentReading = collectionState.intent()
            },
        )
    }
}

@Composable
private fun PermissionCard(
    step: PermissionStep,
    onRequest: (List<String>) -> Unit,
    onOpenSettings: () -> Unit,
) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            when (step) {
                is PermissionStep.RequestRuntime -> {
                    val background = Manifest.permission.ACCESS_BACKGROUND_LOCATION in step.permissions
                    Text(
                        stringResource(
                            if (background) R.string.permission_step_background_dialog_title
                            else R.string.permission_step_foreground_title,
                        ),
                        style = MaterialTheme.typography.titleMedium,
                    )
                    Text(
                        stringResource(
                            if (background) R.string.permission_step_background_dialog_body
                            else R.string.permission_step_foreground_body,
                        ),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    Button(onClick = { onRequest(step.permissions) }) {
                        Text(
                            stringResource(
                                if (background) R.string.permission_step_background_dialog_action
                                else R.string.permission_step_foreground_action,
                            ),
                        )
                    }
                }

                is PermissionStep.OpenAppSettings -> {
                    val background = step.reason == SettingsReason.BACKGROUND_LOCATION_NEEDS_SETTINGS
                    Text(
                        stringResource(
                            if (background) R.string.permission_step_background_settings_title
                            else R.string.permission_step_denied_title,
                        ),
                        style = MaterialTheme.typography.titleMedium,
                    )
                    Text(
                        stringResource(
                            if (background) R.string.permission_step_background_settings_body
                            else R.string.permission_step_denied_body,
                        ),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    Button(onClick = onOpenSettings) {
                        Text(
                            stringResource(
                                if (background) R.string.permission_step_background_settings_action
                                else R.string.permission_step_denied_action,
                            ),
                        )
                    }
                }

                PermissionStep.Ready -> {
                    Text(
                        stringResource(R.string.permission_ready_title),
                        style = MaterialTheme.typography.titleMedium,
                    )
                    Text(
                        stringResource(R.string.permission_ready_body),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
            }
        }
    }
}

@Composable
private fun ServerConfigCard() {
    val context = LocalContext.current
    val prefs = remember { ClientPreferences(context) }
    var url by remember { mutableStateOf(prefs.baseUrl.orEmpty()) }
    var credential by remember { mutableStateOf(prefs.deviceToken.orEmpty()) }
    var message by remember { mutableStateOf<String?>(null) }

    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.config_title), style = MaterialTheme.typography.titleMedium)
            OutlinedTextField(
                value = url,
                onValueChange = { url = it },
                label = { Text(stringResource(R.string.config_url_label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = credential,
                onValueChange = { credential = it },
                label = { Text(stringResource(R.string.config_token_label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            Text(
                stringResource(R.string.config_plaintext_note),
                style = MaterialTheme.typography.bodySmall,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = {
                    prefs.baseUrl = url
                    prefs.deviceToken = credential
                    // Report the validated verdict, not a blanket "Saved": a URL the client will
                    // refuse to use must say so here, not fail silently at the first fix.
                    message = when (val status = prefs.readConfig()) {
                        is ConfigStatus.Configured -> {
                            // A usable configuration is the one thing that unblocks a queue parked
                            // by a `Result.failure()` for want of a URL or a token, so ask for a
                            // flush the moment one exists.
                            FixUploadWorker.enqueueFlush(context)
                            context.getString(R.string.config_saved)
                        }

                        is ConfigStatus.Incomplete -> status.reason
                    }
                }) { Text(stringResource(R.string.config_save)) }
            }
            message?.let { Text(it, style = MaterialTheme.typography.bodySmall) }
        }
    }
}

@Composable
private fun CollectionCard(
    canCollect: Boolean,
    presentation: CollectionPresentation,
    onIntent: (CollectionIntent) -> Unit,
) {
    val context = LocalContext.current
    val running = presentation.running

    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.collection_title), style = MaterialTheme.typography.titleMedium)
            Text(
                stringResource(if (running) R.string.collection_running else R.string.collection_stopped),
                style = MaterialTheme.typography.bodyMedium,
            )
            // ALONGSIDE the running state, whichever state that is. A phone that is collecting but
            // has not managed a position since it restarted shows "Running." AND this line; a phone
            // whose automatic start was refused shows "Stopped." AND this line. Presenting a reason
            // only when stopped would hide the case that exists precisely while collection runs.
            presentation.reason?.let { WarningText(RecordedReasons.message(it)) }
            // Asked for, not running, and nothing recorded to say why: no code of ours ran, which
            // is what a force-stop or an OEM battery manager looks like from in here. Saying so is
            // the honest answer; a blank space would read as "fine".
            if (presentation.unexplainedStop) {
                WarningText(stringResource(R.string.collection_stopped_unexplained))
            }
            Button(
                enabled = canCollect,
                onClick = {
                    // The operator's explicit choice, recorded BEFORE the service is asked to do
                    // anything. It is the fact the next boot reads, and a start whose intent was
                    // never written is a phone that silently does not come back.
                    if (running) {
                        onIntent(CollectionIntent.OFF)
                        LocationCollectionService.stop(context)
                    } else {
                        onIntent(CollectionIntent.ON)
                        LocationCollectionService.start(context)
                    }
                },
            ) {
                Text(stringResource(if (running) R.string.collection_stop else R.string.collection_start))
            }

            // The counters. `dropped` is shown with the same prominence as `delivered` on purpose:
            // a client that only ever displays its successes is how silent fix loss stays silent.
            // `queued` sits between them because it is the number that distinguishes the two — a
            // rising queue during an outage means nothing has been lost, which is the guarantee C2
            // adds and the thing a worried user most needs to be able to see.
            if (CollectionStatus.lastFixAtMillis == 0L &&
                CollectionStatus.delivered == 0 &&
                CollectionStatus.queued == 0
            ) {
                Text(stringResource(R.string.collection_no_fix_yet), style = MaterialTheme.typography.bodySmall)
            } else {
                Text(
                    "delivered: ${CollectionStatus.delivered}   " +
                        "queued: ${CollectionStatus.queued}   " +
                        "dropped: ${CollectionStatus.dropped}",
                    style = MaterialTheme.typography.bodySmall,
                )
            }
            CollectionStatus.lastError?.let { WarningText(it) }
            Text(stringResource(R.string.collection_limitation), style = MaterialTheme.typography.bodySmall)
        }
    }
}

@Composable
private fun WarningText(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.error,
    )
}

/**
 * Opens this app's system settings page.
 *
 * The only route to "Allow all the time" on Android 11+, and the only route to any location grant
 * once the user has denied firmly enough that the OS stops showing the prompt.
 */
private fun Context.openAppSettings() {
    val intent = Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
        data = Uri.fromParts("package", packageName, null)
        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    }
    startActivity(intent)
}

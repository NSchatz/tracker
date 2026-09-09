package com.nschatz.tracker.ui

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.border
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import com.nschatz.tracker.R
import com.nschatz.tracker.collect.ClientPreferences
import com.nschatz.tracker.collect.CollectionStatus
import com.nschatz.tracker.collect.ConfigStatus
import com.nschatz.tracker.collect.LocationCollectionService
import com.nschatz.tracker.collect.TroubleKind
import com.nschatz.tracker.permission.CollectionCapability
import com.nschatz.tracker.permission.LocationGrants
import com.nschatz.tracker.permission.LocationPermissionFlow
import com.nschatz.tracker.permission.PermissionStep
import com.nschatz.tracker.permission.SettingsReason
import com.nschatz.tracker.queue.FixQueues
import com.nschatz.tracker.queue.FixUploadWorker
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * The client's single screen: the **two-step permission flow**, the server configuration, and an
 * honest status readout — plus the explanation destination each card's one affordance opens.
 *
 * The screen exists to make the flow's steps *visible one at a time*. Android's background-location
 * grant genuinely cannot be obtained in one prompt, and an app that fires both requests and shows a
 * single "Enable" button leaves the user in a half-granted state with no idea why the trail has
 * gaps. So this shows exactly one next action, drawn from [LocationPermissionFlow.nextStep] — which
 * is pure, and unit-tested, precisely so that the decision of *which* step comes next is not buried
 * in a composable that only a device can run.
 *
 * ### Why the paragraphs are not on this screen any more
 *
 * F8 of the umbrella's frontend conventions: scope labels stay to a few words on the surface and the
 * paragraphs explaining them live in the repository's docs, linked once per region. Four full
 * paragraphs used to stand on this screen. They have not been dropped — they are in
 * [ExplanationTopic], reachable from one affordance per card, and committed as a document at
 * `internal/server/static/app-explained.html`.
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Show the truth about the durable queue, and give a backlog left by a killed process a
        // chance to drain just because the app was opened. The depth is READ, and a read that fails
        // is reported as a failure rather than as an empty queue.
        CollectionStatus.recordQueued(FixQueues.of(this).depth())
        FixUploadWorker.enqueueFlush(this)
        val mutation = UiMutation.from(intent)
        enableEdgeToEdge()
        setContent {
            TrackerTheme {
                Scaffold(modifier = Modifier.fillMaxSize()) { innerPadding ->
                    TrackerApp(mutation = mutation, modifier = Modifier.padding(innerPadding))
                }
            }
        }
    }
}

/**
 * The colour scheme, authored per theme rather than derived from the wallpaper.
 *
 * Dynamic colour (API 31+) made this screen's contrast a property of whatever picture the user had
 * set, which is not a thing any check can grade: F1 requires WCAG 2.2 AA contrast and F10 requires
 * it graded in BOTH themes, and neither is provable when the palette is generated at runtime from an
 * image. These pairs are fixed, and the instrumented suite measures them on a real emulator in both
 * themes with the platform accessibility checks enabled.
 */
private val LightColors = androidx.compose.material3.lightColorScheme(
    primary = Color(0xFF0B4FA8),
    onPrimary = Color(0xFFFFFFFF),
    secondary = Color(0xFF0A6B3D),
    onSecondary = Color(0xFFFFFFFF),
    background = Color(0xFFFFFFFF),
    onBackground = Color(0xFF14181F),
    surface = Color(0xFFFFFFFF),
    onSurface = Color(0xFF14181F),
    surfaceVariant = Color(0xFFEDEFF2),
    onSurfaceVariant = Color(0xFF3A4149),
    error = Color(0xFF9E1017),
    onError = Color(0xFFFFFFFF),
    outline = Color(0xFF5A6169),
)

private val DarkColors = androidx.compose.material3.darkColorScheme(
    primary = Color(0xFF8EC2FF),
    onPrimary = Color(0xFF00264D),
    secondary = Color(0xFF64DDA4),
    onSecondary = Color(0xFF00301B),
    background = Color(0xFF12161B),
    onBackground = Color(0xFFE9EDF2),
    surface = Color(0xFF1C2229),
    onSurface = Color(0xFFE9EDF2),
    surfaceVariant = Color(0xFF262E36),
    onSurfaceVariant = Color(0xFFCBD3DB),
    error = Color(0xFFFFB3AB),
    onError = Color(0xFF48000A),
    outline = Color(0xFF9AA4AE),
)

@Composable
private fun TrackerTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    MaterialTheme(colorScheme = if (dark) DarkColors else LightColors, content = content)
}

/**
 * A focus indicator that is a PAINTED thing rather than a platform default.
 *
 * F1 requires a visible focus indicator, and "visible" has to mean rendered pixels a check can
 * measure. Compose's default focus indication for a Material button is a low-opacity overlay that a
 * screenshot diff can miss entirely, so every control this screen owns gets an explicit ring. The
 * border is always laid out, transparent when unfocused, so gaining focus never moves anything.
 */
@Composable
private fun Modifier.focusRing(): Modifier {
    var focused by remember { mutableStateOf(false) }
    val colour = if (focused) MaterialTheme.colorScheme.primary else Color.Transparent
    return this
        .onFocusChanged { focused = it.isFocused || it.hasFocus }
        .border(3.dp, colour, RoundedCornerShape(6.dp))
        .padding(2.dp)
}

/** The minimum a finger can reliably hit, and the floor WCAG 2.2 sets for a target. */
private fun Modifier.minimumTarget(): Modifier = this.defaultMinSize(minWidth = 48.dp, minHeight = 48.dp)

/** Which explanation the destination is showing, or null for the home screen. */
enum class ExplanationTopic { PERMISSIONS, SERVER, COUNTERS }

@Composable
private fun TrackerApp(mutation: UiMutation, modifier: Modifier = Modifier) {
    var topic by rememberSaveable { mutableStateOf<ExplanationTopic?>(null) }
    val current = topic
    if (current == null) {
        HomeScreen(mutation = mutation, onExplain = { topic = it }, modifier = modifier)
    } else {
        BackHandler { topic = null }
        ExplanationScreen(topic = current, onBack = { topic = null }, modifier = modifier)
    }
}

@Composable
private fun HomeScreen(
    mutation: UiMutation,
    onExplain: (ExplanationTopic) -> Unit,
    modifier: Modifier = Modifier,
) {
    val context = LocalContext.current
    val activity = context as? Activity

    var grants by remember { mutableStateOf(readGrants(context)) }

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
            if (event == Lifecycle.Event.ON_RESUME) grants = readGrants(context)
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions(),
    ) {
        grants = readGrants(context)
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
            .then(if (mutation == UiMutation.OVERFLOWING_LAYOUT) Modifier.requiredWidth(700.dp) else Modifier)
            .padding(16.dp)
            .testTag("home"),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text(stringResource(R.string.app_title), style = MaterialTheme.typography.headlineMedium)
        Text(stringResource(R.string.app_subtitle), style = MaterialTheme.typography.bodyMedium)

        PermissionCard(
            step = step,
            capability = capability,
            grants = grants,
            sdkInt = sdkInt,
            mutation = mutation,
            onRequest = { permissions ->
                foregroundRequested = true
                permissionLauncher.launch(permissions.toTypedArray())
            },
            onOpenSettings = { context.openAppSettings() },
            onUpgradePrecise = {
                foregroundRequested = true
                permissionLauncher.launch(LocationPermissionFlow.preciseUpgradePermissions().toTypedArray())
            },
            onExplain = { onExplain(ExplanationTopic.PERMISSIONS) },
        )

        ServerConfigCard(mutation = mutation, onExplain = { onExplain(ExplanationTopic.SERVER) })
        CollectionCard(
            canCollect = capability != CollectionCapability.NONE,
            mutation = mutation,
            onExplain = { onExplain(ExplanationTopic.COUNTERS) },
        )
    }
}

@Composable
private fun PermissionCard(
    step: PermissionStep,
    capability: CollectionCapability,
    grants: LocationGrants,
    sdkInt: Int,
    mutation: UiMutation,
    onRequest: (List<String>) -> Unit,
    onOpenSettings: () -> Unit,
    onUpgradePrecise: () -> Unit,
    onExplain: () -> Unit,
) {
    Card(modifier = Modifier.fillMaxWidth().testTag("card-permissions")) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            when (step) {
                is PermissionStep.RequestRuntime -> {
                    val background = Manifest.permission.ACCESS_BACKGROUND_LOCATION in step.permissions
                    CardTitle(
                        if (background) R.string.permission_step_background_dialog_title
                        else R.string.permission_step_foreground_title,
                    )
                    Label(
                        short = if (background) R.string.permission_step_background_dialog_body
                        else R.string.permission_step_foreground_body,
                        long = if (background) R.string.explain_permission_background_dialog
                        else R.string.explain_permission_foreground,
                        mutation = mutation,
                        tag = "permission-body",
                    )
                    RingedButton(
                        onClick = { onRequest(step.permissions) },
                        tag = "permission-action",
                    ) {
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
                    CardTitle(
                        if (background) R.string.permission_step_background_settings_title
                        else R.string.permission_step_denied_title,
                    )
                    Label(
                        short = if (background) R.string.permission_step_background_settings_body
                        else R.string.permission_step_denied_body,
                        long = if (background) R.string.explain_permission_background_settings
                        else R.string.explain_permission_denied,
                        mutation = mutation,
                        tag = "permission-body",
                    )
                    RingedButton(onClick = onOpenSettings, tag = "permission-action") {
                        Text(
                            stringResource(
                                if (background) R.string.permission_step_background_settings_action
                                else R.string.permission_step_denied_action,
                            ),
                        )
                    }
                }

                PermissionStep.Ready -> {
                    CardTitle(R.string.permission_ready_title)
                    Label(
                        short = R.string.permission_ready_body,
                        long = R.string.explain_permission_ready,
                        mutation = mutation,
                        tag = "permission-body",
                    )
                }
            }

            // Degraded states, each named in WORDS rather than carried by the colour of the text.
            if (capability == CollectionCapability.FOREGROUND_ONLY) {
                WarningText(R.string.warning_foreground_only, mutation, "warning-foreground-only")
            }
            // The precise-location upgrade. Both the CONDITION and the PERMISSION LIST come from
            // LocationPermissionFlow rather than being reassembled here: requesting ACCESS_FINE_LOCATION
            // without ACCESS_COARSE_LOCATION is ignored outright by Android 12+, so a hand-rolled array
            // at this call site is a button that silently does nothing on every modern phone.
            if (LocationPermissionFlow.canUpgradeToPrecise(grants)) {
                WarningText(R.string.warning_approximate, mutation, "warning-approximate")
                TextButton(
                    onClick = onUpgradePrecise,
                    modifier = Modifier.focusRing().minimumTarget().testTag("action-precise"),
                ) { Text(stringResource(R.string.action_upgrade_precise)) }
            }
            if (!LocationPermissionFlow.notificationVisible(grants, sdkInt)) {
                WarningText(R.string.warning_notifications_blocked, mutation, "warning-notifications")
            }

            ExplainAffordance(R.string.explain_permissions, onExplain, "explain-permissions")
        }
    }
}

@Composable
private fun ServerConfigCard(mutation: UiMutation, onExplain: () -> Unit) {
    val context = LocalContext.current
    val prefs = remember { ClientPreferences(context) }
    var url by rememberSaveable { mutableStateOf(prefs.baseUrl.orEmpty()) }
    var credential by rememberSaveable { mutableStateOf(prefs.deviceToken.orEmpty()) }
    var message by rememberSaveable { mutableStateOf<String?>(null) }
    var refused by rememberSaveable { mutableStateOf(false) }

    Card(modifier = Modifier.fillMaxWidth().testTag("card-server")) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            CardTitle(R.string.config_title)
            OutlinedTextField(
                value = url,
                onValueChange = { url = it },
                label = { Text(stringResource(R.string.config_url_label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth().focusRing().testTag("field-url"),
            )
            OutlinedTextField(
                value = credential,
                onValueChange = { credential = it },
                label = { Text(stringResource(R.string.config_token_label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth().focusRing().testTag("field-token"),
            )
            Label(
                short = R.string.config_plaintext_note,
                long = R.string.explain_config_plaintext,
                mutation = mutation,
                tag = "config-note",
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                RingedButton(
                    onClick = {
                        prefs.baseUrl = url
                        prefs.deviceToken = credential
                        // Report the validated verdict, not a blanket "Saved": a URL the client will
                        // refuse to use must say so here, not fail silently at the first fix. And the
                        // refusal is a WORD, not a colour — "Not saved" leads the verdict.
                        //
                        // What the card draws is the SUMMARY, a few words. The sentence is behind
                        // "About server settings", which is what F8 asks for and what
                        // explain_config_verdict has always promised. The mutation branch draws the
                        // sentence instead, so the brevity assertion can be shown going red against
                        // a state that is not on the screen when it opens.
                        when (val status = prefs.readConfig()) {
                            is ConfigStatus.Configured -> {
                                // A usable configuration is the one thing that unblocks a queue parked
                                // by a `Result.failure()` for want of a URL or a token, so ask for a
                                // flush the moment one exists.
                                FixUploadWorker.enqueueFlush(context)
                                refused = false
                                message = context.getString(R.string.config_saved)
                            }

                            is ConfigStatus.Incomplete -> {
                                refused = true
                                val verdict = if (mutation == UiMutation.PROSE_IN_A_DEGRADED_STATE) {
                                    status.reason
                                } else {
                                    status.summary
                                }
                                message = context.getString(R.string.config_not_saved) + ": " + verdict
                            }
                        }
                    },
                    tag = "action-save",
                    focusable = mutation != UiMutation.SAVE_NOT_FOCUSABLE,
                ) { Text(stringResource(R.string.config_save)) }
            }
            message?.let {
                Text(
                    text = it,
                    style = MaterialTheme.typography.bodySmall,
                    color = if (refused) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurface,
                    modifier = Modifier.testTag("config-verdict"),
                )
            }
            ExplainAffordance(R.string.explain_server, onExplain, "explain-server")
        }
    }
}

@Composable
private fun CollectionCard(canCollect: Boolean, mutation: UiMutation, onExplain: () -> Unit) {
    val context = LocalContext.current
    val running = CollectionStatus.running

    // The screen must be able to change what it says WITHOUT an interaction and without a fix
    // arriving: a run that has gone quiet becomes "last known" purely because time passed. Compose
    // has no clock, so this is it.
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            kotlinx.coroutines.delay(2_000)
            now = System.currentTimeMillis()
        }
    }
    val readout = CollectionReadout.of(CollectionStatus, now)

    Card(modifier = Modifier.fillMaxWidth().testTag("card-collection")) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            CardTitle(R.string.collection_title)
            Text(
                stringResource(if (running) R.string.collection_running else R.string.collection_stopped),
                style = MaterialTheme.typography.bodyMedium,
                color = if (mutation == UiMutation.LOW_CONTRAST_STATUS) {
                    Color(0xFFBFC6CC)
                } else {
                    MaterialTheme.colorScheme.onSurface
                },
                modifier = Modifier.testTag("collection-running"),
            )
            RingedButton(
                onClick = {
                    if (running) LocationCollectionService.stop(context)
                    else LocationCollectionService.start(context)
                },
                enabled = canCollect,
                tag = "action-collection",
            ) {
                Text(stringResource(if (running) R.string.collection_stop else R.string.collection_start))
            }

            // ONE state, decided in one place (CollectionReadout), so two of the three can never be
            // on the screen at once.
            val blanked = mutation == UiMutation.UNREADABLE_QUEUE_BLANKS_CARD &&
                readout.state == CollectionCardState.UNREADABLE
            if (!blanked) {
                Text(
                    text = stringResource(cardStateLabel(readout.state, mutation)),
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier
                        .testTag("collection-state")
                        .semantics { contentDescription = "collection state: " + readout.state.name },
                )

                if (readout.state != CollectionCardState.UNKNOWN) {
                    CounterRow(
                        R.string.counter_delivered_label,
                        readout.delivered,
                        setLabel = runSetLabel(readout.fromMillis, mutation),
                        tag = "counter-delivered",
                        mutation = mutation,
                    )
                    CounterRow(
                        R.string.counter_queued_label,
                        readout.queued,
                        setLabel = diskSetLabel(readout.asOfMillis, mutation),
                        tag = "counter-queued",
                        mutation = mutation,
                    )
                    CounterRow(
                        R.string.counter_dropped_label,
                        readout.dropped,
                        setLabel = runSetLabel(readout.fromMillis, mutation),
                        tag = "counter-dropped",
                        mutation = mutation,
                    )
                    // F6: current, or last known at a stated time. Never a frozen readout presented
                    // as a live one.
                    val fresh = readout.current || mutation == UiMutation.ALWAYS_CURRENT
                    Text(
                        text = if (fresh) {
                            stringResource(R.string.counters_current)
                        } else {
                            stringResource(R.string.counters_last_known, clockOf(readout.asOfMillis))
                        },
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.testTag("counters-freshness"),
                    )
                }
            }

            // The trouble is named in a few WORDS drawn from a closed set; the sentence behind it -
            // which may be a socket failure or a platform exception, and so has no bound this
            // package can impose - is read behind "About these counters". The mutation branch draws
            // the sentence, which is the F8 defect reproduced so the floor can be shown catching it.
            CollectionStatus.lastTrouble?.let { kind ->
                if (mutation == UiMutation.PROSE_IN_A_DEGRADED_STATE) {
                    WarningLiteral(CollectionStatus.lastError.orEmpty(), mutation, "collection-error")
                } else {
                    WarningText(troubleLabel(kind), mutation, "collection-error")
                }
            }
            if (mutation == UiMutation.PARAGRAPHS_ON_SURFACE) {
                Text(stringResource(R.string.explain_collection_limitation), style = MaterialTheme.typography.bodySmall)
            }
            if (mutation != UiMutation.NO_EXPLANATION_AFFORDANCE) {
                ExplainAffordance(R.string.explain_counters, onExplain, "explain-counters")
            }
        }
    }
}

/**
 * The few words the collection card draws for a trouble.
 *
 * Exhaustive over [TroubleKind] and returning a string resource, which is the structural half of
 * F8: the surface CANNOT draw a domain sentence here without changing this signature, and a kind
 * added without a label fails to compile rather than arriving on the screen as a paragraph.
 */
private fun troubleLabel(kind: TroubleKind): Int = when (kind) {
    TroubleKind.NOT_CONFIGURED -> R.string.trouble_not_configured
    TroubleKind.PERMISSION_LOST -> R.string.trouble_permission_lost
    TroubleKind.SERVICE_REFUSED -> R.string.trouble_service_refused
    TroubleKind.DELIVERY_FAILED -> R.string.trouble_delivery_failed
    TroubleKind.CREDENTIAL_REJECTED -> R.string.trouble_credential_rejected
}

private fun cardStateLabel(state: CollectionCardState, mutation: UiMutation): Int {
    if (mutation == UiMutation.STATES_INDISTINGUISHABLE) return R.string.collection_state_reporting
    return when (state) {
        CollectionCardState.UNKNOWN -> R.string.collection_state_unknown
        CollectionCardState.NOTHING_YET -> R.string.collection_state_nothing_yet
        CollectionCardState.UNREADABLE -> R.string.collection_state_unreadable
        CollectionCardState.REPORTING -> R.string.collection_state_reporting
    }
}

@Composable
private fun runSetLabel(fromMillis: Long?, mutation: UiMutation): String {
    if (mutation == UiMutation.COUNTERS_WITHOUT_THEIR_SET) return ""
    return if (fromMillis == null) {
        stringResource(R.string.counter_set_this_run_unstarted)
    } else {
        stringResource(R.string.counter_set_this_run, clockOf(fromMillis))
    }
}

@Composable
private fun diskSetLabel(asOfMillis: Long?, mutation: UiMutation): String {
    if (mutation == UiMutation.COUNTERS_WITHOUT_THEIR_SET) return ""
    return if (asOfMillis == null) {
        stringResource(R.string.counter_set_on_disk_unread)
    } else {
        stringResource(R.string.counter_set_on_disk, clockOf(asOfMillis))
    }
}

@Composable
private fun CounterRow(
    labelRes: Int,
    figure: Figure,
    setLabel: String,
    tag: String,
    mutation: UiMutation,
) {
    val value = when (figure) {
        is Figure.Measured -> figure.value.toString()
        Figure.NotRecorded ->
            if (mutation == UiMutation.NOT_RECORDED_AS_ZERO) "0" else stringResource(R.string.figure_not_recorded)

        Figure.Unavailable ->
            if (mutation == UiMutation.NOT_RECORDED_AS_ZERO) "0" else stringResource(R.string.figure_unavailable)
    }
    Column(modifier = Modifier.testTag(tag)) {
        Text(
            text = stringResource(labelRes) + " " + value,
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.testTag("$tag-value"),
        )
        if (setLabel.isNotEmpty()) {
            Text(
                text = setLabel,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.testTag("$tag-set"),
            )
        }
    }
}

@Composable
private fun CardTitle(res: Int) {
    Text(stringResource(res), style = MaterialTheme.typography.titleMedium)
}

/**
 * A label on the surface, with the paragraph it replaced named beside it.
 *
 * The mutation branch is the demonstration AC18 needs: it puts the paragraph back on the surface so
 * the brevity assertion can be shown going red.
 */
@Composable
private fun Label(short: Int, long: Int, mutation: UiMutation, tag: String) {
    Text(
        text = stringResource(if (mutation == UiMutation.PARAGRAPHS_ON_SURFACE) long else short),
        style = MaterialTheme.typography.bodyMedium,
        modifier = Modifier.testTag(tag),
    )
}

/**
 * A warning, readable without seeing its colour.
 *
 * F1 forbids a state that is only conveyed by colour. The error paint is reinforcement; the word
 * "Warning" is what actually carries the state, and the mutation branch removes it so the assertion
 * that the word is there can be shown going red.
 */
@Composable
private fun WarningText(res: Int, mutation: UiMutation, tag: String) {
    WarningLiteral(stringResource(res), mutation, tag)
}

@Composable
private fun WarningLiteral(text: String, mutation: UiMutation, tag: String) {
    val prefix = if (mutation == UiMutation.WARNING_BY_COLOUR_ONLY) "" else stringResource(R.string.warning_prefix) + ": "
    Text(
        text = prefix + text,
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.error,
        modifier = Modifier.testTag(tag),
    )
}

/** One affordance per card, opening this app's own explanation for it. */
@Composable
private fun ExplainAffordance(labelRes: Int, onClick: () -> Unit, tag: String) {
    TextButton(
        onClick = onClick,
        modifier = Modifier.focusRing().minimumTarget().testTag(tag),
    ) { Text(stringResource(labelRes)) }
}

@Composable
private fun RingedButton(
    onClick: () -> Unit,
    tag: String,
    enabled: Boolean = true,
    focusable: Boolean = true,
    content: @Composable () -> Unit,
) {
    val base = Modifier.minimumTarget().testTag(tag)
    val withRing = if (focusable) base.focusRing() else base.then(
        Modifier.focusProperties { canFocus = false },
    )
    Button(onClick = onClick, enabled = enabled, modifier = withRing) { content() }
}

@Composable
private fun ExplanationScreen(topic: ExplanationTopic, onBack: () -> Unit, modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val prefs = remember { ClientPreferences(context) }

    // The sentence the home screen deliberately does not draw, read at the moment this destination
    // opens. The server verdict is re-derived from what is stored rather than carried down from the
    // card, because Save writes before it validates: the same input produces the same verdict, and
    // there is no second copy of it to drift.
    val detail: String? = when (topic) {
        ExplanationTopic.SERVER -> (prefs.readConfig() as? ConfigStatus.Incomplete)?.reason
        ExplanationTopic.COUNTERS -> CollectionStatus.lastError
        ExplanationTopic.PERMISSIONS -> null
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp)
            .testTag("explanation"),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(
            stringResource(
                when (topic) {
                    ExplanationTopic.PERMISSIONS -> R.string.explanation_title_permissions
                    ExplanationTopic.SERVER -> R.string.explanation_title_server
                    ExplanationTopic.COUNTERS -> R.string.explanation_title_counters
                },
            ),
            style = MaterialTheme.typography.headlineSmall,
            modifier = Modifier.testTag("explanation-title"),
        )
        detail?.let {
            Text(
                text = stringResource(R.string.explanation_detail_heading) + ": " + it,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.testTag("explanation-detail"),
            )
        }
        for (res in explanationParagraphs(topic)) {
            Text(stringResource(res), style = MaterialTheme.typography.bodyMedium)
        }
        TextButton(
            onClick = onBack,
            modifier = Modifier.focusRing().minimumTarget().testTag("explanation-back"),
        ) { Text(stringResource(R.string.explanation_back)) }
    }
}

/**
 * The paragraphs that used to stand on the home screen, in the order they used to stand there.
 *
 * They are LISTED rather than concatenated into one resource so that the record check can name the
 * one that went missing rather than reporting that a wall of text got shorter.
 */
private fun explanationParagraphs(topic: ExplanationTopic): List<Int> = when (topic) {
    ExplanationTopic.PERMISSIONS -> listOf(
        R.string.explain_permission_foreground,
        R.string.explain_permission_background_dialog,
        R.string.explain_permission_background_settings,
        R.string.explain_permission_denied,
        R.string.explain_permission_ready,
        R.string.explain_warning_foreground_only,
        R.string.explain_warning_approximate,
        R.string.explain_warning_notifications_blocked,
    )

    ExplanationTopic.SERVER -> listOf(
        R.string.explain_config_plaintext,
        R.string.explain_config_verdict,
    )

    ExplanationTopic.COUNTERS -> listOf(
        R.string.explain_collection_trouble,
        R.string.explain_collection_limitation,
        R.string.explain_counter_delivered,
        R.string.explain_counter_queued,
        R.string.explain_counter_dropped,
        R.string.explain_counter_not_recorded,
        R.string.explain_collection_freshness,
    )
}

private val clockFormat = SimpleDateFormat("HH:mm", Locale.getDefault())

private fun clockOf(millis: Long?): String =
    if (millis == null) "--:--" else clockFormat.format(Date(millis))

/**
 * Reads the current grant state from the OS.
 *
 * The framework edge of the permission flow: `checkSelfPermission` is the only Android call
 * involved, and everything decided from its result lives in the pure [LocationPermissionFlow].
 */
private fun readGrants(context: Context): LocationGrants = LocationGrants(
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

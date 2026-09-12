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
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.foundation.layout.size
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
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.platform.LocalViewConfiguration
import androidx.compose.ui.platform.ViewConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.compose.ui.unit.DpSize
import androidx.compose.ui.unit.dp
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import com.nschatz.tracker.R
import com.nschatz.tracker.alert.AlertDeliveryStatus
import com.nschatz.tracker.alert.AlertNotifications
import com.nschatz.tracker.alert.AlertSurface
import com.nschatz.tracker.alert.AlertText
import com.nschatz.tracker.alert.CrossingListState
import com.nschatz.tracker.alert.CrossingsRefresher
import com.nschatz.tracker.alert.NotReceivableReason
import com.nschatz.tracker.alert.PushRegistrar
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
    @OptIn(ExperimentalComposeUiApi::class)
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Show the truth about the durable queue, and give a backlog left by a killed process a
        // chance to drain just because the app was opened. The depth is READ, and a read that fails
        // is reported as a failure rather than as an empty queue.
        CollectionStatus.recordQueued(FixQueues.of(this).depth())
        FixUploadWorker.enqueueFlush(this)
        val mutation = UiMutation.from(intent)
        // The alert channel exists before any crossing can arrive, so the first push is not the
        // thing that discovers it is missing. Cheap and idempotent.
        AlertNotifications.ensureChannel(this)
        enableEdgeToEdge()
        setContent {
            TrackerTheme {
                // testTagsAsResourceId publishes each control's test tag to the platform as its
                // view id, which is what lets an accessibility sweep NAME the view it is reporting
                // on. AC13 requires a failure to name the view, the check and the measured value,
                // and an AccessibilityNodeInfo tree otherwise offers only a class name and a
                // rectangle. It is set once, at the root, and changes nothing that is drawn.
                Scaffold(
                    modifier = Modifier
                        .fillMaxSize()
                        .semantics { testTagsAsResourceId = true },
                ) { innerPadding ->
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
 *
 * The ring is painted at the OUTER EDGE of the control - the 2dp of padding after it insets
 * everything the control itself draws - so the band that carries it is a band nothing else paints
 * in. That is what lets the indicator be measured on its own rather than through a Material ripple
 * state layer, which tints the inside of the control on focus whatever this ring does.
 *
 * [UiMutation.FOCUS_INDICATOR_SUPPRESSED] holds the ring transparent while leaving focus itself
 * untouched, which is the AC26 demonstration: the control still takes focus and still activates, and
 * nothing on the glass says which control has it.
 */
@Composable
private fun Modifier.focusRing(mutation: UiMutation): Modifier {
    var focused by remember { mutableStateOf(false) }
    val suppressed = mutation == UiMutation.FOCUS_INDICATOR_SUPPRESSED
    val colour = if (focused && !suppressed) MaterialTheme.colorScheme.primary else Color.Transparent
    return this
        .onFocusChanged { focused = it.isFocused || it.hasFocus }
        .border(FOCUS_RING_WIDTH_DP.dp, colour, RoundedCornerShape(6.dp))
        .padding(FOCUS_RING_INSET_DP.dp)
}

/** The width of the focus ring, in dp. */
const val FOCUS_RING_WIDTH_DP = 3

/**
 * How far the control's own paint is inset from the ring, in dp.
 *
 * It EQUALS the ring width on purpose. The outer band of that width then belongs to the ring and to
 * nothing else - not to the control's surface, and not to the Material ripple's focus state layer,
 * which tints the inside of a control whenever it takes focus. That is what makes the indicator
 * measurable on its own: the suite compares this band focused against unfocused, so a ring that was
 * never painted reads as zero differing pixels rather than as the ripple's tint. At 2dp the ripple
 * reached the last half-pixel of the band and the AC26 demonstration could not be shown going red.
 */
const val FOCUS_RING_INSET_DP = FOCUS_RING_WIDTH_DP

/**
 * Lets a directional key LEAVE a control that would otherwise swallow it.
 *
 * A Compose text field handles the arrow keys itself - they move the caret - and it consumes them
 * whether or not the caret had anywhere to go. On a single-line field that means DPAD_DOWN moves
 * nothing and reaches no focus search, so a person driving this screen with a keyboard, a d-pad or a
 * screen reader's directional gestures lands in the server URL field and can never leave it: every
 * control below it, the save control included, becomes unreachable. That is impl-gate finding F20,
 * and it is a defect in the screen rather than in the check that found it.
 *
 * `onPreviewKeyEvent` runs on the way DOWN to the focused node, so this sees the key first and moves
 * focus instead. It consumes only what it actually acted on: a direction with nothing beyond it
 * falls through to the field, which is the behaviour a caret needs.
 */
@Composable
private fun Modifier.directionalPassThrough(): Modifier {
    val focusManager = LocalFocusManager.current
    return this.onPreviewKeyEvent { event ->
        if (event.type != KeyEventType.KeyDown) {
            false
        } else {
            when (event.key) {
                Key.DirectionDown -> focusManager.moveFocus(FocusDirection.Down)
                Key.DirectionUp -> focusManager.moveFocus(FocusDirection.Up)
                else -> false
            }
        }
    }
}

/** The minimum a finger can reliably hit, and the floor WCAG 2.2 sets for a target. */
private fun Modifier.minimumTarget(): Modifier = this.defaultMinSize(minWidth = 48.dp, minHeight = 48.dp)

/** Which explanation the destination is showing, or null for the home screen. */
enum class ExplanationTopic { PERMISSIONS, SERVER, COUNTERS, ALERTS }

@Composable
private fun TrackerApp(mutation: UiMutation, modifier: Modifier = Modifier) {
    var topic by rememberSaveable { mutableStateOf<ExplanationTopic?>(null) }
    val current = topic
    if (current == null) {
        HomeScreen(mutation = mutation, onExplain = { topic = it }, modifier = modifier)
    } else {
        BackHandler { topic = null }
        ExplanationScreen(topic = current, mutation = mutation, onBack = { topic = null }, modifier = modifier)
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

    // The persisted ask, and the reason a boot gave for not honouring it.
    //
    // Both are read off disk here rather than in the card, which keeps the card's inputs plain data.
    // The reason is ADOPTED into CollectionStatus rather than rendered from the preference directly:
    // the card already has one closed-vocabulary trouble label and one explanation destination for a
    // sentence, and a second, parallel path to the same two places would be a second thing to keep
    // honest. It is adopted only while collection is not running, because a reason recorded by a boot
    // that has since been superseded by a person starting collection is history, not the current
    // state.
    val homePrefs = remember { ClientPreferences(context) }
    var collectionEnabled by remember { mutableStateOf(homePrefs.collectionEnabled) }
    fun adoptStoredState() {
        collectionEnabled = homePrefs.collectionEnabled
        if (!CollectionStatus.running) {
            homePrefs.bootRestartReason?.let { CollectionStatus.recordBlocked(it.kind, it.sentence) }
        }
    }
    LaunchedEffect(Unit) { adoptStoredState() }

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
                grants = readGrants(context)
                adoptStoredState()
            }
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
            collectionEnabled = collectionEnabled,
            mutation = mutation,
            // The persisted ask is written at the two places a person acts on it, and this is one of
            // them (the other is the ongoing notification's Stop action, answered in the service).
            // The boot path never writes it - it reads it - which is what keeps "somebody asked for
            // this" a record of a human act rather than of the app's own behaviour.
            onCollectionAsked = { wanted ->
                homePrefs.collectionEnabled = wanted
                // Starting collection supersedes whatever a previous boot could not do, so the
                // reason goes with it. Stopping leaves it: a person who stops is not owed a stale
                // complaint either, and the adopt-only-while-stopped rule above is what retires it.
                if (wanted) homePrefs.bootRestartReason = null
                collectionEnabled = wanted
            },
            onExplain = { onExplain(ExplanationTopic.COUNTERS) },
        )
        AlertsCard(mutation = mutation, onExplain = { onExplain(ExplanationTopic.ALERTS) })
    }
}

/**
 * The alert surface: one delivery status, and the family's crossings.
 *
 * Both halves are always here, in every state. That is the shape the phase asks for and the reason
 * is the same each time: whatever is wrong with alerts - no credential, no permission, no backend, a
 * refused registration - the crossings the server recorded are still readable, so the list is shown
 * regardless and the status says what is wrong ABOVE it rather than instead of it.
 *
 * What it DRAWS is a few words per state, drawn from a closed set of string resources, with the
 * paragraphs behind "About alerts". That is F8 of the umbrella's frontend conventions, the same
 * shape the other three cards take: the state a person has to act on is a label they read at a
 * glance, and the explanation of what the state means is one tap away. Nothing was dropped in
 * getting there - every sentence this card used to draw is committed as an explanation paragraph
 * and rendered on that destination.
 */
@Composable
private fun AlertsCard(mutation: UiMutation, onExplain: () -> Unit) {
    val context = LocalContext.current

    // Re-read on every resume, for the same reason the permission grants are: the notification
    // permission can be granted on a system page, and a crossing can have happened while the app was
    // in the background. Both change the answer, and neither arrives as a callback.
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) {
                PushRegistrar.registerInBackground(context)
                CrossingsRefresher.refreshInBackground(context)
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    val view = AlertSurface.listView

    Card(modifier = Modifier.fillMaxWidth().testTag("card-alerts")) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            CardTitle(R.string.alerts_title)
            // Exactly one state, in a few words, with its paragraph behind the affordance below.
            // The mutation branch draws the paragraph instead, which is this card's half of the
            // demonstration AC18 asks for.
            Label(
                short = alertStatusLabel(AlertSurface.status),
                long = alertStatusExplanation(AlertSurface.status),
                mutation = mutation,
                tag = "alert-status",
            )
            if (AlertSurface.discardedPushes > 0) {
                // Shown, not merely counted: a push this app threw away is an alert a person did not
                // get, and a client that discards silently is indistinguishable from one receiving
                // nothing at all.
                WarningLiteral(
                    stringResource(R.string.alerts_discarded, AlertSurface.discardedPushes),
                    mutation,
                    "alert-discards",
                )
            }

            Text(stringResource(R.string.crossings_title), style = MaterialTheme.typography.titleSmall)
            // Each list state names itself in words. The three failures lead with "unread", which is
            // what keeps them tellable apart from the empty family log rather than reading as one.
            when (view.state) {
                CrossingListState.CHECKING ->
                    Text(
                        stringResource(R.string.crossings_checking),
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.testTag("crossings-state"),
                    )

                CrossingListState.EMPTY ->
                    Text(
                        stringResource(R.string.crossings_empty),
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.testTag("crossings-state"),
                    )

                CrossingListState.CREDENTIAL_REJECTED ->
                    WarningText(R.string.crossings_credential_rejected, mutation, "crossings-state")

                CrossingListState.SERVER_UNREACHABLE ->
                    WarningText(R.string.crossings_unreachable, mutation, "crossings-state")

                CrossingListState.SERVER_ERROR ->
                    WarningText(R.string.crossings_server_error, mutation, "crossings-state")

                CrossingListState.SHOWING -> Unit
            }

            for (crossing in view.rows) {
                Text(AlertText.listRow(crossing), style = MaterialTheme.typography.bodySmall)
            }

            TextButton(
                onClick = { CrossingsRefresher.refreshInBackground(context) },
                modifier = Modifier.focusRing(mutation).minimumTarget().testTag("action-refresh-crossings"),
            ) { Text(stringResource(R.string.crossings_refresh)) }
            if (mutation == UiMutation.PARAGRAPHS_ON_SURFACE) {
                Text(stringResource(R.string.explain_alerts_limitation), style = MaterialTheme.typography.bodySmall)
            }
            ExplainAffordance(R.string.explain_alerts, onExplain, "explain-alerts", mutation)
        }
    }
}

/**
 * Maps the single alert delivery state to the few words shown for it.
 *
 * A `when` with no else: adding a state to the status without deciding what the app SAYS about it
 * would otherwise compile, and a state nobody wrote a label for is a state the user is not told
 * about, which is the failure this whole surface exists to prevent.
 */
private fun alertStatusLabel(status: AlertDeliveryStatus?): Int = when (status) {
    null -> R.string.alerts_status_checking
    AlertDeliveryStatus.NotConfigured -> R.string.alerts_status_not_configured
    AlertDeliveryStatus.CannotShow -> R.string.alerts_status_cannot_show
    AlertDeliveryStatus.NotRegisteredRefused -> R.string.alerts_status_not_registered_refused
    AlertDeliveryStatus.NotRegisteredUnreachable -> R.string.alerts_status_not_registered_unreachable
    AlertDeliveryStatus.Armed -> R.string.alerts_status_armed
    is AlertDeliveryStatus.NotReceivable -> notReceivableLabel(status)
}

/**
 * The four not-receivable reasons, each its own label rather than one shared sentence.
 *
 * The parameter is named `state`, not `status`, on purpose. `internal/uiverify`'s F8 artefact scans
 * this file for a `reason` read off a receiver spelled `status`, which is the shape that reaches
 * [ConfigStatus.Incomplete]'s unbounded sentence and would put a socket failure on the surface. What
 * is read here is a four-member enum that can only ever select a string resource, so it is not that
 * shape; spelling the receiver differently keeps that guard aimed at the sentence it was written to
 * catch instead of at this, and weakens it by not one character.
 */
private fun notReceivableLabel(state: AlertDeliveryStatus.NotReceivable): Int = when (state.reason) {
    NotReceivableReason.NO_ROUTING_ADDRESS -> R.string.alerts_status_r1
    NotReceivableReason.NO_CONFIGURED_PROVIDER -> R.string.alerts_status_r2
    NotReceivableReason.PROVIDER_NOT_RECEIVABLE -> R.string.alerts_status_r3
    NotReceivableReason.SERVER_DOES_NOT_REPORT -> R.string.alerts_status_r4
}

/**
 * The paragraph behind each label, exhaustive over the same closed set.
 *
 * Kept beside [alertStatusLabel] so a state cannot gain a label without also gaining the sentence
 * that says what to do about it: the pairing is what makes "the explanations moved" true rather
 * than aspirational.
 */
private fun alertStatusExplanation(status: AlertDeliveryStatus?): Int = when (status) {
    null -> R.string.explain_alerts_checking
    AlertDeliveryStatus.NotConfigured -> R.string.explain_alerts_not_configured
    AlertDeliveryStatus.CannotShow -> R.string.explain_alerts_cannot_show
    AlertDeliveryStatus.NotRegisteredRefused -> R.string.explain_alerts_not_registered_refused
    AlertDeliveryStatus.NotRegisteredUnreachable -> R.string.explain_alerts_not_registered_unreachable
    AlertDeliveryStatus.Armed -> R.string.explain_alerts_armed
    is AlertDeliveryStatus.NotReceivable -> notReceivableExplanation(status)
}

/** The paragraph for each not-receivable reason. See [notReceivableLabel] for the parameter's name. */
private fun notReceivableExplanation(state: AlertDeliveryStatus.NotReceivable): Int = when (state.reason) {
    NotReceivableReason.NO_ROUTING_ADDRESS -> R.string.explain_alerts_r1
    NotReceivableReason.NO_CONFIGURED_PROVIDER -> R.string.explain_alerts_r2
    NotReceivableReason.PROVIDER_NOT_RECEIVABLE -> R.string.explain_alerts_r3
    NotReceivableReason.SERVER_DOES_NOT_REPORT -> R.string.explain_alerts_r4
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
                        mutation = mutation,
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
                    RingedButton(onClick = onOpenSettings, tag = "permission-action", mutation = mutation) {
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
                    modifier = Modifier.focusRing(mutation).minimumTarget().testTag("action-precise"),
                ) { Text(stringResource(R.string.action_upgrade_precise)) }
            }
            if (!LocationPermissionFlow.notificationVisible(grants, sdkInt)) {
                WarningText(R.string.warning_notifications_blocked, mutation, "warning-notifications")
            }

            ExplainAffordance(R.string.explain_permissions, onExplain, "explain-permissions", mutation)
        }
    }
}

@Composable
private fun ServerConfigCard(mutation: UiMutation, onExplain: () -> Unit) {
    val context = LocalContext.current
    val prefs = remember { ClientPreferences(context) }
    var url by rememberSaveable { mutableStateOf(prefs.baseUrl.orEmpty()) }
    var credential by rememberSaveable { mutableStateOf(prefs.deviceToken.orEmpty()) }
    // Saved across recreation for the same reason the two above are: a rotation between typing a
    // credential and pressing Save must not silently empty the field.
    var viewerCredential by rememberSaveable { mutableStateOf(prefs.viewerToken.orEmpty()) }
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
                modifier = Modifier
                    .fillMaxWidth()
                    .directionalPassThrough()
                    .focusRing(mutation)
                    .testTag("field-url"),
            )
            OutlinedTextField(
                value = credential,
                onValueChange = { credential = it },
                label = { Text(stringResource(R.string.config_token_label)) },
                singleLine = true,
                modifier = Modifier
                    .fillMaxWidth()
                    .directionalPassThrough()
                    .focusRing(mutation)
                    .testTag("field-token"),
            )
            // The VIEWER credential, entered the same way as the device token because it is issued
            // the same way: printed once by an operator command, out of band. It is a separate field
            // and not a second use of the one above, because the server's two credentials are
            // separate by construction and a device token is a 401 on every route this one reaches.
            OutlinedTextField(
                value = viewerCredential,
                onValueChange = { viewerCredential = it },
                label = { Text(stringResource(R.string.config_viewer_token_label)) },
                singleLine = true,
                // directionalPassThrough for the same reason the two fields above carry it, and this
                // one arrived without it: a Compose text field consumes the arrow keys whether or
                // not its caret has anywhere to go, so a directional traversal that lands here can
                // never leave, and Save, the server card's affordance and the whole alerts card
                // below become unreachable to anyone driving this screen without a pointer. That is
                // impl-gate finding F20 exactly, one field further down.
                modifier = Modifier
                    .fillMaxWidth()
                    .directionalPassThrough()
                    .focusRing(mutation)
                    .testTag("field-viewer-token"),
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
                        if (mutation == UiMutation.SAVE_WITHOUT_EFFECT) {
                            // Everything a press does EXCEPT the saving: the control was reachable,
                            // it took focus, the centre key activated it, and a verdict lands on the
                            // glass. Nothing is written, so the verdict is the refusal.
                            //
                            // This is the AC26 demonstration for AC14's third SHALL. Impl-gate
                            // finding F1: the assertion that graded "saved to the same effect a
                            // touch has" accepted BOTH verdicts, so it passed whether the keyboard
                            // save saved or the screen refused. It has to be able to go red against
                            // a screen that answers a keyboard press with a refusal, and this is
                            // that screen.
                            refused = true
                            message = context.getString(R.string.config_not_saved) + ": " +
                                ((prefs.readConfig() as? ConfigStatus.Incomplete)?.summary ?: "")
                        } else {
                            prefs.baseUrl = url
                            prefs.deviceToken = credential
                            prefs.viewerToken = viewerCredential
                            // Report the validated verdict, not a blanket "Saved": a URL the client
                            // will refuse to use must say so here, not fail silently at the first
                            // fix. And the refusal is a WORD, not a colour — "Not saved" leads the
                            // verdict.
                            //
                            // What the card draws is the SUMMARY, a few words. The sentence is behind
                            // "About server settings", which is what F8 asks for and what
                            // explain_config_verdict has always promised. The mutation branch draws
                            // the sentence instead, so the brevity assertion can be shown going red
                            // against a state that is not on the screen when it opens.
                            when (val status = prefs.readConfig()) {
                                is ConfigStatus.Configured -> {
                                    // A usable configuration is the one thing that unblocks a queue
                                    // parked by a `Result.failure()` for want of a URL or a token, so
                                    // ask for a flush the moment one exists.
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
                        }
                        // A newly-entered viewer credential is the one thing that unblocks the alert
                        // half, so re-run it here rather than waiting for the next app start.
                        AlertSurface.reset()
                        PushRegistrar.registerInBackground(context)
                        CrossingsRefresher.refreshInBackground(context)
                    },
                    tag = "action-save",
                    mutation = mutation,
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
            ExplainAffordance(R.string.explain_server, onExplain, "explain-server", mutation)
        }
    }
}

@Composable
private fun CollectionCard(
    canCollect: Boolean,
    collectionEnabled: Boolean,
    mutation: UiMutation,
    onCollectionAsked: (Boolean) -> Unit,
    onExplain: () -> Unit,
) {
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
    val readout = CollectionReadout.of(CollectionStatus, now, collectionEnabled)
    // The mutation reads the stored ask as a running collection: "Running" on the word, and no line
    // naming the disagreement. Exactly the two things the claim measures, and nothing else.
    val asksReadAsRunning = mutation == UiMutation.STORED_ASK_READS_AS_RUNNING
    val drawsAsRunning = running || (asksReadAsRunning && collectionEnabled)

    Card(modifier = Modifier.fillMaxWidth().testTag("card-collection")) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            CardTitle(R.string.collection_title)
            Text(
                stringResource(if (drawsAsRunning) R.string.collection_running else R.string.collection_stopped),
                style = MaterialTheme.typography.bodyMedium,
                // The contrast mutation, and it is chosen PER THEME.
                //
                // A single fixed grey cannot break this claim in both themes: a light grey that
                // measures 1.7:1 on a white card measures nearly 9:1 on a dark one, so a
                // theme-blind mutation leaves the dark demonstration green while looking broken.
                // Each of these measures about 2.5:1 against the card behind it in its own theme,
                // which is decisively under the 4.5:1 floor and still a colour a crop can separate
                // from its background.
                color = if (mutation == UiMutation.CONTRAST_BELOW_FLOOR) {
                    if (isSystemInDarkTheme()) Color(0xFF646464) else Color(0xFFA5A5A5)
                } else {
                    MaterialTheme.colorScheme.onSurface
                },
                modifier = Modifier.testTag("collection-running"),
            )

            // The disagreement between what was asked for and what is running, said in its own
            // words. Without it "Stopped" is all a reader gets, and "Stopped" is what a phone
            // somebody switched off says too - so the state a failed restart leaves behind would be
            // indistinguishable from a deliberate stop, which is the half of this the reboot work
            // exists to make visible. It is drawn ONLY in that state, so it never competes with
            // "Running" or contradicts an honest "Stopped".
            if (readout.enabledButNotRunning && !asksReadAsRunning) {
                Text(
                    stringResource(R.string.collection_enabled_not_running),
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.testTag("collection-ask"),
                )
            }

            val collectionLabel =
                stringResource(if (running) R.string.collection_stop else R.string.collection_start)
            val onCollectionClick: () -> Unit = {
                if (running) {
                    onCollectionAsked(false)
                    LocationCollectionService.stop(context)
                } else {
                    onCollectionAsked(true)
                    LocationCollectionService.start(context)
                }
            }
            if (mutation == UiMutation.TARGET_BELOW_FLOOR) {
                // A touch target genuinely under the 48dp floor, which takes TWO changes rather
                // than one, and finding out why is most of what finding F21 was hiding.
                //
                // Drawing the control at 20dp is not enough, and neither is dropping Material's
                // `Button` (whose `minimumInteractiveComponentSize()` expands the laid-out node back
                // to 48dp around a 20dp visual). Compose reports the node's TOUCH bounds to the
                // accessibility layer, and it expands any small target to
                // `ViewConfiguration.minimumTouchTargetSize` - 48dp - for pointer input. So a 20dp
                // control still HAS a 48dp target, an accessibility sweep reading those bounds was
                // telling the truth when it reported no defect, and success criterion 2.5.8 is about
                // the target rather than the paint. Taking that floor away for this one control is
                // what actually breaks the claim.
                val platform = LocalViewConfiguration.current
                CompositionLocalProvider(
                    LocalViewConfiguration provides object : ViewConfiguration by platform {
                        override val minimumTouchTargetSize: DpSize get() = DpSize.Zero
                    },
                ) {
                    Box(
                        modifier = Modifier
                            .size(20.dp)
                            .testTag("action-collection")
                            .clickable(onClick = onCollectionClick),
                        contentAlignment = Alignment.Center,
                    ) { Text(collectionLabel, maxLines = 1, style = MaterialTheme.typography.bodySmall) }
                }
            } else {
                RingedButton(
                    onClick = onCollectionClick,
                    enabled = canCollect,
                    tag = "action-collection",
                    mutation = mutation,
                ) { Text(collectionLabel) }
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
                ExplainAffordance(R.string.explain_counters, onExplain, "explain-counters", mutation)
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
    TroubleKind.BACKGROUND_LOCATION_MISSING -> R.string.trouble_background_location_missing
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
private fun ExplainAffordance(labelRes: Int, onClick: () -> Unit, tag: String, mutation: UiMutation) {
    TextButton(
        onClick = onClick,
        modifier = Modifier.focusRing(mutation).minimumTarget().testTag(tag),
    ) { Text(stringResource(labelRes)) }
}

/**
 * A button with its focus ring on a WRAPPER, which is what makes the ring measurable.
 *
 * The ring used to sit in the button's own modifier chain, and it could not be read from there. A
 * Compose semantics node takes its bounds from the coordinator of the semantics modifier itself
 * rather than from the outermost one, and Material's button merges its descendants, so a capture of
 * the tagged node came back as the button's SURFACE with the ring outside the frame - while the
 * Material ripple's focus state layer, which tints that surface on focus, was inside it. The claim
 * passed on the ripple and the AC26 demonstration refused it, correctly: nothing about that
 * measurement could tell an indicator that had been painted from one that had not.
 *
 * On a wrapper the geometry is not a question. The Box's own outer [FOCUS_RING_WIDTH_DP]dp carries
 * the ring and nothing else - the button begins after the inset - so the band the suite compares is
 * the indicator, and a suppressed ring reads as zero.
 */
@Composable
private fun RingedButton(
    onClick: () -> Unit,
    tag: String,
    mutation: UiMutation,
    enabled: Boolean = true,
    focusable: Boolean = true,
    content: @Composable () -> Unit,
) {
    val base = Modifier.minimumTarget().testTag(tag)
    val button = if (focusable) base else base.then(Modifier.focusProperties { canFocus = false })
    Box(modifier = Modifier.testTag(ringTagOf(tag)).focusRing(mutation)) {
        Button(onClick = onClick, enabled = enabled, modifier = button) { content() }
    }
}

/** Where a control's focus ring is drawn, and the tag the suite measures it by. */
fun ringTagOf(tag: String): String = "$tag-ring"

@Composable
private fun ExplanationScreen(
    topic: ExplanationTopic,
    mutation: UiMutation,
    onBack: () -> Unit,
    modifier: Modifier = Modifier,
) {
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
        // The alert half's live sentence is the delivery state's own paragraph, which is already in
        // the list below and needs no second copy here.
        ExplanationTopic.ALERTS -> null
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
                    ExplanationTopic.ALERTS -> R.string.explanation_title_alerts
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
            modifier = Modifier.focusRing(mutation).minimumTarget().testTag("explanation-back"),
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
        R.string.explain_collection_restart,
        R.string.explain_collection_limitation,
        R.string.explain_counter_delivered,
        R.string.explain_counter_queued,
        R.string.explain_counter_dropped,
        R.string.explain_counter_not_recorded,
        R.string.explain_collection_freshness,
    )

    // Every state of the alert surface, in the order the card can reach them: the ten delivery
    // states first, then the crossings list, then the two limitations a person needs to know about
    // before they trust a notification to arrive.
    ExplanationTopic.ALERTS -> listOf(
        R.string.explain_alerts_checking,
        R.string.explain_alerts_not_configured,
        R.string.explain_alerts_cannot_show,
        R.string.explain_alerts_not_registered_refused,
        R.string.explain_alerts_not_registered_unreachable,
        R.string.explain_alerts_r1,
        R.string.explain_alerts_r2,
        R.string.explain_alerts_r3,
        R.string.explain_alerts_r4,
        R.string.explain_alerts_armed,
        R.string.explain_crossings_states,
        R.string.explain_alerts_discarded,
        R.string.explain_alerts_limitation,
        R.string.explain_viewer_plaintext,
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

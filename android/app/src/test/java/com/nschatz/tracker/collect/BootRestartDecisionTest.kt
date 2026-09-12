package com.nschatz.tracker.collect

import com.nschatz.tracker.permission.CollectionCapability
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The boot-restart decision, over its whole input space.
 *
 * Pure JVM, `junit` only - no Robolectric, no mocking framework, no Android framework class. That is
 * not a limitation worked around here, it is the reason the decision was moved out of
 * `BootCompletedReceiver` in the first place: a receiver cannot be constructed, handed an intent or
 * observed by a headless build, so the judgement lives where it can be asserted and the receiver
 * keeps only the three reads it cannot avoid.
 *
 * What this file does NOT claim to prove: that a real phone delivers `BOOT_COMPLETED`, or that the
 * platform then permits the service. Those are the emulator route's (`make verify-boot-restart`) and,
 * for a particular OEM skin, nobody's - see `android/README.md`.
 */
class BootRestartDecisionTest {

    // --- the decision, over every combination of its three inputs --------------------------------

    @Test
    fun aBootWithCollectionEnabledAndEverythingInPlaceStartsCollection() {
        assertSame(
            BootRestartAction.Start,
            BootRestartDecision.decide(
                collectionEnabled = true,
                capability = CollectionCapability.CONTINUOUS,
                config = usableConfig(),
            ),
        )
    }

    @Test
    fun aBootWithCollectionNotEnabledDoesNotStartCollectionAndRecordsNothing() {
        // Every grant state and every configuration, because "nobody asked" is the one answer that
        // must not depend on anything else. A phone whose owner turned collection off is behaving
        // exactly as asked, and a recorded reason there would put a complaint in front of somebody
        // who made a choice.
        for (capability in CollectionCapability.entries) {
            for (config in listOf(usableConfig(), unusableConfig())) {
                val action = BootRestartDecision.decide(
                    collectionEnabled = false,
                    capability = capability,
                    config = config,
                )
                assertEquals(
                    "collection was not enabled, so the boot must not start it ($capability)",
                    BootRestartAction.DoNotStart(reason = null),
                    action,
                )
            }
        }
    }

    @Test
    fun withoutBackgroundLocationNothingIsStartedAndTheMissingGrantIsNamed() {
        // FOREGROUND_ONLY refuses exactly as NONE does. Android will not create a `location`
        // foreground service from the background without ACCESS_BACKGROUND_LOCATION, and a boot
        // receiver is the background, so a foreground-only grant is as unable to restart collection
        // as no grant at all.
        for (capability in listOf(CollectionCapability.NONE, CollectionCapability.FOREGROUND_ONLY)) {
            val action = BootRestartDecision.decide(
                collectionEnabled = true,
                capability = capability,
                config = usableConfig(),
            )
            assertEquals(
                "with $capability the boot path must not attempt the service",
                BootRestartAction.DoNotStart(BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED),
                action,
            )
            val sentence = BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED.sentence
            assertTrue(
                "the recorded reason does not name the missing grant: \"$sentence\"",
                sentence.contains("Allow all the time") && sentence.contains("location"),
            )
        }
    }

    @Test
    fun withNoServerUrlOrNoDeviceTokenNothingIsStartedAndTheReasonIsADifferentOne() {
        // The two halves of an unusable configuration, each through the real validator rather than a
        // hand-built status, so this measures what the app will actually read off disk.
        val noUrl = ConfigValidation.validate(baseUrl = null, deviceToken = "a-device-token")
        val noToken = ConfigValidation.validate(baseUrl = "https://tracker.example", deviceToken = null)
        for ((what, config) in listOf("no server URL" to noUrl, "no device token" to noToken)) {
            val action = BootRestartDecision.decide(
                collectionEnabled = true,
                capability = CollectionCapability.CONTINUOUS,
                config = config,
            )
            assertEquals(
                "with $what the boot path must not attempt the service",
                BootRestartAction.DoNotStart(BootRestartReason.SERVER_CONFIG_UNUSABLE),
                action,
            )
        }

        // Distinguishable from the missing-grant reason in BOTH of the two things a person sees: the
        // few words on the card and the sentence behind it. One reason rendered as another is a
        // person sent to fix the wrong thing.
        val grant = BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED
        val config = BootRestartReason.SERVER_CONFIG_UNUSABLE
        assertTrue("the two reasons share a card label", grant.kind != config.kind)
        assertTrue("the two reasons share a sentence", grant.sentence != config.sentence)
    }

    @Test
    fun theWholeInputSpaceIsAnsweredAndEveryAnswerIsTheCommittedOne() {
        // The cross product, enumerated from the enum itself rather than written out, so a new
        // CollectionCapability member cannot slip through unmeasured.
        val expected = mutableMapOf<String, BootRestartAction>()
        for (enabled in listOf(true, false)) {
            for (capability in CollectionCapability.entries) {
                for ((configName, config) in listOf("usable" to usableConfig(), "unusable" to unusableConfig())) {
                    val want = when {
                        !enabled -> BootRestartAction.DoNotStart(reason = null)
                        capability != CollectionCapability.CONTINUOUS ->
                            BootRestartAction.DoNotStart(BootRestartReason.BACKGROUND_LOCATION_NOT_GRANTED)

                        configName == "unusable" ->
                            BootRestartAction.DoNotStart(BootRestartReason.SERVER_CONFIG_UNUSABLE)

                        else -> BootRestartAction.Start
                    }
                    expected["$enabled/$capability/$configName"] = want
                    assertEquals(
                        "enabled=$enabled capability=$capability config=$configName",
                        want,
                        BootRestartDecision.decide(enabled, capability, config),
                    )
                }
            }
        }
        assertEquals(
            "the cross product of the three inputs is 2 x ${CollectionCapability.entries.size} x 2; " +
                "a smaller sweep than that is not the input space",
            2 * CollectionCapability.entries.size * 2,
            expected.size,
        )
        // Exactly one input combination starts collection. A second would mean some state short of
        // "asked for, permitted, and configured" turns the GPS on.
        assertEquals(
            "more than one input combination starts collection: " +
                expected.filterValues { it == BootRestartAction.Start }.keys,
            1,
            expected.values.count { it == BootRestartAction.Start },
        )
    }

    // --- the input space is the WHOLE input space ------------------------------------------------

    @Test
    fun theDecisionReadsThoseThreeInputsAndNothingElse() {
        // Read off the compiled function rather than asserted in prose. A fourth input - a build
        // flag, an intent extra, a system property, a clock - is what a grading route could use to
        // enable collection on a shipped phone, so "there are three" is the property worth checking
        // mechanically. Java reflection over the class under test is not a mock and needs no
        // dependency: the JVM has it.
        val decide = BootRestartDecision::class.java.declaredMethods.filter { it.name == "decide" }
        assertEquals("BootRestartDecision declares ${decide.size} decide methods, not one", 1, decide.size)
        assertEquals(
            "decide takes ${decide[0].parameterTypes.size} parameters; the decision's inputs are the " +
                "persisted ask, the grants and the stored configuration, and nothing else",
            3,
            decide[0].parameterTypes.size,
        )
        assertEquals(
            listOf("boolean", CollectionCapability::class.java.name, ConfigStatus::class.java.name),
            decide[0].parameterTypes.map { it.name },
        )

        // ... and it holds no state of its own, so there is nowhere for a fourth input to be stashed
        // between calls. Two names are not state and are excluded by name: INSTANCE is the Kotlin
        // `object` singleton itself, and a `$`-prefixed field is the compiler's - the Compose plugin
        // puts a `$stable` int constant on every class in a module it processes, which no code here
        // declares and none can read.
        val fields = BootRestartDecision::class.java.declaredFields
            .map { it.name }
            .filter { it != "INSTANCE" && !it.startsWith("$") }
        assertTrue("BootRestartDecision holds state: $fields", fields.isEmpty())

        // Determinism, measured rather than assumed: the same three inputs answer the same way with a
        // different call interleaved between, which no hidden per-call input would survive.
        val first = BootRestartDecision.decide(true, CollectionCapability.CONTINUOUS, usableConfig())
        BootRestartDecision.decide(false, CollectionCapability.NONE, unusableConfig())
        assertEquals(
            first,
            BootRestartDecision.decide(true, CollectionCapability.CONTINUOUS, usableConfig()),
        )
    }

    // --- the privacy bar on the record ----------------------------------------------------------

    @Test
    fun noRecordedReasonCarriesACoordinateOrACredential() {
        // Hostile inputs: a configuration whose own sentence carries a device token, a viewer token
        // and a coordinate. This is the shape the bar exists to refuse - a design that passed the
        // failure's own sentence through to the record would put every one of these on the screen
        // and into the preference file, and ConfigStatus.Incomplete.reason is a string the caller
        // supplies.
        val secrets = listOf(
            "tok-device-4f9a2b7c1e",
            "tok-viewer-88d1c0aa53",
            "47.6205",
            "-122.3493",
        )
        val hostile = ConfigStatus.Incomplete(
            summary = "No device token set",
            reason = "device ${secrets[0]} viewer ${secrets[1]} at ${secrets[2]},${secrets[3]}",
        )

        val action = BootRestartDecision.decide(
            collectionEnabled = true,
            capability = CollectionCapability.CONTINUOUS,
            config = hostile,
        )
        val reason = (action as BootRestartAction.DoNotStart).reason
        assertEquals(BootRestartReason.SERVER_CONFIG_UNUSABLE, reason)

        // Everything the record can ever hold: the stored value (the enum name) and the two texts a
        // person reads. The vocabulary is closed, so this sweep covers every reason there is.
        for (candidate in BootRestartReason.entries) {
            val recorded = listOf(candidate.name, candidate.sentence, candidate.kind.name)
            for (text in recorded) {
                for (secret in secrets) {
                    assertTrue(
                        "a recorded reason carries $secret: \"$text\"",
                        !text.contains(secret),
                    )
                }
                assertTrue(
                    "a recorded reason carries a decimal number, which is the shape of a coordinate: \"$text\"",
                    !Regex("""-?\d+\.\d+""").containsMatchIn(text),
                )
            }
        }
    }

    @Test
    fun whatIsStoredIsTheClosedVocabularyAndNothingElse() {
        // The persisted form is the enum NAME, and an unrecognised value on disk reads as null - so a
        // preference file written by an older build, or by hand, cannot make the screen render
        // something the closed set does not name.
        for (reason in BootRestartReason.entries) {
            assertSame(reason, BootRestartReason.named(reason.name))
        }
        assertNull(BootRestartReason.named(null))
        assertNull(BootRestartReason.named(""))
        assertNull(BootRestartReason.named("47.6205,-122.3493"))
        assertNull(BootRestartReason.named("BACKGROUND_LOCATION_NOT_GRANTED "))

        // Every reason has a card label and a sentence, so none can reach the surface with nothing to
        // say or with a sentence that is really a label.
        for (reason in BootRestartReason.entries) {
            assertTrue("${reason.name} has an empty sentence", reason.sentence.isNotBlank())
            assertTrue(
                "${reason.name}'s sentence is ${reason.sentence.split(' ').size} words; the card draws " +
                    "the label and this is the paragraph behind it",
                reason.sentence.split(' ').size > 8,
            )
        }
    }

    private fun usableConfig(): ConfigStatus =
        ConfigValidation.validate(baseUrl = "https://tracker.example", deviceToken = "a-device-token")

    private fun unusableConfig(): ConfigStatus = ConfigValidation.validate(baseUrl = null, deviceToken = null)
}

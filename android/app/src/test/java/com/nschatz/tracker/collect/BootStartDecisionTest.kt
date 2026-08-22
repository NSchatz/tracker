package com.nschatz.tracker.collect

import com.nschatz.tracker.permission.CollectionCapability
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * What a boot does, in every combination of stored intent and granted permission.
 *
 * This is the one part of the reboot behaviour a headless build can genuinely decide, and it is
 * decided exhaustively here: three intent readings times three capabilities is nine cases, and all
 * nine are below. What the gate still cannot prove is that the broadcast arrives at all, which is
 * device and OEM behaviour and is an operator check (`android/README.md`).
 */
class BootStartDecisionTest {

    private val on = IntentReading.Known(CollectionIntent.ON)
    private val off = IntentReading.Known(CollectionIntent.OFF)

    @Test
    fun startsWhenTheOperatorAskedForCollectionAndBackgroundLocationIsGranted() {
        assertEquals(
            BootAction.Start,
            BootStartDecision.decide(on, CollectionCapability.CONTINUOUS),
        )
    }

    /**
     * The refusal that keeps the boot receiver honest. Android will not create a `location`
     * foreground service from the background without `ACCESS_BACKGROUND_LOCATION`, and a receiver
     * is the background - so a build that tried anyway would throw, and one that tried and
     * swallowed the throw would be silent about a phone that has stopped reporting.
     */
    @Test
    fun refusesWithoutBackgroundLocationAndSaysWhy() {
        for (capability in listOf(CollectionCapability.FOREGROUND_ONLY, CollectionCapability.NONE)) {
            assertEquals(
                "capability $capability cannot start a location service from a boot receiver",
                BootAction.DoNotStart(ReasonCase.BACKGROUND_LOCATION_NOT_GRANTED),
                BootStartDecision.decide(on, capability),
            )
        }
    }

    /**
     * Intent OFF is not a fault. Nothing starts, nothing is sent, and - this is the part worth
     * asserting - NO reason is recorded, whatever the permission state. A device whose operator
     * turned collection off is obeying, and putting a complaint in front of them is how a product
     * teaches people to ignore its warnings.
     */
    @Test
    fun intentOffStartsNothingAndRecordsNothingInEveryPermissionState() {
        for (capability in CollectionCapability.values()) {
            assertEquals(
                "intent OFF with capability $capability",
                BootAction.DoNotStart(reason = null),
                BootStartDecision.decide(off, capability),
            )
        }
    }

    /**
     * An intent that is absent or unreadable is never guessed, and the two are told apart because
     * they are different things to tell an operator: one means nothing was ever chosen, the other
     * means something was and it cannot be read back.
     */
    @Test
    fun anIntentThatCannotBeReadStartsNothingAndNamesWhichCaseItWas() {
        for (capability in CollectionCapability.values()) {
            assertEquals(
                BootAction.DoNotStart(ReasonCase.COLLECTION_INTENT_MISSING),
                BootStartDecision.decide(IntentReading.Missing, capability),
            )
            assertEquals(
                BootAction.DoNotStart(ReasonCase.COLLECTION_INTENT_UNREADABLE),
                BootStartDecision.decide(IntentReading.Unreadable, capability),
            )
        }
    }

    /**
     * Both a missing permission and an unreadable intent stop the start, and only one reason is
     * held. The intent wins: if the app cannot tell whether the operator wanted collection at all,
     * reporting a permission problem answers a question nobody asked.
     */
    @Test
    fun theIntentCaseIsReportedAheadOfThePermissionCase() {
        assertEquals(
            BootAction.DoNotStart(ReasonCase.COLLECTION_INTENT_UNREADABLE),
            BootStartDecision.decide(IntentReading.Unreadable, CollectionCapability.NONE),
        )
    }
}

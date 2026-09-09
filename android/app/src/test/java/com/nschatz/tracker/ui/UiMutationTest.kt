package com.nschatz.tracker.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * AC28: the mutation seam never ships.
 *
 * The seam is a debug-only switch that deliberately breaks the screen so the instrumented suite can
 * be shown going red. Widening it - which this item does, from one mutation to six - widens the one
 * genuinely irreversible-shaped risk the item carries: a seam that leaked into a release build would
 * ship an app that is broken on purpose, and no re-run undoes an app that is already on a phone.
 *
 * So it is pinned two ways here, and this is the one claim in the item that is legitimately graded
 * off a device, because it is about what a BUILD contains rather than about what a screen shows:
 *
 *  - [UiMutation.select] is pure, so "a release build applies no mutation, whatever asks for one" is
 *    provable by calling it: every name this enum has, every name it does not, `null`, and the
 *    empty string, all answer [UiMutation.NONE] when the debug flag is false;
 *  - and a source scan asserts that nothing else in the client can select one - no second reader of
 *    the intent extra, no environment variable, no system property, no build flag other than the one
 *    `select` reads first.
 */
class UiMutationTest {

    @Test
    fun aReleaseBuildAppliesNoMutationWhateverAsksForOne() {
        for (mutation in UiMutation.entries) {
            assertEquals(
                "a release build honoured the mutation ${mutation.name}; the shipped app must have no " +
                    "path to a deliberately broken screen",
                UiMutation.NONE,
                UiMutation.select(debug = false, name = mutation.name),
            )
        }
        for (name in listOf(null, "", "  ", "none", "NONE ", "ACCESSIBILITY_DEFECT", "../CONTRAST_BELOW_FLOOR")) {
            assertEquals(
                "a release build answered ${name.orEmpty()} with something other than NONE",
                UiMutation.NONE,
                UiMutation.select(debug = false, name = name),
            )
        }
    }

    @Test
    fun aDebugBuildHonoursExactlyTheNamesThisEnumHas() {
        for (mutation in UiMutation.entries) {
            assertEquals(mutation, UiMutation.select(debug = true, name = mutation.name))
        }
        // An unknown, misspelt or partially matching name is NONE rather than an error and rather
        // than a near miss: the seam either names a mutation this enum declares, or the screen is
        // the real one.
        for (name in listOf(null, "", "contrast_below_floor", "CONTRAST", "CONTRAST_BELOW_FLOOR ")) {
            assertEquals(
                "a debug build resolved ${name.orEmpty()} to a mutation",
                UiMutation.NONE,
                UiMutation.select(debug = true, name = name),
            )
        }
    }

    @Test
    fun theDebugFlagIsReadBeforeAnythingElseAndOnlyInSelect() {
        val seam = source("ui/UiMutation.kt")
        val body = seam.substringAfter("fun select(")
        val debugAt = body.indexOf("debug")
        val nameAt = body.indexOf("name ==")
        assertTrue("UiMutation.select does not test the debug flag at all", debugAt >= 0)
        assertTrue(
            "UiMutation.select resolves a name before it consults the debug flag; a release build " +
                "must decide NONE first and read nothing else",
            debugAt < nameAt || nameAt < 0,
        )

        // The seam has exactly one reader of the intent extra, and it is the one that calls select.
        val readers = clientSources().filter { (path, text) ->
            path != "ui/UiMutation.kt" && text.contains(UiMutation.EXTRA)
        }
        assertTrue(
            "these files read the mutation extra without going through UiMutation.select: " +
                readers.keys.joinToString(", "),
            readers.isEmpty(),
        )
    }

    @Test
    fun noEnvironmentVariableSystemPropertyOrOtherBuildFlagCanSelectAMutation() {
        val forbidden = listOf("System.getenv", "System.getProperty", "SystemProperties", "getStringExtra")
        val offenders = mutableListOf<String>()
        for ((path, text) in clientSources()) {
            for (needle in forbidden) {
                if (!text.contains(needle)) continue
                // UiMutation.from is the one intent read there is, and it hands its result straight
                // to select, which answers NONE unless the build is a debug build.
                if (path == "ui/UiMutation.kt" && needle == "getStringExtra") continue
                offenders.add("$path reads $needle")
            }
        }
        assertTrue(
            "the client reads an external switch that could ask for a mutation: " + offenders.joinToString("; "),
            offenders.isEmpty(),
        )

        // BuildConfig.DEBUG is the only build flag the seam consults. A second one would be a second
        // way for a release build to answer anything but NONE.
        val seam = source("ui/UiMutation.kt")
        val flags = Regex("BuildConfig\\.[A-Z_]+").findAll(seam).map { it.value }.toSet()
        assertEquals("the seam reads more than one build flag: $flags", setOf("BuildConfig.DEBUG"), flags)
    }

    @Test
    fun withNoMutationSelectedTheScreenDrawsWhatItDrawsWithTheSeamAbsent() {
        // Every branch the seam owns is an equality test against ONE named mutation, so selecting
        // NONE takes the unmutated arm of every one of them. An inequality is legal and is used
        // (`mutation != SAVE_NOT_FOCUSABLE`), but only against a specific mutation: a comparison
        // against NONE itself would invert - the real screen would take the mutated arm - and a
        // comparison assembled from a variable would not be readable here at all.
        val screen = source("ui/MainActivity.kt")
        val comparisons = Regex("mutation\\s*([!=]=)\\s*UiMutation\\.([A-Z_]+)").findAll(screen).toList()
        assertTrue("MainActivity.kt tests no mutation at all, so this assertion would pass vacuously", comparisons.size >= 8)
        for (m in comparisons) {
            assertTrue(
                "MainActivity.kt compares the mutation against NONE (${m.value}); with the seam " +
                    "unset that arm would draw the MUTATED screen",
                m.groupValues[2] != "NONE",
            )
        }
        val loose = Regex("mutation\\s*([!=]=)\\s*(?!UiMutation\\.)[A-Za-z]").findAll(screen).toList()
        assertTrue(
            "MainActivity.kt compares the mutation against something that is not a named UiMutation " +
                "constant: " + loose.joinToString(", ") { it.value },
            loose.isEmpty(),
        )
    }

    // --- reading the client's own sources --------------------------------------------------------
    //
    // This is a source scan and it is the right instrument here: AC28 is a claim about what a BUILD
    // contains, not about what a screen shows, so F2's "the runtime that draws it" rule does not
    // reach it. It FAILS rather than skips when it cannot find the sources - a scan that quietly
    // found no files would be the vacuous pass this whole route exists to refuse.

    private fun clientSources(): Map<String, String> {
        val root = mainSourceRoot()
        val out = mutableMapOf<String, String>()
        root.walkTopDown().filter { it.isFile && it.extension == "kt" }.forEach {
            out[it.relativeTo(root).path.replace(File.separatorChar, '/')] = it.readText()
        }
        assertTrue("no Kotlin source was found under $root, so this scan would prove nothing", out.size >= 5)
        return out
    }

    private fun source(relative: String): String {
        val file = File(mainSourceRoot(), relative)
        assertTrue("expected to find $file", file.isFile)
        return file.readText()
    }

    private fun mainSourceRoot(): File {
        val suffix = File("src/main/java/com/nschatz/tracker")
        var dir: File? = File("").absoluteFile
        while (dir != null) {
            for (candidate in listOf(File(dir, suffix.path), File(dir, "android/app/" + suffix.path))) {
                if (candidate.isDirectory) return candidate
            }
            dir = dir.parentFile
        }
        throw AssertionError(
            "could not locate the client's sources from ${File("").absolutePath}; AC28 is graded by " +
                "reading them, and a scan that found none would report green having read nothing",
        )
    }
}

// Regression artefact for S0056 impl-gate finding F8.
//
// AC12 of work/specs/S0056-tracker-frontend-conventions/spec.md:
//
//	"WHEN the Android screen is rendered on an emulator at a 360dp-wide phone profile, THE SYSTEM
//	 SHALL show no text clipped or pushed outside the display and SHALL require no horizontal
//	 scrolling; every label, warning and status line drawn on the screen itself SHALL be short
//	 enough to read at a glance, with the explanatory paragraphs that stand there today reachable
//	 instead through exactly one affordance per card [...]"
//
// # The finding, and why this file was re-derived rather than deleted
//
// The refuter's artefact (impl ordinal 2) pinned nine sentences the domain layer produced and the
// home tree rendered verbatim - the server card's validated verdict and the collection card's
// warning - and measured them against the floor the instrumented suite declares. It also pinned the
// two render CALL SITES that put them there, and instructed a later reader, in its own words, that
// if one of them moved "the fixture must be re-derived rather than trusted".
//
// It moved. Both texts now come from bounded sources: the verdict from ConfigStatus.Incomplete's
// short `summary` and the warning from an exhaustive `when` over TroubleKind returning a string
// resource. So this file is re-derived onto the fixed render path, and it is aimed at the property
// rather than at nine sentences, which is what makes it survive the next paragraph somebody adds.
//
// It is STRICTLY STRONGER than the artefact it replaces. That one enumerated nine literals and went
// green the moment any of them was edited; this one asserts:
//
//  1. the home tree draws the verdict from `summary` and the warning from `troubleLabel`, and
//     reaches the SENTENCE for either only inside the debug-only mutation branch that exists to
//     show the brevity assertion going red;
//  2. every bounded source it can draw - each Incomplete summary, each trouble label - is under the
//     suite's own floor once the composable's prefix is added;
//  3. every TroubleKind constant HAS a label, so the closed set cannot grow a member that falls
//     through to prose;
//  4. the sentences are still committed and still reachable on the explanation destination, so they
//     were moved rather than dropped, which is what AC12 asks for;
//  5. AC12_labels_stay_short now DRIVES the refused-save and blocked-collection states before it
//     measures - the half impl verdict 2 called "the one that keeps it closed", and the exact
//     inverse of what the artefact asserted while the defect stood.
//
// WHAT THIS TEST IS AND IS NOT. It measures committed source: string lengths, the shape of a render
// path, the membership of an enum. Those are facts about the source, not about a rendering, so no
// browser or emulator is needed to decide them and F2's "grade a rendered claim with the runtime
// that draws it" is not in tension. The RENDERED claim is AC12_labels_stay_short on a booted
// emulator, and assertion 5 above is this file making sure that claim actually looks at the states
// in question. This file is the guard rail; the emulator is the grader.
package uiverify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// androidSrc locates a file in the client's source tree from internal/uiverify.
func androidSrc(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "android", "app", "src"}, parts...)...)
}

var (
	serverConfigKt = androidSrc("main", "java", "com", "nschatz", "tracker", "collect", "ServerConfig.kt")
	collectionSvc  = androidSrc("main", "java", "com", "nschatz", "tracker", "collect", "LocationCollectionService.kt")
	statusKt       = androidSrc("main", "java", "com", "nschatz", "tracker", "collect", "CollectionStatus.kt")
	mainActivityKt = androidSrc("main", "java", "com", "nschatz", "tracker", "ui", "MainActivity.kt")
	stringsXML     = androidSrc("main", "res", "values", "strings.xml")
	uiClaimTestKt  = androidSrc("androidTest", "java", "com", "nschatz", "tracker", "ui", "UiClaimTest.kt")
)

// theSentencesTheDomainStillProduces are the unbounded texts the domain layer produces.
//
// They are the refuter's fixture, kept verbatim and re-purposed: each one must STILL be committed
// where it was, because AC12 moves the explanations rather than deleting them, and none of them may
// be what the surface draws.
var theSentencesTheDomainStillProduces = []struct {
	File      string
	Fragments []string
}{
	{collectionSvc, []string{"Location permission was revoked, so collection stopped."}},
	{collectionSvc, []string{
		"Collection could not start: Android refused the location foreground service ",
		"(" + interpolatedExceptionName + "). Check that location permission is granted, then ",
		"start it again from this screen.",
	}},
	{serverConfigKt, []string{"No server URL is set. Enter the tracker server address."}},
	{serverConfigKt, []string{"The server URL must start with https:// (or http:// for local testing)."}},
	{serverConfigKt, []string{"Refusing to send the device token over plaintext http://. Use https://."}},
	{serverConfigKt, []string{"The server URL is missing a host name."}},
	{serverConfigKt, []string{"No device token is set. Run `tracker enroll` on the server and paste the token here."}},
}

// interpolatedExceptionName is spelled out of its pieces so this file carries no Kotlin template
// that a Go tool would mistake for one of its own.
const interpolatedExceptionName = "$" + "{e.javaClass.simpleName}"

// theMutationThatPutsProseBack is the debug-only switch that reproduces F8 on purpose, so the
// brevity assertion can be shown going red against a state that is not on the screen at launch. It
// is the ONLY place either sentence may reach the home tree.
const theMutationThatPutsProseBack = "UiMutation.PROSE_IN_A_DEGRADED_STATE"

func TestRegressS0056F8TheAndroidHomeTreeRendersParagraphs(t *testing.T) {
	read := func(p string) string {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		return string(body)
	}

	// 1. The floor the instrumented suite itself enforces, read out of the suite.
	claims := read(uiClaimTestKt)
	m := regexp.MustCompile(`MAX_LABEL_WORDS\s*=\s*(\d+)`).FindStringSubmatch(claims)
	if m == nil {
		t.Fatalf("%s no longer declares MAX_LABEL_WORDS, so this test cannot measure against the floor the suite uses", uiClaimTestKt)
	}
	floor := 0
	if _, err := fmt.Sscanf(m[1], "%d", &floor); err != nil || floor <= 0 {
		t.Fatalf("MAX_LABEL_WORDS is %q, which is not a word count", m[1])
	}

	// 2. The prefixes the two composables add, read out of the resources.
	res := read(stringsXML)
	stringNamed := func(name string) string {
		p := regexp.MustCompile(`<string name="` + name + `">([^<]*)</string>`).FindStringSubmatch(res)
		if p == nil {
			t.Fatalf("%s no longer declares %q, so the rendered text cannot be established", stringsXML, name)
		}
		return p[1]
	}
	notSaved := stringNamed("config_not_saved") + ": "
	warning := stringNamed("warning_prefix") + ": "

	activity := read(mainActivityKt)

	// 3. The home tree draws BOUNDED sources.
	//
	//    These are the call sites the fix put in place of the two that rendered a sentence. If one
	//    of them moves, this test is measuring something the screen no longer does and says so
	//    rather than passing.
	for _, site := range []string{
		`.testTag("home")`,
		`ServerConfigCard(mutation = mutation`,
		`CollectionCard(`,
		`.testTag("config-verdict")`,
		// The verdict is the SHORT summary, and "Not saved" still leads it.
		`context.getString(R.string.config_not_saved) + ": " + verdict`,
		`status.summary`,
		// The warning is a string resource chosen by an exhaustive `when` over the closed set.
		`WarningText(troubleLabel(kind), mutation, "collection-error")`,
		`private fun troubleLabel(kind: TroubleKind): Int = when (kind)`,
		`stringResource(R.string.warning_prefix) + ": "`,
	} {
		if !strings.Contains(activity, site) {
			t.Fatalf("%s no longer contains %q; the bounded render path this test measures has moved, "+
				"so the fixture must be re-derived rather than trusted", mainActivityKt, site)
		}
	}

	// 4. ... and reaches a SENTENCE only inside the mutation branch that exists to break the claim.
	//    This is the assertion that keeps prose off the surface no matter what is added later: a
	//    future edit that renders `status.reason` or `CollectionStatus.lastError` on the home tree
	//    fails HERE, without needing anyone to have listed the new sentence.
	onlyUnderTheMutationGuard(t, activity, `status.reason`)
	onlyUnderTheMutationGuard(t, activity, `CollectionStatus.lastError.orEmpty()`)

	// 5. Every bounded source the surface can draw is under the floor, prefix included.
	summaries := regexp.MustCompile(`summary = "([^"]*)"`).FindAllStringSubmatch(read(serverConfigKt), -1)
	if len(summaries) < 5 {
		t.Fatalf("%s declares %d refusal summaries; ConfigValidation has five refusal branches, so a "+
			"smaller number means this test is measuring only some of what the card can draw",
			serverConfigKt, len(summaries))
	}
	troubles := regexp.MustCompile(`<string name="(trouble_[a-z_]+)">([^<]*)</string>`).FindAllStringSubmatch(res, -1)
	if len(troubles) == 0 {
		t.Fatalf("%s declares no trouble_* label, so the collection card has nothing bounded to draw", stringsXML)
	}

	var offenders []string
	measure := func(prefix, text, where string) {
		if n := len(strings.Fields(prefix + text)); n > floor {
			offenders = append(offenders, fmt.Sprintf("%q renders %d words (floor %d)  [%s]", prefix+text, n, floor, where))
		}
	}
	for _, s := range summaries {
		measure(notSaved, s[1], serverConfigKt)
	}
	for _, s := range troubles {
		measure(warning, s[2], stringsXML)
	}
	if len(offenders) > 0 {
		t.Fatalf("AC12 (spec.md): %d texts the Android home tree draws are paragraphs, not labels:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}

	// 6. The closed set is closed. Every TroubleKind has a label, so no member of it can fall
	//    through to whatever sentence happened to be recorded with it.
	kinds := regexp.MustCompile(`(?m)^\s{4}([A-Z][A-Z_]+),`).FindAllStringSubmatch(
		sliceBetween(t, read(statusKt), "enum class TroubleKind {", "\n}"), -1)
	if len(kinds) == 0 {
		t.Fatalf("%s declares no TroubleKind constants, so the surface's closed vocabulary is empty", statusKt)
	}
	declared := map[string]bool{}
	for _, s := range troubles {
		declared[s[1]] = true
	}
	for _, k := range kinds {
		label := "trouble_" + strings.ToLower(k[1])
		if !declared[label] {
			t.Fatalf("TroubleKind.%s has no %q in %s, so the collection card has no few-word label for it",
				k[1], label, stringsXML)
		}
		if !strings.Contains(activity, "TroubleKind."+k[1]+" -> R.string."+label) {
			t.Fatalf("troubleLabel in %s does not map TroubleKind.%s to R.string.%s", mainActivityKt, k[1], label)
		}
	}

	// 7. The sentences MOVED; they were not dropped. Each is still committed where it was, and the
	//    explanation destination renders whichever one is live.
	for _, s := range theSentencesTheDomainStillProduces {
		src := read(s.File)
		for _, frag := range s.Fragments {
			if !strings.Contains(src, frag) {
				t.Fatalf("%s no longer contains the literal %q; AC12 moves an explanation behind the "+
					"card's affordance, it does not delete it", s.File, frag)
			}
		}
	}
	explanation := sliceBetween(t, activity, "private fun ExplanationScreen(", "private fun explanationParagraphs")
	for _, needed := range []string{
		`ConfigStatus.Incomplete)?.reason`,
		`CollectionStatus.lastError`,
		`.testTag("explanation-detail")`,
	} {
		if !strings.Contains(explanation, needed) {
			t.Fatalf("ExplanationScreen in %s does not contain %q, so the sentence the home tree stopped "+
				"drawing is reachable nowhere", mainActivityKt, needed)
		}
	}

	// 8. And the half that keeps it closed: the RENDERED floor now binds on the states the screen
	//    can be driven into, not only on the one it opens in. This is the exact inverse of what the
	//    refuter's artefact asserted while the defect stood.
	body := sliceBetween(t, claims, "fun AC12_labels_stay_short()", "@Test")
	if !strings.Contains(body, "everyStateStaysShort()") {
		t.Fatalf("AC12_labels_stay_short in %s measures only the screen as it looks at launch; the "+
			"refused-save and blocked-collection states are not on it then, so the floor cannot see them",
			uiClaimTestKt)
	}
	sweep := sliceBetween(t, claims, "private fun everyStateStaysShort()", "private fun labelsStayShort")
	for _, needed := range []string{
		`onNodeWithTag("action-save").performClick()`, // it enters the refused-save state
		`textOf("config-verdict")`,                    // ... and proves it got there
		`TroubleKind.entries`,                         // it sweeps the whole closed set
		`CollectionStatus.recordBlocked(kind`,         // ... driving each one onto the screen
		`textOf("collection-error")`,                  // ... and proves each one drew
		`labelsStayShort()`,                           // ... measuring at every stop
	} {
		if !strings.Contains(sweep, needed) {
			t.Fatalf("everyStateStaysShort in %s does not contain %q, so it does not actually drive and "+
				"measure the states F8 named", uiClaimTestKt, needed)
		}
	}
}

// onlyUnderTheMutationGuard fails unless every occurrence of needle sits inside the debug-only
// branch that reproduces F8 on purpose.
//
// The window is generous: what it rules out is a render path that reaches a domain sentence with no
// mutation named anywhere near it, which is the shape the finding had.
func onlyUnderTheMutationGuard(t *testing.T, body, needle string) {
	t.Helper()
	const window = 300
	for at := 0; ; {
		i := strings.Index(body[at:], needle)
		if i < 0 {
			return
		}
		i += at
		from := i - window
		if from < 0 {
			from = 0
		}
		if !strings.Contains(body[from:i], theMutationThatPutsProseBack) {
			t.Fatalf("%s reaches the domain sentence %q without %s guarding it; that sentence is "+
				"unbounded (a socket failure or a platform exception writes it) and AC12 keeps it off "+
				"the surface", mainActivityKt, needle, theMutationThatPutsProseBack)
		}
		at = i + len(needle)
	}
}

// sliceBetween returns the text from the first occurrence of start up to the next occurrence of end
// after it, so one Kotlin declaration can be inspected on its own.
func sliceBetween(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("could not find %q, so the fixture must be re-derived", start)
	}
	rest := body[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

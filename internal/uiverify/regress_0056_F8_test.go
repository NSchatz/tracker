// Regression artefact for S0056 impl-gate finding F8 (refuter, impl ordinal 2).
//
// AC12 of work/specs/S0056-tracker-frontend-conventions/spec.md:
//
//	"WHEN the Android screen is rendered on an emulator at a 360dp-wide phone profile, THE SYSTEM
//	 SHALL show no text clipped or pushed outside the display and SHALL require no horizontal
//	 scrolling; every label, warning and status line drawn on the screen itself SHALL be short
//	 enough to read at a glance, with the explanatory paragraphs that stand there today reachable
//	 instead through exactly one affordance per card [...]"
//
// The static strings were shortened and their paragraphs moved to the explanation destination. Two
// DYNAMIC texts were not: the server card's validated verdict (`config-verdict`) and the collection
// card's warning (`collection-error`) render whatever sentence the domain layer produced, verbatim,
// on the home tree. Those sentences are 7 to 24 words before the composable's own prefix is added,
// which is up to three times the floor the instrumented suite itself enforces
// (UiClaimTest.MAX_LABEL_WORDS).
//
// The Android brevity assertion cannot see it: AC12_labels_stay_short launches the screen and
// measures it, and neither of those two states is on the screen at launch. The browser half of this
// same diff closed the equivalent hole in the other direction, widening chromeTexts() from
// "[data-region] subtrees" to the whole body precisely because "a floor that only looked inside
// [data-region] could be dodged by moving a paragraph one element out" (audit.go). The Android floor
// is still dodged by a state the suite never renders.
//
// WHAT THIS TEST IS AND IS NOT. It measures the length of committed string literals and the presence
// of the call sites that put them on the home tree. Both are facts about the source, not about a
// rendering, so no browser or emulator is needed to decide them and F2's "grade a rendered claim
// with the runtime that draws it" is not in tension: the rendered claim here would be graded by an
// instrumented case that presses Save with no token, which this container has no emulator to run.
// The test fixes the literals verbatim and fails loudly if one has been edited, so it can never pass
// by drifting away from what it measures.
//
// It is a REFUTER artefact: it documents the defect. Fixing it is upstream's job, and the fix is not
// "delete this file". Either the surface stops rendering unbounded prose (a short verdict word, with
// the sentence behind the card's existing "About server settings" affordance), or the brevity
// assertion is extended to render those states so the floor actually binds on them.
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
	mainActivityKt = androidSrc("main", "java", "com", "nschatz", "tracker", "ui", "MainActivity.kt")
	stringsXML     = androidSrc("main", "res", "values", "strings.xml")
	uiClaimTestKt  = androidSrc("androidTest", "java", "com", "nschatz", "tracker", "ui", "UiClaimTest.kt")
)

// onScreenText is one sentence the home tree renders verbatim, with the prefix the composable puts
// in front of it and the source fragments the sentence is assembled from.
type onScreenText struct {
	Tag       string            // the Compose testTag it is drawn into
	Prefix    string            // what MainActivity concatenates ahead of it
	File      string            // where the sentence is committed
	Fragments []string          // the literal, in the pieces the source spells it in
	Fills     map[string]string // Kotlin interpolations, with one real value each
}

// rendered is the sentence a person reads: the composable's prefix, the literal, and each Kotlin
// interpolation replaced by a value the running app actually produces there.
func (o onScreenText) rendered() string {
	text := o.Prefix + strings.Join(o.Fragments, "")
	for placeholder, value := range o.Fills {
		text = strings.ReplaceAll(text, placeholder, value)
	}
	return text
}

// theSentencesTheHomeTreeRenders are the unbounded texts this screen puts on the home tree.
//
// `Prefix` is what the composable adds: ServerConfigCard writes `config_not_saved` + ": " + reason
// into `config-verdict`, and WarningLiteral writes `warning_prefix` + ": " + text into
// `collection-error`. Both prefixes are read out of strings.xml below rather than assumed.
var theSentencesTheHomeTreeRenders = []onScreenText{
	{
		Tag:       "collection-error",
		File:      collectionSvc,
		Fragments: []string{"Location permission was revoked, so collection stopped."},
	},
	{
		Tag:  "collection-error",
		File: collectionSvc,
		Fragments: []string{
			"Collection could not start: Android refused the location foreground service ",
			"(" + interpolatedExceptionName + "). Check that location permission is granted, then ",
			"start it again from this screen.",
		},
		// The one interpolation, filled with a class Android really throws there.
		Fills: map[string]string{interpolatedExceptionName: "ForegroundServiceStartNotAllowedException"},
	},
	{
		Tag:       "collection-error",
		File:      serverConfigKt,
		Fragments: []string{"No server URL is set. Enter the tracker server address."},
	},
	{
		Tag:       "collection-error",
		File:      serverConfigKt,
		Fragments: []string{"The server URL must start with https:// (or http:// for local testing)."},
	},
	{
		Tag:       "collection-error",
		File:      serverConfigKt,
		Fragments: []string{"Refusing to send the device token over plaintext http://. Use https://."},
	},
	{
		Tag:       "collection-error",
		File:      serverConfigKt,
		Fragments: []string{"The server URL is missing a host name."},
	},
	{
		Tag:       "collection-error",
		File:      serverConfigKt,
		Fragments: []string{"No device token is set. Run `tracker enroll` on the server and paste the token here."},
	},
	{
		Tag:       "config-verdict",
		File:      serverConfigKt,
		Fragments: []string{"No device token is set. Run `tracker enroll` on the server and paste the token here."},
	},
	{
		Tag:       "config-verdict",
		File:      serverConfigKt,
		Fragments: []string{"The server URL must start with https:// (or http:// for local testing)."},
	},
}

// interpolatedExceptionName is spelled out of its pieces so this file carries no Kotlin template
// that a Go tool would mistake for one of its own.
const interpolatedExceptionName = "$" + "{e.javaClass.simpleName}"

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
	prefixOf := func(name string) string {
		p := regexp.MustCompile(`<string name="` + name + `">([^<]*)</string>`).FindStringSubmatch(res)
		if p == nil {
			t.Fatalf("%s no longer declares %q, so the rendered prefix cannot be established", stringsXML, name)
		}
		return p[1] + ": "
	}
	notSaved := prefixOf("config_not_saved")
	warning := prefixOf("warning_prefix")

	// 3. The call sites that put these sentences on the HOME tree. If any of them has moved, this
	//    test is measuring something the screen no longer does and must say so rather than fail.
	activity := read(mainActivityKt)
	for _, site := range []string{
		`.testTag("home")`,
		`ServerConfigCard(mutation = mutation`,
		`CollectionCard(`,
		`context.getString(R.string.config_not_saved) + ": " + status.reason`,
		`.testTag("config-verdict")`,
		`CollectionStatus.lastError?.let { WarningLiteral(it, mutation, "collection-error") }`,
		`stringResource(R.string.warning_prefix) + ": "`,
	} {
		if !strings.Contains(activity, site) {
			t.Fatalf("%s no longer contains %q; the render path this test measures has moved, so the "+
				"fixture must be re-derived rather than trusted", mainActivityKt, site)
		}
	}

	// 4. AC12's own assertion never renders either state, which is why the floor does not bind on
	//    them. The claim case launches the screen and measures it; it presses nothing and records no
	//    error.
	body := sliceBetween(t, claims, "fun AC12_labels_stay_short()", "@Test")
	for _, absent := range []string{"action-save", "recordBlocked", "config-verdict", "collection-error"} {
		if strings.Contains(body, absent) {
			t.Fatalf("AC12_labels_stay_short now mentions %q, so it may render the states this test says "+
				"it cannot see; re-derive the finding", absent)
		}
	}

	// 5. Every sentence is committed where this test says it is. A literal that has been edited
	//    fails HERE, loudly, instead of quietly dropping out of the measurement.
	for _, s := range theSentencesTheHomeTreeRenders {
		src := read(s.File)
		for _, frag := range s.Fragments {
			if !strings.Contains(src, frag) {
				t.Fatalf("%s no longer contains the literal %q; re-derive this fixture", s.File, frag)
			}
		}
	}

	// 6. The measurement AC12 asks for, applied the way UiClaimTest applies it.
	var offenders []string
	for _, s := range theSentencesTheHomeTreeRenders {
		prefix := warning
		if s.Tag == "config-verdict" {
			prefix = notSaved
		}
		s.Prefix = prefix
		text := s.rendered()
		if n := len(strings.Fields(text)); n > floor {
			offenders = append(offenders, fmt.Sprintf("%s renders %d words (floor %d): %q  [%s]", s.Tag, n, floor, text, s.File))
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("AC12 (spec.md): %d texts drawn on the Android home tree are paragraphs, not labels.\n"+
			"Every one is a warning or a status line the screen draws on itself, and the instrumented\n"+
			"floor of %d words never sees any of them because AC12_labels_stay_short only ever measures\n"+
			"the screen as it looks at launch:\n  %s",
			len(offenders), floor, strings.Join(offenders, "\n  "))
	}
}

// sliceBetween returns the text from the first occurrence of start up to the next occurrence of end
// after it, so one Kotlin test body can be inspected on its own.
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

// A demonstration on the Android surface must re-run its claim's measuring code against a mutated
// screen. This is the structural half of impl-gate observation F10.
//
// AC18 of work/specs/S0056-tracker-frontend-conventions/spec.md:
//
//	"[...] and SHALL fail if fewer demonstrations ran than there are rendered claims for that
//	 surface - a demonstration being the same measuring code shown going red against a surface
//	 mutated to break exactly one claim - so that no assertion here can pass vacuously."
//
// CheckAndroidRun pairs a claim with its demonstration BY NAME, recovered from what the emulator
// wrote (see notes.md R13, which explains why the name is the pairing and why the count cannot be
// parsed out of Kotlin). That is the right home for the COUNT, and it is not touched here. What it
// cannot see is the inside of a case: an empty `@Test fun X_demonstration() {}` passes, so it lands
// in the emulator's results as a green demonstration and satisfies the count.
//
// This closes that. It reads the suite and requires each demonstration to (a) name a UiMutation
// other than NONE, (b) assert a failure rather than merely run, and (c) call every measuring helper
// its claim calls - so a demonstration cannot drift away from the claim it is evidence for.
//
// WHAT IT DOES NOT CLOSE, stated rather than implied: nothing here proves at RUNTIME that the
// mutation the case asked for was honoured by the app. The browser surface has that
// (Stack.MutationError feeds Result.MutationBroke); the emulator's equivalent needs the suite to
// write a per-demonstration marker that the route pulls back off the device, and this session has
// no emulator to develop it against. See notes.md.
//
// Reading Kotlin here is not the thing F2 forbids. F2 reserves the RUNTIME for a claim about what a
// person sees; whether a test case applies a mutation and asserts a failure is a property of the
// suite, decidable from the source, and no cascade, layout or paint is being judged.
package uiverify

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// declaredFunctions maps every `fun NAME(` in the source to its brace-matched body.
func declaredFunctions(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	decl := regexp.MustCompile(`fun ([A-Za-z_][A-Za-z0-9_]*)\(`)
	for _, loc := range decl.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		open := strings.Index(src[loc[1]:], "{")
		if open < 0 {
			continue
		}
		open += loc[1]
		depth, end := 0, -1
		for i := open; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			t.Fatalf("could not find the end of fun %s; the suite must be re-read rather than trusted", name)
		}
		out[name] = src[open : end+1]
	}
	return out
}

// helpersCalledBy returns the measuring helpers a case body invokes, in sorted order.
func helpersCalledBy(body string, declared map[string]string) []string {
	var out []string
	for name := range declared {
		if strings.HasPrefix(name, "AC") || strings.HasSuffix(name, "_demonstration") {
			continue // a claim case, not a helper
		}
		if strings.Contains(body, name+"(") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryAndroidDemonstrationRunsItsClaimAgainstAMutation(t *testing.T) {
	body, err := os.ReadFile(uiClaimTestKt)
	if err != nil {
		t.Fatalf("reading %s: %v", uiClaimTestKt, err)
	}
	src := string(body)
	declared := declaredFunctions(t, src)

	// The measuring helpers that are fixtures rather than assertions. A demonstration is not
	// required to re-run these, because they set the screen up; they do not grade it.
	fixtures := map[string]bool{
		"aRunHasHappened":        true,
		"nothingHasBeenMeasured": true,
		"theQueueCannotBeRead":   true,
	}

	claims := 0
	for name := range declared {
		if !strings.HasPrefix(name, "AC") || strings.HasSuffix(name, "_demonstration") {
			continue
		}
		claims++
		demo, ok := declared[name+"_demonstration"]
		if !ok {
			t.Fatalf("the claim %s has no %s_demonstration beside it, so nothing shows its assertion "+
				"going red and it can pass vacuously", name, name)
		}

		// (a) it applies a mutation, and not the inert one.
		mutations := regexp.MustCompile(`UiMutation\.([A-Z][A-Z_]*)`).FindAllStringSubmatch(demo, -1)
		applied := false
		for _, m := range mutations {
			if m[1] != "NONE" {
				applied = true
			}
		}
		if !applied {
			t.Fatalf("%s_demonstration names no UiMutation other than NONE, so it launches the SAME "+
				"screen the claim passed against and its pass is not evidence of anything", name)
		}

		// (b) it asserts a failure rather than merely running.
		if !strings.Contains(demo, "assertFails") {
			t.Fatalf("%s_demonstration does not call assertFails, so it never asserts that the claim "+
				"went red against the mutated screen", name)
		}

		// (c) it re-runs the claim's own measuring code.
		for _, helper := range helpersCalledBy(declared[name], declared) {
			if fixtures[helper] {
				continue
			}
			if !strings.Contains(demo, helper+"(") {
				t.Fatalf("%s measures with %s(), and %s_demonstration never calls it; the demonstration "+
					"is evidence for a different assertion than the claim it is named after",
					name, helper, name)
			}
		}
	}

	// A sweep that matched nothing would pass forever, so the floor is what the committed record
	// accounts for on this surface: the assertions it names PLUS the ones it defers to another item.
	// A parked case is still audited - it has to be well formed when the item that owns it picks it
	// up - so it counts here even though it does not run on the emulator. Reading the floor off the
	// record rather than writing a number down is what stops the two drifting apart.
	recorded, err := recordedAssertions(theRepoRoot, AndroidSurface)
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	deferred, err := deferredAssertions(theRepoRoot)
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	floor := len(recorded) + len(deferred)
	if floor == 0 {
		t.Fatal("the committed record accounts for no assertion at all on the android screen, so this " +
			"sweep would have no floor to check against")
	}
	if claims < floor {
		t.Fatalf("found %d instrumented claims in %s; the record accounts for %d (%d graded here, %d "+
			"deferred), so this sweep is not reaching the suite it is supposed to be reading",
			claims, uiClaimTestKt, floor, len(recorded), len(deferred))
	}
}

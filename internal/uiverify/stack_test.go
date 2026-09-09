// The demonstration machinery, checked without a browser.
//
// A demonstration is only evidence while its mutation still applies. The page is edited far more
// often than the mutation set is, so the failure mode that matters here is ROT: a `Find` literal that
// no longer occurs in the served bytes. The response becomes a 500, the check goes red at the error
// page, and - before this was recorded - the run counted that redness as "demonstrated able to fail".
// It is not: nothing about the claim was ever broken.
package uiverify

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func getMap(t *testing.T, s *Stack) string {
	t.Helper()
	resp, err := http.Get(s.URL() + "/map")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestAMutationThatStillAppliesIsNotReportedAsBroken(t *testing.T) {
	t.Parallel()
	s := NewStack()
	defer s.Close()

	// A literal the shipped page really contains. If this ever stops being true the sub-test below
	// would pass for the wrong reason, so assert it against the pristine response first.
	const present = `<button id="watch">`
	if !strings.Contains(getMap(t, s), present) {
		t.Fatalf("the served map no longer contains %q, so this case cannot test what it says it does", present)
	}

	s.SetMutation(&Mutation{ID: "test", Find: present, Replace: `<button id="watch" hidden>`})
	body := getMap(t, s)
	if err := s.MutationError(); err != nil {
		t.Fatalf("a mutation that applies cleanly was reported as broken: %v", err)
	}
	if !strings.Contains(body, `<button id="watch" hidden>`) {
		t.Fatal("the mutation did not reach the served bytes")
	}
}

func TestAMutationThatNoLongerAppliesIsNotADemonstration(t *testing.T) {
	t.Parallel()
	s := NewStack()
	defer s.Close()

	s.SetMutation(&Mutation{ID: "AC-stale", Find: "a literal this page has never contained", Replace: "x"})
	body := getMap(t, s)

	// The response is an error page, so any check run against it would go red - which is exactly the
	// redness that must NOT be counted.
	if !strings.Contains(body, "mutation did not apply") {
		t.Fatalf("a stale mutation did not turn the response into an error page: %q", body)
	}
	err := s.MutationError()
	if err == nil {
		t.Fatal("a mutation whose Find no longer occurs was not recorded as broken, so the run would " +
			"count the resulting error page as a demonstration (AC18)")
	}
	if !strings.Contains(err.Error(), "AC-stale") {
		t.Fatalf("the recorded failure does not name the mutation: %v", err)
	}

	// Installing the next mutation clears it, so one rotted demonstration cannot condemn the rest.
	s.SetMutation(nil)
	if s.MutationError() != nil {
		t.Fatal("the recorded failure survived the mutation being cleared")
	}
}

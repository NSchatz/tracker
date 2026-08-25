// The map page is the one part of this repository that no Go test can execute: it is a single
// vendored HTML page with no build step, and adding a headless-browser harness as a dependency of
// THIS repo is a decision the presentation-state change may not take on its own. So the rendered
// outcomes are verified by an eye, against a running stack, and the record of that ride in the diff
// as MAP-VERIFICATION.md.
//
// A written record rots the moment nobody is checking it, and "we will fill it in later" is exactly
// how a document that promises evidence ends up promising nothing. This test is the check: it reads
// the committed record and fails while any of AC24 to AC30, or the actor / date / commit the record
// must identify, is still unobserved. It asserts nothing about what was SEEN - it cannot - only that
// somebody has actually looked and said so, which is the difference between evidence and intent.
//
// It began as the impl gate's own failing probe for finding F1 (impl verdict ordinal 1). The name is
// kept verbatim so that probe re-runs unchanged against this branch; it stays in the suite because
// the property it guards outlives the finding.
package server_test

import (
	"os"
	"strings"
	"testing"
)

// TestRegress0010F1MapVerificationRecordIsObserved reads the committed verification record and
// fails while any AC24-to-AC30 row, or the commit/actor/date the record must identify, is still
// unobserved.
func TestRegress0010F1MapVerificationRecordIsObserved(t *testing.T) {
	const path = "../../MAP-VERIFICATION.md"

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("AC35 requires a committed verification record in the diff; %s is unreadable: %v", path, err)
	}
	doc := string(raw)

	// Section 5 is the record. Everything above it is the procedure, which AC35 also wants but
	// which is not the evidence: the evidence is what somebody SAW.
	_, record, found := strings.Cut(doc, "## 5. The record")
	if !found {
		t.Fatalf("%s carries no record section, only a procedure; AC35 wants the steps run, "+
			"how each outcome was observed and by whom", path)
	}

	if n := strings.Count(record, "NOT YET OBSERVED"); n > 0 {
		t.Errorf("the verification record carries %d NOT YET OBSERVED entries. AC35 requires the "+
			"steps run for each of AC24 to AC30, HOW each outcome was observed and BY WHOM, dated "+
			"and identifying the commit it was run against. Nothing on the map page has been seen "+
			"by anybody, so AC24 to AC30 have no evidence in the diff at all.", n)
	}

	// Each criterion AC35 enumerates must appear in the record, not only in the procedure.
	for _, ac := range []string{"AC24", "AC25", "AC26", "AC27", "AC28", "AC29", "AC30"} {
		if !strings.Contains(record, ac) {
			t.Errorf("the verification record never names %s", ac)
		}
	}
}

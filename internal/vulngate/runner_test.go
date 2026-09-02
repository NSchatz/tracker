package vulngate

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerInvokesThePinnedTool(t *testing.T) {
	args, err := GovulncheckRunner{Version: "v1.1.4", Patterns: []string{"./..."}}.Args()
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	want := []string{"run", "golang.org/x/vuln/cmd/govulncheck@v1.1.4", "./..."}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

// C2: GOVULNCHECK_VERSION may move forward. It may not be removed or floated. A gate whose tool
// version is `latest` answers differently on different days and cannot be reproduced from the tree.
func TestRunnerRefusesAnUnpinnedVersion(t *testing.T) {
	for _, version := range []string{"", "latest", "master", "v1.1", "1.1.4", "@v1.1.4"} {
		if _, err := (GovulncheckRunner{Version: version, Patterns: []string{"./..."}}).Args(); err == nil {
			t.Errorf("Args accepted unpinned version %q", version)
		}
	}
}

func TestRunnerRefusesAnEmptyScanScope(t *testing.T) {
	if _, err := (GovulncheckRunner{Version: "v1.1.4"}).Args(); err == nil {
		t.Error("Args accepted a scan with no package patterns")
	}
}

func TestRunnerReportsAProcessThatCouldNotStart(t *testing.T) {
	// An unpinned version never reaches exec, so drive the failure through Run itself.
	_, err := GovulncheckRunner{Version: "latest", Patterns: []string{"./..."}}.Run(context.Background())
	if err == nil {
		t.Fatal("Run started govulncheck with an unpinned version")
	}
	if !strings.Contains(err.Error(), "GOVULNCHECK_VERSION") {
		t.Errorf("failure does not point at the Makefile pin: %v", err)
	}
}

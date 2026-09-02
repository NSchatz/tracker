package vulngate

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerBuildsThePinnedTool(t *testing.T) {
	r := GovulncheckRunner{Version: "v1.1.4", Patterns: []string{"./..."}}

	install, err := r.InstallArgs()
	if err != nil {
		t.Fatalf("InstallArgs: %v", err)
	}
	want := []string{"install", "golang.org/x/vuln/cmd/govulncheck@v1.1.4"}
	if !reflect.DeepEqual(install, want) {
		t.Errorf("got %v, want %v", install, want)
	}

	scan, err := r.ScanArgs()
	if err != nil {
		t.Fatalf("ScanArgs: %v", err)
	}
	// govulncheck is invoked with the scan scope and NOTHING else: no -show, no -format, no flag that
	// could narrow or soften what it reports.
	if !reflect.DeepEqual(scan, []string{"./..."}) {
		t.Errorf("got %v, want [./...]", scan)
	}
}

// C2: GOVULNCHECK_VERSION may move forward. It may not be removed or floated. A gate whose tool
// version is `latest` answers differently on different days and cannot be reproduced from the tree.
func TestRunnerRefusesAnUnpinnedVersion(t *testing.T) {
	for _, version := range []string{"", "latest", "master", "v1.1", "1.1.4", "@v1.1.4"} {
		if _, err := (GovulncheckRunner{Version: version, Patterns: []string{"./..."}}).InstallArgs(); err == nil {
			t.Errorf("InstallArgs accepted unpinned version %q", version)
		}
	}
}

func TestRunnerRefusesAnEmptyScanScope(t *testing.T) {
	if _, err := (GovulncheckRunner{Version: "v1.1.4"}).ScanArgs(); err == nil {
		t.Error("ScanArgs accepted a scan with no package patterns")
	}
}

func TestRunnerReportsAToolItCannotBuild(t *testing.T) {
	// An unpinned version never reaches `go install`, so drive the failure through Run itself and
	// confirm it comes back as an error rather than as a clean, empty result.
	res, err := GovulncheckRunner{Version: "latest", Patterns: []string{"./..."}}.Run(context.Background())
	if err == nil {
		t.Fatal("Run built govulncheck from an unpinned version")
	}
	if res.ExitCode != 0 || res.Stdout != "" {
		t.Errorf("a tool that never ran returned a result that looks like one that did: %+v", res)
	}
	if !strings.Contains(err.Error(), "GOVULNCHECK_VERSION") {
		t.Errorf("failure does not point at the Makefile pin: %v", err)
	}
}

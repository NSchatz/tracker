package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is two directories up from internal/toolchain, which is where `go test` runs this package.
const repoRoot = "../.."

// TestRepoToolchainPinsAgree is THE assertion this package exists for, and it runs inside `make test`,
// which is a prerequisite of `make check-go` and therefore of `make check`. Bump the Dockerfile without
// the CI workflow (or the reverse) and this fails, naming both files.
func TestRepoToolchainPinsAgree(t *testing.T) {
	pins, err := CollectPins(repoRoot)
	if err != nil {
		t.Fatalf("CollectPins: %v", err)
	}
	if len(pins) < 2 {
		t.Fatalf("found %d toolchain pins (%v); tracker states the Go version in the Dockerfile AND in CI, so fewer than two means the check stopped looking", len(pins), pins)
	}
	floor, err := LanguageFloor(repoRoot)
	if err != nil {
		t.Fatalf("LanguageFloor: %v", err)
	}
	if err := Verify(pins, floor); err != nil {
		t.Fatalf("toolchain pins disagree:\n%v", err)
	}
	t.Logf("Go toolchain pinned to %s in %d files; go.mod language floor %s", pins[0].Version, len(pins), floor.Version)
}

// TestRepoPinCheckBites proves the live check above would actually fail on a one-sided bump, by taking
// the repo's real pins and moving exactly one of them. A green check that cannot go red proves nothing.
func TestRepoPinCheckBites(t *testing.T) {
	pins, err := CollectPins(repoRoot)
	if err != nil {
		t.Fatalf("CollectPins: %v", err)
	}
	floor, err := LanguageFloor(repoRoot)
	if err != nil {
		t.Fatalf("LanguageFloor: %v", err)
	}

	for i := range pins {
		mutated := append([]Pin(nil), pins...)
		mutated[i].Version = "1.99.0"
		err := Verify(mutated, floor)
		if err == nil {
			t.Fatalf("moving %s alone did not fail the check", pins[i])
		}
		msg := err.Error()
		for _, p := range pins {
			if !strings.Contains(msg, p.File) {
				t.Errorf("failure message does not name %s:\n%s", p.File, msg)
			}
		}
	}
}

func TestCollectPinsFindsDockerfileAndWorkflows(t *testing.T) {
	root := writeTree(t, map[string]string{
		"Dockerfile":                    "FROM golang:1.25.14-bookworm AS build\nWORKDIR /src\n",
		".github/workflows/ci.yml":      "env:\n  GO_VERSION: \"1.25.14\"\n",
		".github/workflows/release.yml": "env:\n  GO_VERSION: '1.25.14'\n",
		"go.mod":                        "module example.com/x\n\ngo 1.25.12\n",
	})

	pins, err := CollectPins(root)
	if err != nil {
		t.Fatalf("CollectPins: %v", err)
	}
	if len(pins) != 3 {
		t.Fatalf("got %d pins, want 3: %v", len(pins), pins)
	}
	files := map[string]bool{}
	for _, p := range pins {
		files[p.File] = true
		if p.Version != "1.25.14" {
			t.Errorf("%s: got version %q, want 1.25.14", p, p.Version)
		}
	}
	for _, want := range []string{"Dockerfile", ".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		if !files[want] {
			t.Errorf("no pin collected from %s", want)
		}
	}

	floor, err := LanguageFloor(root)
	if err != nil {
		t.Fatalf("LanguageFloor: %v", err)
	}
	if floor.Version != "1.25.12" {
		t.Errorf("got language floor %q, want 1.25.12", floor.Version)
	}
	if err := Verify(pins, floor); err != nil {
		t.Errorf("Verify on an agreeing tree: %v", err)
	}
}

func TestCollectPinsRefusesToPassOnNothingFound(t *testing.T) {
	t.Run("no builder image", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"Dockerfile":               "FROM gcr.io/distroless/static-debian12:nonroot\n",
			".github/workflows/ci.yml": "env:\n  GO_VERSION: \"1.25.14\"\n",
		})
		if _, err := CollectPins(root); err == nil {
			t.Fatal("CollectPins accepted a Dockerfile with no golang builder image")
		}
	})

	t.Run("no workflow GO_VERSION", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"Dockerfile":               "FROM golang:1.25.14-bookworm AS build\n",
			".github/workflows/ci.yml": "jobs:\n  check:\n    runs-on: ubuntu-latest\n",
		})
		if _, err := CollectPins(root); err == nil {
			t.Fatal("CollectPins accepted a workflow set that states no Go version")
		}
	})
}

func TestVerify(t *testing.T) {
	docker := Pin{File: "Dockerfile", Line: 7, Version: "1.25.14", Kind: "image"}
	ci := Pin{File: ".github/workflows/ci.yml", Line: 20, Version: "1.25.14", Kind: "ci"}
	floor := Pin{File: "go.mod", Line: 3, Version: "1.25.12", Kind: "floor"}

	t.Run("agreeing", func(t *testing.T) {
		if err := Verify([]Pin{docker, ci}, floor); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("one-sided bump names both files", func(t *testing.T) {
		bumped := ci
		bumped.Version = "1.25.15"
		err := Verify([]Pin{docker, bumped}, floor)
		if err == nil {
			t.Fatal("Verify accepted disagreeing pins")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Dockerfile") || !strings.Contains(msg, ".github/workflows/ci.yml") {
			t.Errorf("failure does not name both files:\n%s", msg)
		}
		if !strings.Contains(msg, "1.25.14") || !strings.Contains(msg, "1.25.15") {
			t.Errorf("failure does not name both versions:\n%s", msg)
		}
	})

	t.Run("language floor above the toolchain", func(t *testing.T) {
		high := floor
		high.Version = "1.26.0"
		err := Verify([]Pin{docker, ci}, high)
		if err == nil {
			t.Fatal("Verify accepted a go directive above the pinned toolchain")
		}
		if !strings.Contains(err.Error(), "go.mod") {
			t.Errorf("failure does not name go.mod:\n%v", err)
		}
	})

	t.Run("language floor below the toolchain is fine", func(t *testing.T) {
		low := floor
		low.Version = "1.24.0"
		if err := Verify([]Pin{docker, ci}, low); err != nil {
			t.Fatalf("Verify rejected a language floor below the toolchain: %v", err)
		}
	})

	t.Run("downgrade below the minimum", func(t *testing.T) {
		old := Pin{File: "Dockerfile", Line: 7, Version: "1.25.9", Kind: "image"}
		oldCI := Pin{File: ".github/workflows/ci.yml", Line: 20, Version: "1.25.9", Kind: "ci"}
		lowFloor := Pin{File: "go.mod", Line: 3, Version: "1.24.0", Kind: "floor"}
		err := Verify([]Pin{old, oldCI}, lowFloor)
		if err == nil {
			t.Fatal("Verify accepted a toolchain below the minimum")
		}
		if !strings.Contains(err.Error(), MinimumVersion) {
			t.Errorf("failure does not name the floor %s:\n%v", MinimumVersion, err)
		}
	})

	t.Run("no pins at all", func(t *testing.T) {
		if err := Verify(nil, floor); err == nil {
			t.Fatal("Verify passed with no pins to compare")
		}
	})
}

func TestCompareIsNumericNotLexical(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.25.9", "1.25.14", -1}, // the case string comparison gets backwards
		{"1.25.14", "1.25.9", 1},
		{"1.25.14", "1.25.14", 0},
		{"1.25", "1.25.0", 0},
		{"1.26.0", "1.25.14", 1},
		{"1.25.12", "1.25.12", 0},
	}
	for _, c := range cases {
		if got := compare(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

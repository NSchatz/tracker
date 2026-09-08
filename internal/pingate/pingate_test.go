package pingate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is two directories up from internal/pingate, which is where `go test` runs this package.
const repoRoot = "../.."

// TestRepoIsPinned is THE assertion this package exists for, and it runs inside `make test`, which
// is a prerequisite of `make check-go` and therefore of `make check` and therefore of CI. Add an
// unpinned image, action, dependency or wrapper distribution and this fails naming it.
//
// It is the same body `make pin-check` runs, deliberately: the command exists so the verdict is
// reachable without a Docker daemon or an Android SDK, not so there are two different checks.
func TestRepoIsPinned(t *testing.T) {
	if err := CheckRepo(repoRoot); err != nil {
		t.Fatalf("pin gate FAILED on this repository:\n%v", err)
	}

	report, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, c := range report.Categories {
		t.Logf("%-42s examined %3d  (%s)", c.Name, c.Examined, c.Detail)
	}
}

// TestEveryCategoryActuallyLookedAtSomething is the AC-14 property stated as a test, borrowed from
// toolchain.CollectPins: a check that silently passes because a file moved or a pattern stopped
// matching is worse than no check. Every category must have examined at least one reference on this
// repository, and the failure must name which one went quiet.
func TestEveryCategoryActuallyLookedAtSomething(t *testing.T) {
	report, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Categories) != len(scanners) {
		t.Fatalf("got %d categories from %d scanners: every scanner must report a category, or one of them can go quiet unnoticed", len(report.Categories), len(scanners))
	}
	for _, c := range report.EmptyCategories() {
		t.Errorf("category %q examined nothing (%s)", c.Name, c.Detail)
	}
}

// TestEmptyCategoryIsARefusal proves the property above BITES, by scanning a tree that has none of
// the files the gate reads. A green check that cannot go red proves nothing.
func TestEmptyCategoryIsARefusal(t *testing.T) {
	report, err := Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	empty := report.EmptyCategories()
	// The node category asserts ABSENCE rather than counting nothing, so it is legitimately the one
	// category that is answered on an empty tree. Everything else must be reported as unlooked-at.
	if len(empty) != len(scanners)-1 {
		t.Fatalf("an empty tree produced %d empty categories, want %d: %v", len(empty), len(scanners)-1, empty)
	}
	err = report.Err()
	if err == nil {
		t.Fatal("Report.Err() passed a tree in which every category examined nothing")
	}
	for _, c := range empty {
		if !strings.Contains(err.Error(), c.Name) {
			t.Errorf("the refusal does not name the empty category %q:\n%v", c.Name, err)
		}
	}
}

// TestNodeCategoryAssertsAbsenceRatherThanCountingNothing is AC-11's distinction: tracker has no
// node in it, and "we looked and there is none" must not be the same green tick as "we stopped
// looking".
func TestNodeCategoryAssertsAbsenceRatherThanCountingNothing(t *testing.T) {
	report, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := category(t, report, "node manifests")
	if c.Examined == 0 {
		t.Fatalf("the node category counted nothing instead of asserting absence: %+v", c)
	}
	if !strings.Contains(c.Detail, "ABSENT") {
		t.Errorf("the node category does not say it asserted absence: %q", c.Detail)
	}
	// And the absence must be true, not assumed - across the WHOLE tree, the demonstration
	// directory the scan skips included. This is why the node demonstration commits its manifest
	// under an inert name and materialises it: a real `package.json` anywhere under this repository
	// would make "tracker has no node in it" false, however carefully the scanner avoided looking
	// at it, and an assertion of absence that is only true where the check happens to look is not
	// an assertion of absence.
	var manifests []string
	err = filepath.WalkDir(repoRoot, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if !e.IsDir() && (e.Name() == "package.json" || e.Name() == ".npmrc") {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("the node category asserted absence but the tree holds %v", manifests)
	}
}

// TestEveryRefusalNamesFileLineReferenceAndClause is AC-12 over every refusal the five committed
// demonstrations produce, plus the synthetic trees below. A refusal that says "something is
// unpinned" costs a reader the search that the check already did.
func TestEveryRefusalNamesFileLineReferenceAndClause(t *testing.T) {
	seen := 0
	for _, d := range Demonstrations {
		root, cleanup, err := CaseTree(repoRoot, d)
		if err != nil {
			t.Fatalf("CaseTree(%s): %v", d.Name, err)
		}
		report, err := Scan(root)
		cleanup()
		if err != nil {
			t.Fatalf("Scan(%s): %v", d.Name, err)
		}
		for _, v := range report.Violations {
			seen++
			msg := v.String()
			for _, want := range []string{
				v.File,
				":" + strconv.Itoa(v.Line) + ":",
				v.Reference,
				v.Clause.ID,
				ConventionsFile,
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal does not carry %q:\n  %s", want, msg)
				}
			}
			if v.Line < 1 {
				t.Errorf("refusal has no line number: %s", msg)
			}
			if v.Why == "" {
				t.Errorf("refusal explains nothing: %s", msg)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no refusals were produced at all, so this assertion checked nothing")
	}
}

// TestDemonstrationsAllGoRed is AC-13: five committed instances of the five forbidden shapes, each
// producing the refusal it exists to produce.
func TestDemonstrationsAllGoRed(t *testing.T) {
	red, err := RunDemonstrations(repoRoot)
	if err != nil {
		t.Fatalf("RunDemonstrations: %v", err)
	}
	if red != len(Demonstrations) {
		t.Fatalf("%d of %d demonstrations went red", red, len(Demonstrations))
	}
	if len(Demonstrations) != 5 {
		t.Fatalf("the conventions name five forbidden shapes; %d are demonstrated", len(Demonstrations))
	}
	for _, d := range Demonstrations {
		t.Logf("%-26s %s -> %s", d.Name, d.Shape, d.Clause.ID)
	}
}

// TestDemonstrationTreeHoldsOnlyTheDemonstrations closes the hole that excluding a directory from
// the scan would otherwise open.
func TestDemonstrationTreeHoldsOnlyTheDemonstrations(t *testing.T) {
	if err := verifyDemonstrationTree(repoRoot); err != nil {
		t.Fatalf("verifyDemonstrationTree: %v", err)
	}

	// And it bites: a stray directory parked there is refused by name.
	tmp := writeTree(t, map[string]string{
		filepath.Join(DemonstrationsDir, "smuggled-in-here", "Dockerfile"): "FROM golang:1.26.8-bookworm\n",
	})
	err := verifyDemonstrationTree(tmp)
	if err == nil {
		t.Fatal("verifyDemonstrationTree accepted an unexpected directory in the excluded tree")
	}
	if !strings.Contains(err.Error(), "smuggled-in-here") {
		t.Errorf("the refusal does not name the stray directory:\n%v", err)
	}
}

// TestScanSkipsOnlyTheDemonstrationTree proves the exclusion is a path, not a name: an unpinned
// Dockerfile anywhere ELSE under testdata is still caught.
func TestScanSkipsOnlyTheDemonstrationTree(t *testing.T) {
	tmp := writeTree(t, map[string]string{
		"internal/pingate/testdata/elsewhere/Dockerfile":                      "FROM golang:1.26.8-bookworm AS build\n",
		filepath.Join(DemonstrationsDir, "dockerfile-tag-only", "Dockerfile"): "FROM golang:1.26.8-bookworm AS build\n",
	})
	report, err := Scan(tmp)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	files := violationFiles(report)
	if !files["internal/pingate/testdata/elsewhere/Dockerfile"] {
		t.Errorf("an unpinned Dockerfile outside the demonstration tree was not caught: %v", report.Violations)
	}
	for f := range files {
		if strings.HasPrefix(f, DemonstrationsDir) {
			t.Errorf("the repository scan descended into the demonstration tree: %s", f)
		}
	}
}

// ---------------------------------------------------------------------------
// per-clause behaviour, on trees written here so each rule can be shown accepting AND refusing
// ---------------------------------------------------------------------------

func TestDockerfileFromRule(t *testing.T) {
	const good = "golang:1.26.8-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81"

	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"tag and digest", "FROM " + good + " AS build\n", false},
		{"tag only", "FROM golang:1.26.8-bookworm AS build\n", true},
		{"digest with no tag", "FROM golang@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81\n", true},
		{"no tag and no digest", "FROM golang\n", true},
		{"latest", "FROM golang:latest\n", true},
		{"uppercase digest", "FROM golang:1.26.8-bookworm@sha256:9FDC884AACC3BEC89B20FFC69F4BB369C78210E3E4F600387B5128B12C199F81\n", true},
		{"truncated digest", "FROM golang:1.26.8-bookworm@sha256:9fdc884aacc3bec8\n", true},
		{"platform flag before a pinned ref", "FROM --platform=$BUILDPLATFORM " + good + " AS build\n", false},
		{"build argument for the image", "ARG BASE\nFROM $BASE AS build\n", true},
		{"a later stage referring to an earlier one", "FROM " + good + " AS build\nFROM build AS test\n", false},
		{"a commented-out unpinned FROM", "# FROM golang:1.26.8-bookworm\nFROM " + good + "\n", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"Dockerfile": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			red := len(report.Violations) > 0
			if red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.body, report.Violations)
			}
			if red && report.Violations[0].Clause.ID != P2.ID && report.Violations[0].Clause.ID != P6.ID {
				t.Errorf("refusal cites %s, want P2 or P6: %s", report.Violations[0].Clause.ID, report.Violations[0])
			}
		})
	}
}

func TestComposeImageRule(t *testing.T) {
	const pinned = "postgis/postgis:16-3.4@sha256:44126d872ac91993766c341e369c539e8196614321765d36a6f1bab0419a5fa5"

	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"pulled image with a digest", "services:\n  db:\n    image: " + pinned + "\n", false},
		{"pulled image with no digest", "services:\n  db:\n    image: postgis/postgis:16-3.4\n", true},
		{"built service needs no digest", "services:\n  app:\n    build:\n      context: .\n    image: tracker:dev\n", false},
		{"built service with a bare name", "services:\n  app:\n    build: .\n    image: app\n", false},
		{"image from an environment variable", "services:\n  db:\n    image: ${DB_IMAGE}\n", true},
		{"no image key at all", "services:\n  app:\n    build:\n      context: .\n", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"docker-compose.yml": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v; violations: %v", red, c.wantRed, report.Violations)
			}
		})
	}
}

// TestComposeUnparseableIsRefused: "the gate could not read it" must never round to "it is fine".
func TestComposeUnparseableIsRefused(t *testing.T) {
	report, err := Scan(writeTree(t, map[string]string{"docker-compose.yml": "services:\n  db:\n   image: [unclosed\n"}))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Violations) == 0 {
		t.Fatal("a compose file that does not parse was accepted")
	}
}

func TestWorkflowUsesRule(t *testing.T) {
	const sha = "11d5960a326750d5838078e36cf38b85af677262"

	cases := []struct {
		name    string
		line    string
		wantRed bool
	}{
		{"sha with a version comment", "      - uses: actions/checkout@" + sha + " # v4", false},
		{"sha with no comment", "      - uses: actions/checkout@" + sha, true},
		{"sha with a comment that names no version", "      - uses: actions/checkout@" + sha + " # pinned", true},
		{"mutable tag", "      - uses: actions/checkout@v4", true},
		{"branch", "      - uses: actions/checkout@main", true},
		{"short sha", "      - uses: actions/checkout@11d5960", true},
		{"local action", "      - uses: ./.github/actions/setup", false},
		{"docker action with a digest", "      - uses: docker://alpine:3.20@sha256:" + strings.Repeat("a", 64), false},
		{"docker action with no digest", "      - uses: docker://alpine:3.20", true},
		{"reusable workflow at a sha", "      - uses: owner/repo/.github/workflows/x.yml@" + sha + " # v1", false},
		{"a commented-out mutable tag", "      # - uses: actions/checkout@v4", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" + c.line + "\n"
			report, err := Scan(writeTree(t, map[string]string{".github/workflows/ci.yml": body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.line, report.Violations)
			}
		})
	}
}

// TestRunsOnIsNotAnImageReference: `ubuntu-latest` is a runner LABEL selecting a GitHub-hosted
// machine pool, not something this repo can pin, and refusing it would be refusing a thing with no
// compliant form.
func TestRunsOnIsNotAnImageReference(t *testing.T) {
	body := "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make check\n"
	report, err := Scan(writeTree(t, map[string]string{".github/workflows/ci.yml": body}))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, v := range report.Violations {
		if strings.Contains(v.Reference, "ubuntu-latest") {
			t.Errorf("runs-on was treated as an image reference: %s", v)
		}
	}
}

func TestGoModuleRule(t *testing.T) {
	const head = "module example.com/x\n\ngo 1.26.0\n\ntoolchain go1.26.8\n\n"

	cases := []struct {
		name    string
		gomod   string
		gosum   bool
		wantRed bool
	}{
		{"exact versions", head + "require (\n\tgithub.com/google/uuid v1.6.0 // indirect\n)\n", true, false},
		{"a pseudo-version is exact", head + "require github.com/a/b v0.0.0-20240606120523-5a60cdf6a761\n", true, false},
		{"an +incompatible version is exact", head + "require github.com/a/b v2.1.0+incompatible\n", true, false},
		{"latest", head + "require (\n\tgithub.com/google/uuid latest\n)\n", true, true},
		{"no go.sum", head + "require github.com/google/uuid v1.6.0\n", false, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"go.mod": c.gomod}
			if c.gosum {
				files["go.sum"] = "github.com/google/uuid v1.6.0 h1:placeholder=\n"
			}
			report, err := Scan(writeTree(t, files))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v; violations: %v", red, c.wantRed, report.Violations)
			}
		})
	}
}

func TestMakefileToolVersionRule(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"exact calendar version", "STATICCHECK_VERSION ?= 2025.1.1\n", false},
		{"exact semantic version", "GOVULNCHECK_VERSION ?= v1.1.4\n", false},
		{"latest", "STATICCHECK_VERSION ?= latest\n", true},
		{"a shell expression", "STATICCHECK_VERSION ?= $(shell curl -s example.com/version)\n", true},
		{"an at-latest module spec", "STATICCHECK_VERSION := honnef.co/go/tools@latest\n", true},
		// The bare VERSION variable is this artefact's own version, computed by git describe. It is
		// not a pin on anything fetched, and refusing it would be refusing a thing with no pinned form.
		{"the artefact's own VERSION is not a tool pin", "VERSION ?= $(shell git describe --tags --always --dirty)\n", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"Makefile": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.body, report.Violations)
			}
		})
	}
}

func TestGradleWrapperRule(t *testing.T) {
	const url = `distributionUrl=https\://services.gradle.org/distributions/gradle-8.9-bin.zip`
	const sum = "d725d707bfabd4dfdc958c624003b3c80accc03f7037b5122c4b1d0ef15cecab"

	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"url and checksum", url + "\ndistributionSha256Sum=" + sum + "\n", false},
		{"url with no checksum", url + "\nvalidateDistributionUrl=true\n", true},
		{"a short checksum", url + "\ndistributionSha256Sum=d725d707\n", true},
		{"an uppercase checksum", url + "\ndistributionSha256Sum=" + strings.ToUpper(sum) + "\n", true},
		{"a version-less url", `distributionUrl=https\://example.com/gradle-latest.zip` + "\ndistributionSha256Sum=" + sum + "\n", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"android/gradle/wrapper/gradle-wrapper.properties": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.body, report.Violations)
			}
		})
	}
}

// TestGradleWrapperValidateUrlIsNotAChecksum is the specific confusion this rule exists to prevent:
// validateDistributionUrl checks the URL's shape, never the bytes it returns.
func TestGradleWrapperValidateUrlIsNotAChecksum(t *testing.T) {
	body := `distributionUrl=https\://services.gradle.org/distributions/gradle-8.9-bin.zip` + "\nvalidateDistributionUrl=true\n"
	report, err := Scan(writeTree(t, map[string]string{"android/gradle/wrapper/gradle-wrapper.properties": body}))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Violations) == 0 {
		t.Fatal("validateDistributionUrl=true was accepted in place of distributionSha256Sum")
	}
	if !strings.Contains(report.Violations[0].Why, "distributionSha256Sum") {
		t.Errorf("the refusal does not say what to add: %s", report.Violations[0])
	}
}

func TestGradleCatalogRule(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"exact literals", "[versions]\nagp = \"8.5.2\"\ncomposeBom = \"2024.06.00\"\n", false},
		{"a plus wildcard", "[versions]\ncoreKtx = \"1.13.+\"\n", true},
		{"a bare plus", "[versions]\ncoreKtx = \"+\"\n", true},
		{"latest", "[versions]\ncoreKtx = \"latest.release\"\n", true},
		{"the RELEASE keyword", "[versions]\ncoreKtx = \"RELEASE\"\n", true},
		{"a maven range", "[versions]\ncoreKtx = \"[1.0,2.0)\"\n", true},
		{"a snapshot", "[versions]\ncoreKtx = \"1.14.0-SNAPSHOT\"\n", true},
		{"a prerelease literal is exact", "[versions]\ncoreKtx = \"1.14.0-alpha01\"\n", false},
		{"version.ref is not an inline version", "[versions]\nagp = \"8.5.2\"\n[libraries]\nx = { group = \"g\", name = \"n\", version.ref = \"agp\" }\n", false},
		{"an inline dynamic version in [libraries]", "[versions]\nagp = \"8.5.2\"\n[libraries]\nx = { group = \"g\", name = \"n\", version = \"1.+\" }\n", true},
		{"a module coordinate with a dynamic version", "[versions]\nagp = \"8.5.2\"\n[libraries]\nx = { module = \"g:n:1.+\" }\n", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"android/gradle/libs.versions.toml": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.body, report.Violations)
			}
		})
	}
}

func TestGradleScriptRule(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantRed bool
	}{
		{"aliases only", "dependencies {\n    implementation(libs.androidx.core.ktx)\n}\n", false},
		{"a restated coordinate", "dependencies {\n    implementation(\"androidx.core:core-ktx:1.13.1\")\n}\n", true},
		{"a restated plugin version", "plugins {\n    id(\"com.android.application\") version \"8.5.2\"\n}\n", true},
		{"a plugin alias", "plugins {\n    alias(libs.plugins.android.application)\n}\n", false},
		// These are the shapes a naive coordinate matcher trips over. An SDK level is not a
		// dependency version, and a URL is not a Maven coordinate.
		{"an sdk level is not a version literal", "android {\n    compileSdk = 34\n    minSdk = 29\n}\n", false},
		{"a jvm target is not a version literal", "kotlinOptions {\n    jvmTarget = \"17\"\n}\n", false},
		{"a url is not a coordinate", "repositories {\n    maven(\"https://example.com/m2\")\n}\n", false},
		{"a commented-out coordinate", "dependencies {\n    // implementation(\"androidx.core:core-ktx:1.13.1\")\n}\n", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{"android/app/build.gradle.kts": c.body}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.body, report.Violations)
			}
		})
	}
}

// TestNodeLifecycleScriptsOptBackIn is the half of P4's node rule the committed demonstration does
// not carry: an .npmrc that switches lifecycle scripts back ON. The tree is written here rather
// than committed because a checked-in .npmrc is a credential-shaped file, and this repository has
// no node in it to need one.
func TestNodeLifecycleScriptsOptBackIn(t *testing.T) {
	const manifest = `{"name":"x","private":true,"dependencies":{"left-pad":"1.3.0"}}`
	const lock = `{"name":"x","lockfileVersion":3}`

	cases := []struct {
		name    string
		npmrc   string
		wantRed bool
	}{
		{"scripts off", "ignore-scripts=true\n", false},
		{
			name:    "scripts on with a committed reason",
			npmrc:   "# better-sqlite3 has no prebuilt binary for this platform and must compile at install time\nignore-scripts=false\n",
			wantRed: false,
		},
		{"scripts on with no reason", "ignore-scripts=false\n", true},
		{"scripts on with an empty comment", "#\nignore-scripts=false\n", true},
		{"scripts on with the reason after the switch", "ignore-scripts=false\n# a native module needs to compile at install time here\n", true},
		{"no setting at all", "registry=https://registry.npmjs.org/\n", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, map[string]string{
				"package.json":      manifest,
				"package-lock.json": lock,
				".npmrc":            c.npmrc,
			}))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v for %q; violations: %v", red, c.wantRed, c.npmrc, report.Violations)
			}
		})
	}
}

func TestNodeManifestRule(t *testing.T) {
	const lock = `{"name":"x","lockfileVersion":3}`

	cases := []struct {
		name    string
		files   map[string]string
		wantRed bool
	}{
		{
			name:    "locked, scripts off, exact spec",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":"1.3.0"}}`, "package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: false,
		},
		{
			name:    "a caret range is bounded above and the lockfile fixes it",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":"^1.3.0"}}`, "package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: false,
		},
		{
			name:    "the latest dist-tag",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":"latest"}}`, "package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: true,
		},
		{
			name:    "a star spec",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":"*"}}`, "package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: true,
		},
		{
			name:    "an unbounded range",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":">=1.3.0"}}`, "package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: true,
		},
		{
			name:    "no lockfile",
			files:   map[string]string{"package.json": `{"name":"x","dependencies":{"left-pad":"1.3.0"}}`, ".npmrc": "ignore-scripts=true\n"},
			wantRed: true,
		},
		{
			name:    "a root .npmrc covers a nested manifest",
			files:   map[string]string{"web/package.json": `{"name":"x","dependencies":{"left-pad":"1.3.0"}}`, "web/package-lock.json": lock, ".npmrc": "ignore-scripts=true\n"},
			wantRed: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Scan(writeTree(t, c.files))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if red := len(report.Violations) > 0; red != c.wantRed {
				t.Fatalf("red=%v want %v; violations: %v", red, c.wantRed, report.Violations)
			}
		})
	}
}

// TestCheckRepoRefusesOutsideAGitWorkTree: the go.sum tracking assertion is only answerable inside
// one, and dropping half an assertion because of where the gate ran is not an option.
func TestCheckRepoRefusesOutsideAGitWorkTree(t *testing.T) {
	err := CheckRepo(t.TempDir())
	if err == nil {
		t.Fatal("CheckRepo passed outside a git work tree")
	}
	if !strings.Contains(err.Error(), "git work tree") {
		t.Errorf("the refusal does not say why:\n%v", err)
	}
}

// TestCheckRepoBitesOnEachDemonstration is the whole-gate mutation test: point CheckRepo at a tree
// that is the repository plus one broken file, and it must refuse. A check that cannot be shown
// failing on the real shapes is not evidence.
func TestCheckRepoBitesOnEachDemonstration(t *testing.T) {
	for _, d := range Demonstrations {
		t.Run(d.Name, func(t *testing.T) {
			caseRoot, cleanup, err := CaseTree(repoRoot, d)
			if err != nil {
				t.Fatalf("CaseTree: %v", err)
			}
			defer cleanup()
			report, err := Scan(caseRoot)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if err := report.Err(); err == nil {
				t.Fatalf("%s produced no refusal at all", d.Name)
			}
			if matchingViolation(report.Violations, d) == nil {
				t.Fatalf("%s did not produce a %s refusal naming %s: %v", d.Name, d.Clause.ID, d.File, report.Violations)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func category(t *testing.T, r *Report, name string) Category {
	t.Helper()
	for _, c := range r.Categories {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no category %q in %v", name, r.Categories)
	return Category{}
}

func violationFiles(r *Report) map[string]bool {
	out := map[string]bool{}
	for _, v := range r.Violations {
		out[v.File] = true
	}
	return out
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

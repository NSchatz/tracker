package pingate

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// the Gradle wrapper distribution (P1, P6)
// ---------------------------------------------------------------------------

var (
	// A Gradle distribution URL naming a released version: gradle-8.9-bin.zip, gradle-8.9-all.zip,
	// gradle-8.10-rc-1-bin.zip.
	reGradleDistribution = regexp.MustCompile(`gradle-(\d+(?:\.\d+)*(?:-[A-Za-z0-9.\-]+)?)-(bin|all)\.zip`)

	// The checksum the wrapper verifies the downloaded archive against.
	reSha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func isGradleWrapperProperties(rel string) bool {
	return path.Base(rel) == "gradle-wrapper.properties"
}

// scanGradleWrapper is the one place in this repository where a build DOWNLOADS an executable
// artefact by URL. `validateDistributionUrl=true` checks only that the URL is well formed; it says
// nothing about the bytes that come back, so without `distributionSha256Sum` the wrapper unpacks
// and runs whatever the network handed it. P1's "the digest is what actually resolves" and P6's
// "a build that cannot get exactly what it pinned stops" are the same clause said about a zip.
func scanGradleWrapper(root string, r *Report) error {
	files, err := walkFiles(root, isGradleWrapperProperties)
	if err != nil {
		return err
	}

	examined := 0
	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		url, urlLine, sum := "", 0, ""
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
				continue
			}
			key, value, found := strings.Cut(trimmed, "=")
			if !found {
				continue
			}
			// .properties escapes the colon in a URL as `\:`.
			value = strings.ReplaceAll(strings.TrimSpace(value), `\:`, ":")
			switch strings.TrimSpace(key) {
			case "distributionUrl":
				url, urlLine = value, i+1
			case "distributionSha256Sum":
				sum = value
			}
		}
		if url == "" {
			continue
		}
		examined++

		v := Violation{File: rel, Line: urlLine, Reference: url}
		switch {
		case !reGradleDistribution.MatchString(url):
			v.Clause, v.Why = P6, "this distribution URL does not name an exact released Gradle version, so the build runs whichever Gradle the URL points at on the day it runs"
		case sum == "":
			v.Clause, v.Why = P6, "there is no distributionSha256Sum beside it, so the wrapper downloads and RUNS this archive without ever checking the bytes - P1 pins an image by the digest that actually resolves, and an executable zip fetched over the network is owed exactly the same; add `distributionSha256Sum=<64 hex>` from "+url+".sha256"
		case !reSha256Hex.MatchString(sum):
			v.Reference = "distributionSha256Sum=" + sum
			v.Clause, v.Why = P6, "a distribution checksum must be 64 lowercase hexadecimal characters, or the wrapper has nothing it can compare the download against"
		default:
			continue
		}
		r.Violations = append(r.Violations, v)
	}

	r.Categories = append(r.Categories, Category{
		Name:     "gradle wrapper distributions",
		Examined: examined,
		Detail:   fmt.Sprintf("%d gradle-wrapper.properties with a distributionUrl, each checked for a committed distributionSha256Sum", examined),
	})
	return nil
}

// ---------------------------------------------------------------------------
// the Gradle version catalog (P4, P6)
// ---------------------------------------------------------------------------

var (
	// A `[section]` header in the TOML catalog.
	reTomlSection = regexp.MustCompile(`^\s*\[([A-Za-z0-9_\-]+)\]\s*$`)

	// `agp = "8.5.2"` in [versions].
	reTomlVersionEntry = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=\s*"([^"]*)"\s*$`)

	// An inline `version = "..."` in a [libraries] or [plugins] entry. `version.ref = "coreKtx"` is
	// NOT this: it points at [versions], which is where a version belongs.
	reTomlInlineVersion = regexp.MustCompile(`(?:^|[,{\s])version\s*=\s*"([^"]*)"`)

	// A `module = "group:artifact:1.2.3"` entry carrying the version inline.
	reTomlModuleCoordinate = regexp.MustCompile(`module\s*=\s*"[^":]+:[^":]+:([^"]+)"`)

	// An exact Gradle/Maven version: 8.5.2, 1.9.24, 2024.06.00, 21.3.0, 1.0.0-alpha01.
	reExactMavenVersion = regexp.MustCompile(`^\d+(\.\d+)*(-[0-9A-Za-z.\-]+)?$`)
)

func isVersionCatalog(rel string) bool {
	return path.Base(rel) == "libs.versions.toml"
}

func scanGradleCatalogs(root string, r *Report) error {
	files, err := walkFiles(root, isVersionCatalog)
	if err != nil {
		return err
	}

	examined := 0
	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		section := ""
		for i, line := range lines {
			if idx := strings.Index(line, "#"); idx >= 0 {
				line = line[:idx]
			}
			if m := reTomlSection.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}

			var name, version string
			switch {
			case section == "versions":
				m := reTomlVersionEntry.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				name, version = m[1], m[2]
			default:
				if m := reTomlInlineVersion.FindStringSubmatch(line); m != nil {
					name, version = strings.TrimSpace(strings.SplitN(line, "=", 2)[0]), m[1]
				} else if m := reTomlModuleCoordinate.FindStringSubmatch(line); m != nil {
					name, version = strings.TrimSpace(strings.SplitN(line, "=", 2)[0]), m[1]
				} else {
					continue
				}
			}

			examined++
			if reason := dynamicVersionReason(version); reason != "" {
				r.Violations = append(r.Violations, Violation{
					File: rel, Line: i + 1, Reference: name + " = \"" + version + "\"", Clause: P4,
					Why: reason,
				})
			}
		}
	}

	r.Categories = append(r.Categories, Category{
		Name:     "gradle version catalog entries",
		Examined: examined,
		Detail:   fmt.Sprintf("%d version literal(s) across %d catalog(s), each required to be exact", examined, len(files)),
	})
	return nil
}

// dynamicVersionReason names why a Gradle/Maven version is dynamic, or returns "" when it is exact.
func dynamicVersionReason(v string) string {
	s := strings.TrimSpace(v)
	upper := strings.ToUpper(s)
	switch {
	case s == "":
		return "an empty version resolves to whatever the repository holds at build time"
	case strings.Contains(s, "+"):
		return "a `+` is Gradle's dynamic version: `" + s + "` resolves to whatever the repository holds when the build runs, which is a different dependency tomorrow"
	case upper == "LATEST" || upper == "RELEASE" || strings.HasPrefix(upper, "LATEST."):
		return "`" + s + "` is a Gradle status keyword, not a version: it resolves to the newest artefact the repository is serving"
	case strings.ContainsAny(s, "[]()") || strings.Contains(s, ","):
		return "`" + s + "` is a Maven version RANGE, so the resolved dependency is chosen by the repository and not by this file"
	case strings.Contains(upper, "SNAPSHOT"):
		return "a -SNAPSHOT is republished under the same coordinate, so `" + s + "` names different bytes on different days (P5: pin to something the publisher keeps)"
	case !reExactMavenVersion.MatchString(s):
		return "`" + s + "` is not an exact version literal"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Gradle build scripts (P4, P6)
// ---------------------------------------------------------------------------

var (
	// A Maven coordinate written out in a build script with its version inline. The version group
	// must start with a digit, which keeps ordinary two-part strings ("com.android.application")
	// and non-coordinate text out of it.
	reScriptCoordinate = regexp.MustCompile(`"([A-Za-z0-9_.\-]+):([A-Za-z0-9_.\-]+):(\d[^"]*)"`)

	// `id("org.example.plugin") version "1.2.3"` in a plugins block.
	reScriptPluginVersion = regexp.MustCompile(`id\s*\(\s*"([^"]+)"\s*\)\s+version\s+"([^"]+)"`)
)

func isGradleScript(rel string) bool {
	name := path.Base(rel)
	return strings.HasSuffix(name, ".gradle") || strings.HasSuffix(name, ".gradle.kts")
}

// scanGradleScripts keeps the version catalog the SINGLE source of truth. A version literal written
// into a build script is a second place the pin lives, and two places is how one of them silently
// stops being the one that is read.
func scanGradleScripts(root string, r *Report) error {
	files, err := walkFiles(root, isGradleScript)
	if err != nil {
		return err
	}

	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		for i, line := range lines {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if m := reScriptCoordinate.FindStringSubmatch(code); m != nil {
				r.Violations = append(r.Violations, Violation{
					File: rel, Line: i + 1, Reference: m[0], Clause: P4,
					Why: "a dependency version written into a build script is a second place this pin lives; move it to the version catalog (gradle/libs.versions.toml) and reference it by alias",
				})
				continue
			}
			if m := reScriptPluginVersion.FindStringSubmatch(code); m != nil {
				r.Violations = append(r.Violations, Violation{
					File: rel, Line: i + 1, Reference: m[0], Clause: P4,
					Why: "a plugin version written into a build script is a second place this pin lives; declare the plugin in the version catalog's [plugins] section and apply it by alias",
				})
			}
		}
	}

	r.Categories = append(r.Categories, Category{
		Name:     "gradle build scripts",
		Examined: len(files),
		Detail:   fmt.Sprintf("%d build script(s) checked for version literals restated outside the catalog", len(files)),
	})
	return nil
}

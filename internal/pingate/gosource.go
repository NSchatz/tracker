package pingate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// container image references named in Go source (P1, P6)
// ---------------------------------------------------------------------------
//
// A Dockerfile and a compose file are not the only places this repository names an image it
// PULLS. internal/testsupport starts a real PostGIS with testcontainers on every `make test`, and
// the reference it starts is a Go string constant. That reference is the instrument tracker's
// spatial verdict is measured with, so P1 reaches it exactly as it reaches `docker-compose.yml` -
// and a gate that pins what the stack RUNS while leaving what the tests MEASURE on a floating tag
// lets those two silently become different databases.
//
// # What counts as an image reference here, and what deliberately does not
//
// The unit examined is a WHOLE string literal: the entire literal has to be a reference, so
// `"postgis/postgis:16-3.4"` is examined and `"FROM golang:1.26.8-bookworm AS build\n"` is not.
// That boundary is what lets this package's own test tables, and internal/toolchain's, hold
// deliberately unpinned Dockerfile TEXT without the gate refusing its own fixtures - the strings
// there are inputs to a scanner, never things anything pulls. It also means the rule needs no
// excluded directory at all, which is the better trade: an exclusion is a hole that has to be
// nailed shut from the other side, and this rule has none to nail.
//
// A whole literal is only examined when something says it is an image rather than a colon-joined
// pair of words: it names a repository PATH (`postgis/postgis:16-3.4`), or it already carries an
// `@sha256:` digest, or the identifier it is bound to says image (`PostGISImage`). Without that,
// `secretscan:allow` - a marker string with no compliant form - would be refused as an unpinned
// image, which is the `runs-on: ubuntu-latest` mistake spelled differently.
//
// # The evasion this closes from the other side
//
// A literal rule can be walked around by assembling the reference at run time. So a file that
// imports testcontainers AND calls one of its container-starting entry points must carry an image
// reference this gate can read: replace the constant with `os.Getenv("IMAGE")` and the refusal is
// immediate, naming the file.

var (
	// One component of an image name: lowercase alphanumerics with single separators, which is the
	// distribution grammar's `path-component`.
	goImageNameComponent = `[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*`

	// The whole reference: NAME[:TAG][@sha256:...]. The digest half is matched LOOSELY on purpose -
	// an uppercase or truncated digest must be examined and then refused by reImageRef, not skipped
	// as "not an image reference at all".
	reGoImageCandidate = regexp.MustCompile(`^` + goImageNameComponent + `(?:/` + goImageNameComponent + `)*` +
		`(?::[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?(?:@sha256:[0-9A-Za-z]+)?$`)

	// An identifier that says its value is an image: PostGISImage, defaultImage, IMAGE.
	reImageIdent = regexp.MustCompile(`(?i)image`)

	// The testcontainers entry points that actually start a container. A file that imports
	// testcontainers and calls one of these is running an image, whatever it names it.
	containerStarters = map[string]bool{"Run": true, "RunContainer": true, "GenericContainer": true}
)

func isGoSource(rel string) bool { return strings.HasSuffix(rel, ".go") }

func scanGoSources(root string, r *Report) error {
	files, err := walkFiles(root, isGoSource)
	if err != nil {
		return err
	}

	examined, parsed := 0, 0
	for _, rel := range files {
		data, ok, err := readFile(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), data, 0)
		if err != nil {
			// "The gate could not read it" must never round to "it is fine" - the same stance the
			// compose scanner takes on YAML it cannot parse.
			r.Violations = append(r.Violations, Violation{
				File: rel, Line: 1, Reference: rel, Clause: P6,
				Why: "this Go file does not parse, so any image reference it names could not be checked: " + err.Error(),
			})
			continue
		}
		parsed++

		n, violations := checkGoFile(rel, fset, file)
		examined += n
		r.Violations = append(r.Violations, violations...)
	}

	r.Categories = append(r.Categories, Category{
		Name:     "container image references in Go source",
		Examined: examined,
		Detail: fmt.Sprintf("%d whole-literal image reference(s) across %d parsed .go file(s); a literal is examined when it names a repository path, carries a digest, or is bound to an identifier that says image",
			examined, parsed),
	})
	return nil
}

// checkGoFile returns how many image references the file names and the refusals they earn.
func checkGoFile(rel string, fset *token.FileSet, file *ast.File) (int, []Violation) {
	names := literalNames(file)

	examined := 0
	var out []Violation

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if !isGoImageReference(value, names[lit]) {
			return true
		}
		examined++
		if reImageRef.MatchString(value) {
			return true
		}
		out = append(out, Violation{
			File: rel, Line: fset.Position(lit.Pos()).Line, Reference: value, Clause: P1,
			Why: "this string is a container image reference that something here PULLS, so it takes both halves exactly as a compose `image:` does: the tag stays readable and the digest is what actually resolves, and a tag alone is moved by its publisher under every run; write " + withDigestHint(value),
		})
		return true
	})

	if line, starts := startsAContainer(fset, file); starts && examined == 0 {
		out = append(out, Violation{
			File: rel, Line: line, Reference: "testcontainers", Clause: P1,
			Why: "this file starts a container with testcontainers but names no image reference this gate can read, so what it runs is decided somewhere the pin check cannot see; keep the reference in this file as a single string literal pinned NAME:TAG@sha256:<64 hex>",
		})
	}

	return examined, out
}

// isGoImageReference decides whether a whole string literal is an image reference this gate owes a
// verdict on. ident is the identifier the literal is bound to, or "" when it is bound to none.
func isGoImageReference(value, ident string) bool {
	if !reGoImageCandidate.MatchString(value) {
		return false
	}
	// A bare name with neither tag nor digest is not distinguishable from an import path or a
	// directory; there is nothing here to have pinned.
	if !strings.ContainsAny(value, ":@") {
		return false
	}
	return strings.Contains(value, "/") ||
		strings.Contains(value, "@sha256:") ||
		reImageIdent.MatchString(ident)
}

// literalNames maps a string literal to the identifier it is bound to, which is the third way a
// literal announces itself as an image (`const PostGISImage = "..."`).
func literalNames(file *ast.File) map[*ast.BasicLit]string {
	names := map[*ast.BasicLit]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.ValueSpec:
			for i, v := range d.Values {
				if lit, ok := v.(*ast.BasicLit); ok && i < len(d.Names) {
					names[lit] = d.Names[i].Name
				}
			}
		case *ast.AssignStmt:
			for i, v := range d.Rhs {
				if lit, ok := v.(*ast.BasicLit); ok && i < len(d.Lhs) {
					names[lit] = exprName(d.Lhs[i])
				}
			}
		case *ast.KeyValueExpr:
			if lit, ok := d.Value.(*ast.BasicLit); ok {
				names[lit] = exprName(d.Key)
			}
		}
		return true
	})
	return names
}

func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if v, err := strconv.Unquote(x.Value); err == nil {
				return v
			}
		}
	}
	return ""
}

// startsAContainer reports whether this file both imports testcontainers and calls one of its
// container-starting entry points, and the line of that call.
func startsAContainer(fset *token.FileSet, file *ast.File) (int, bool) {
	imports := false
	for _, spec := range file.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil && strings.Contains(p, "testcontainers") {
			imports = true
			break
		}
	}
	if !imports {
		return 0, false
	}

	line := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || line != 0 {
			return line == 0
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && containerStarters[sel.Sel.Name] {
			line = fset.Position(call.Pos()).Line
		}
		return true
	})
	return line, line != 0
}

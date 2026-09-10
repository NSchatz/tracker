package prosegate

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"regexp"
	"strings"
)

// Count is one file's measurement.
type Count struct {
	Prose int
	Code  int
}

// Counted is the denominator: prose lines plus code lines. Blank lines and the
// lines of a leading licence notice are in neither.
func (c Count) Counted() int { return c.Prose + c.Code }

// Above reports whether the ratio exceeds a whole-percentage-point threshold,
// in integer arithmetic.
func (c Count) Above(pct int) bool { return c.Prose*100 > pct*c.Counted() }

// MajorityProse is the semantic floor: prose lines reaching code lines is a
// file with more narration than code, which is the defect itself rather than a
// tuned number.
func (c Count) MajorityProse() bool { return c.Prose >= c.Code }

var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// licenceWords are matched case-insensitively against a file's leading comment
// block. Each is unambiguous; "license" alone would catch a package doc that
// merely discusses one.
var licenceWords = []string{
	"copyright",
	"spdx-license-identifier",
	"licensed under",
	"all rights reserved",
}

// CountSource measures one Go source file. The second result reports the
// generated-file marker: those comments are not a human's to trim, so such a
// file leaves the measurement entirely rather than scoring zero in it.
func CountSource(filename string, src []byte) (Count, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return Count{}, false, err
	}
	if ast.IsGenerated(file) {
		return Count{}, true, nil
	}

	code := tokenLines(filename, src)
	skip := licenceLines(fset, file)

	prose := map[int]bool{}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if isDirectiveComment(c.Text) {
				continue
			}
			from := rawLine(fset, c.Pos())
			to := rawLine(fset, c.End())
			for n := from; n <= to; n++ {
				prose[n] = true
			}
		}
	}

	var out Count
	for i, line := range strings.Split(string(src), "\n") {
		n := i + 1
		if skip[n] || strings.TrimSpace(line) == "" {
			continue
		}
		if prose[n] && !code[n] {
			out.Prose++
			continue
		}
		out.Code++
	}
	return out, false, nil
}

// tokenLines are the lines carrying a token that is not a comment.
//
// It is what keeps a trailing note from deleting a line of code out of the
// denominator: `x := 1 // metres` is a line of code whatever is appended to it.
// The interior lines of a raw string carry no token start, so they fall through
// to the code count below rather than to prose, which is the second half of the
// failure this counter exists to avoid.
func tokenLines(filename string, src []byte) map[int]bool {
	fset := token.NewFileSet()
	file := fset.AddFile(filename, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)

	lines := map[int]bool{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return lines
		}
		// An automatic semicolon is reported at the position of the token or
		// comment that provoked it, never on a line of its own.
		if tok == token.COMMENT || (tok == token.SEMICOLON && lit == "\n") {
			continue
		}
		lines[file.PositionFor(pos, false).Line] = true
	}
}

// isDirectiveComment reports whether a comment is a compiler or tooling
// directive. Deleting one changes what compiles, what is generated or what a
// linter does, so it counts as code and no run of this gate can demand it.
func isDirectiveComment(text string) bool {
	if constraint.IsGoBuild(text) || constraint.IsPlusBuild(text) || generatedMarker.MatchString(text) {
		return true
	}
	if strings.HasPrefix(text, "/*line ") || strings.HasPrefix(text, "/*line\t") {
		return true
	}
	if !strings.HasPrefix(text, "//") {
		return false
	}
	body := text[2:]
	// A linter directive written without an argument carries no colon and so is
	// not a tool directive by the rule below.
	if body == "nolint" || strings.HasPrefix(body, "nolint ") {
		return true
	}
	return isToolDirective(body)
}

// isToolDirective mirrors the rule go/ast applies to a comment body and does not
// export: //line, //extern, //export, and //name:argument where name is lower
// alphanumeric and the argument does not start with a space.
func isToolDirective(body string) bool {
	if strings.HasPrefix(body, "line ") || strings.HasPrefix(body, "extern ") || strings.HasPrefix(body, "export ") {
		return true
	}
	colon := strings.Index(body, ":")
	if colon <= 0 || colon+1 >= len(body) {
		return false
	}
	for i := 0; i <= colon+1; i++ {
		if i == colon {
			continue
		}
		b := body[i]
		if !('a' <= b && b <= 'z' || '0' <= b && b <= '9') {
			return false
		}
	}
	return true
}

// licenceLines are the lines of a leading licence or copyright notice, which is
// excluded from both counts: it is prose by any reading and it is not
// deletable, so counting it either way distorts the ratio.
func licenceLines(fset *token.FileSet, file *ast.File) map[int]bool {
	skip := map[int]bool{}
	if len(file.Comments) == 0 {
		return skip
	}
	first := file.Comments[0]
	if first.Pos() > file.Package {
		return skip
	}
	lower := strings.ToLower(first.Text())
	notice := false
	for _, w := range licenceWords {
		if strings.Contains(lower, w) {
			notice = true
			break
		}
	}
	if !notice {
		return skip
	}
	for n := rawLine(fset, first.Pos()); n <= rawLine(fset, first.End()); n++ {
		skip[n] = true
	}
	return skip
}

// rawLine is the line a position occupies IN THE FILE. The adjusted position a
// file set reports by default is the one a `//line` directive rewrote it to, so
// asking for it would have a single directive renumber every count below it
// into a file that does not exist.
func rawLine(fset *token.FileSet, pos token.Pos) int {
	return fset.PositionFor(pos, false).Line
}

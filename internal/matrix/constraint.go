package matrix

import (
	"bytes"
	"errors"
	"fmt"
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"
)

// The comment openings the header scan recognises.
const (
	lineComment       = "//"
	blockCommentOpen  = "/*"
	blockCommentClose = "*/"
)

// testStem is the file-name field that names a test file, which carries no
// platform constraint of its own.
const testStem = "test"

// errMultipleConstraints reports a file declaring more than one build
// expression, which the toolchain refuses rather than resolves.
var errMultipleConstraints = errors.New("matrix: file declares more than one build expression")

// fileConstraint is one file's build constraint, held as the toolchain reads it:
// the constraint the file's name implies and the expression the file declares.
type fileConstraint struct {
	name constraint.Expr // what the name implies, nil where it implies nothing
	decl constraint.Expr // what the file declares, nil where it declares nothing
	path string          // target-relative, forward slashes
}

// expr returns the one expression the file's two halves make, and nil for a file
// no build constraint decides.
func (f fileConstraint) expr() constraint.Expr {
	switch {
	case f.name == nil:
		return f.decl
	case f.decl == nil:
		return f.name
	default:
		return &constraint.AndExpr{X: f.name, Y: f.decl}
	}
}

// collectTags records every tag one expression names, negated and alternative
// branches included, so an atom counts wherever it appears rather than only
// where it decides the expression.
func collectTags(expression constraint.Expr, into map[string]bool) {
	switch e := expression.(type) {
	case *constraint.TagExpr:
		into[e.Tag] = true
	case *constraint.NotExpr:
		collectTags(e.X, into)
	case *constraint.AndExpr:
		collectTags(e.X, into)
		collectTags(e.Y, into)
	case *constraint.OrExpr:
		collectTags(e.X, into)
		collectTags(e.Y, into)
	}
}

// readConstraint reads the build constraint of the Go file at path, which sits
// under root and is recorded relative to it.
func readConstraint(root, path string) (fileConstraint, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return fileConstraint{}, err
	}
	relative := relativeToSlash(root, path)
	declared, err := declaredConstraint(content)
	if err != nil {
		return fileConstraint{}, fmt.Errorf("%s: %w", relative, err)
	}
	return fileConstraint{
		path: relative,
		name: nameConstraint(filepath.Base(path)),
		decl: declared,
	}, nil
}

// relativeToSlash renders path relative to base with forward slashes, falling
// back to path itself where no relative form exists.
func relativeToSlash(base, path string) string {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// nameConstraint returns the constraint a file's name implies, and nil for a
// name that implies none.
//
// The name is read up to its first full stop, and everything before its first
// underscore is dropped, so a file whose whole name is a platform name carries
// no constraint. The last two fields that remain name an operating system and an
// architecture, or the last field alone names one of the two; a trailing test
// field belongs to the test-file convention and is dropped first.
func nameConstraint(base string) constraint.Expr {
	stem, _, _ := strings.Cut(base, ".")
	_, suffixed, ok := strings.Cut(stem, "_")
	if !ok {
		return nil
	}
	fields := strings.Split(suffixed, "_")
	if last := len(fields) - 1; fields[last] == testStem {
		fields = fields[:last]
	}
	switch last := len(fields) - 1; {
	case last >= 1 && isOS(fields[last-1]) && isArch(fields[last]):
		return &constraint.AndExpr{
			X: &constraint.TagExpr{Tag: fields[last-1]},
			Y: &constraint.TagExpr{Tag: fields[last]},
		}
	case last >= 0 && (isOS(fields[last]) || isArch(fields[last])):
		return &constraint.TagExpr{Tag: fields[last]}
	default:
		return nil
	}
}

// declaredConstraint returns the constraint the file's content declares, and nil
// for content that declares none.
//
// A //go:build expression controls where one is present; otherwise the legacy
// lines the header holds are combined, each of them a further condition the file
// must satisfy. A legacy line that does not parse is no constraint, which is how
// the toolchain reads one, and a second //go:build expression is an error.
func declaredConstraint(content []byte) (constraint.Expr, error) {
	goBuild, legacy, err := scanHeader(content)
	if err != nil {
		return nil, err
	}
	if goBuild != "" {
		expression, err := constraint.Parse(goBuild)
		if err != nil {
			return nil, err
		}
		return expression, nil
	}

	var combined constraint.Expr
	for _, line := range legacy {
		expression, err := constraint.Parse(line)
		if err != nil {
			continue
		}
		if combined == nil {
			combined = expression
			continue
		}
		combined = &constraint.AndExpr{X: combined, Y: expression}
	}
	return combined, nil
}

// scanHeader reads the leading run of comment and blank lines that a build
// constraint may appear in, and returns the //go:build line it holds and the
// legacy lines that count.
//
// The run ends at the first line carrying anything but a comment. A //go:build
// line counts anywhere in that run as long as no block comment encloses it. A
// legacy line counts only where a blank line separates it from the code below,
// which is the rule that keeps a package's doc comment from constraining it, so
// the lines seen since the last blank line are dropped at the end.
func scanHeader(content []byte) (goBuild string, legacy []string, err error) {
	var header headerScan
	for raw := range bytes.Lines(content) {
		ended, err := header.read(strings.TrimSpace(string(raw)))
		if err != nil {
			return "", nil, err
		}
		if ended {
			break
		}
	}
	return header.goBuild, header.legacy, nil
}

// headerScan is one walk's position in a file's leading comment run.
type headerScan struct {
	goBuild  string   // the //go:build line the run holds
	legacy   []string // the legacy lines a blank line separated from the code
	pending  []string // the legacy lines seen since the last blank line
	coded    bool     // a line has carried something other than a comment
	enclosed bool     // a block comment is open
}

// read takes one trimmed line of the file and reports whether the run ends with
// it.
func (h *headerScan) read(line string) (ended bool, err error) {
	if line == "" && !h.coded {
		h.legacy = append(h.legacy, h.pending...)
		h.pending = h.pending[:0]
		return false, nil
	}
	if !strings.HasPrefix(line, lineComment) {
		h.coded = true
	}
	if !h.enclosed {
		if err := h.declare(line); err != nil {
			return true, err
		}
	}
	code, enclosed := scanComments(line, h.enclosed)
	h.enclosed = enclosed
	return code, nil
}

// declare records the constraint one line of the run declares.
func (h *headerScan) declare(line string) error {
	if constraint.IsGoBuild(line) {
		if h.goBuild != "" {
			return errMultipleConstraints
		}
		h.goBuild = line
	}
	if constraint.IsPlusBuild(line) {
		h.pending = append(h.pending, line)
	}
	return nil
}

// scanComments walks one line's comment structure, reporting whether the line
// carries anything but a comment and whether a block comment is open at its end.
func scanComments(line string, enclosed bool) (code, inComment bool) {
	for line != "" {
		if enclosed {
			_, rest, closed := strings.Cut(line, blockCommentClose)
			if !closed {
				return false, true
			}
			enclosed = false
			line = strings.TrimSpace(rest)
			continue
		}
		if strings.HasPrefix(line, lineComment) {
			return false, false
		}
		rest, opens := strings.CutPrefix(line, blockCommentOpen)
		if !opens {
			return true, false
		}
		enclosed = true
		line = strings.TrimSpace(rest)
	}
	return false, enclosed
}

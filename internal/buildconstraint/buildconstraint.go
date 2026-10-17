// Package buildconstraint reads a Go file's build constraint the way the
// toolchain reads it, and answers the questions the analysis asks of one: which
// tags it names, whether it holds only by hand, and which names the toolchain
// reserves for a platform.
package buildconstraint

import (
	"bytes"
	"errors"
	"go/build/constraint"
	"maps"
	"slices"
	"strings"
)

// IgnoreTag is the toolchain's conventional tag for a file kept out of ordinary
// package builds and run by naming it, which no configuration sets.
const IgnoreTag = "ignore"

// Compiler names the compiler whose type sizes and tag a load reads, which is the
// one the load's own toolchain is.
const Compiler = "gc"

// The comment openings the header scan recognises.
const (
	lineComment       = "//"
	blockCommentOpen  = "/*"
	blockCommentClose = "*/"
)

// byteOrderMark is the UTF-8 encoding of the byte-order mark a Go file may open
// with.
const byteOrderMark = "\uFEFF"

// testStem is the file-name field that names a test file, which carries no
// platform constraint of its own.
const testStem = "test"

// goSuffix is the extension of a Go source file.
const goSuffix = ".go"

// maxFreeTags bounds the settings [HoldsWith] enumerates. An expression naming
// more other tags is answered as holding.
const maxFreeTags = 16

// ErrMultipleConstraints reports a file declaring more than one build
// expression, which the toolchain refuses rather than resolves.
var ErrMultipleConstraints = errors.New("buildconstraint: file declares more than one build expression")

// Of is the one build constraint the Go file named name with that content
// carries: what its name implies and what its header declares, joined, a
// condition both halves state side by side appearing once, so the result is the
// constraint the file spells. It is nil for a file no constraint decides.
func Of(name string, content []byte) (constraint.Expr, error) {
	declared, err := declaredConstraint(content)
	if err != nil {
		return nil, err
	}
	implied := nameConstraint(name)
	switch {
	case implied == nil:
		return declared, nil
	case declared == nil:
		return implied, nil
	default:
		return conjunction(append(conditions(implied), conditions(declared)...)), nil
	}
}

// Tags is every tag one expression names, negated and alternative branches
// included, so a tag counts wherever it appears rather than only where it decides
// the expression.
func Tags(expression constraint.Expr) map[string]bool {
	named := make(map[string]bool)
	var walk func(constraint.Expr)
	walk = func(e constraint.Expr) {
		switch e := e.(type) {
		case *constraint.TagExpr:
			named[e.Tag] = true
		case *constraint.NotExpr:
			walk(e.X)
		case *constraint.AndExpr:
			walk(e.X)
			walk(e.Y)
		case *constraint.OrExpr:
			walk(e.X)
			walk(e.Y)
		}
	}
	walk(expression)
	return named
}

// HoldsWith reports whether some setting of the other tags expression names makes
// it true with tag set to value. Each other tag is read as free, platform tags
// included.
func HoldsWith(expression constraint.Expr, tag string, value bool) bool {
	named := Tags(expression)
	delete(named, tag)
	if len(named) > maxFreeTags {
		return true
	}
	free := slices.Sorted(maps.Keys(named))
	for setting := range 1 << len(free) {
		if expression.Eval(func(name string) bool {
			if name == tag {
				return value
			}
			at := slices.Index(free, name)
			return at >= 0 && setting&(1<<at) != 0
		}) {
			return true
		}
	}
	return false
}

// OnlyUnderIgnore reports whether expression holds only when the ignore tag is
// set, under any platform: a file it constrains is outside the analysis. Whether
// one configuration runs such a file as a program is the configuration's own
// question, which its build context answers.
func OnlyUnderIgnore(expression constraint.Expr) bool {
	return expression != nil && Tags(expression)[IgnoreTag] && !HoldsWith(expression, IgnoreTag, false)
}

// SkipsDir reports whether the toolchain builds nothing below a directory of that
// name: one it reserves for test inputs or vendored sources, or one whose name
// opens with a character that keeps every path under it out of a package.
func SkipsDir(name string) bool {
	return name == "" || name[0] == '.' || name[0] == '_' || name == "testdata" || name == "vendor"
}

// IsGoFile reports whether a file of that name is a Go source file the toolchain
// reads. A name opening with an underscore or a full stop is one it ignores.
func IsGoFile(name string) bool {
	stem, ok := strings.CutSuffix(name, goSuffix)
	return ok && stem != "" && stem[0] != '_' && stem[0] != '.'
}

// nameConstraint is the constraint a file's name implies, and nil for a name that
// implies none.
//
// The name is read up to its first full stop and everything before its first
// underscore is dropped, so a file whose whole name is a platform name carries no
// constraint. The last two fields that remain name an operating system and an
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
	case last >= 1 && IsOS(fields[last-1]) && IsArch(fields[last]):
		return &constraint.AndExpr{
			X: &constraint.TagExpr{Tag: fields[last-1]},
			Y: &constraint.TagExpr{Tag: fields[last]},
		}
	case last >= 0 && (IsOS(fields[last]) || IsArch(fields[last])):
		return &constraint.TagExpr{Tag: fields[last]}
	default:
		return nil
	}
}

// declaredConstraint is the constraint the file's content declares, and nil for
// content that declares none.
//
// A //go:build expression controls where one is present; otherwise the legacy
// lines the header holds are combined, each a further condition the file must
// satisfy. A legacy line that does not parse is no constraint, which is how the
// toolchain reads one.
func declaredConstraint(content []byte) (constraint.Expr, error) {
	goBuild, legacy, err := scanHeader(content)
	if err != nil {
		return nil, err
	}
	if goBuild != "" {
		return constraint.Parse(goBuild)
	}
	var combined constraint.Expr
	for _, line := range legacy {
		if expression, err := constraint.Parse(line); err == nil {
			combined = and(combined, expression)
		}
	}
	return combined, nil
}

// conditions is the conditions one expression states side by side, which is the
// expression itself where it states one.
func conditions(expression constraint.Expr) []constraint.Expr {
	joined, ok := expression.(*constraint.AndExpr)
	if !ok {
		return []constraint.Expr{expression}
	}
	return append(conditions(joined.X), conditions(joined.Y)...)
}

// conjunction joins conditions into one expression, keeping the first of any that
// read the same and their order.
func conjunction(stated []constraint.Expr) constraint.Expr {
	var joined constraint.Expr
	held := make(map[string]bool, len(stated))
	for _, condition := range stated {
		if !held[condition.String()] {
			held[condition.String()] = true
			joined = and(joined, condition)
		}
	}
	return joined
}

// and joins two conditions, and answers the second alone where the first is
// absent.
func and(held, next constraint.Expr) constraint.Expr {
	if held == nil {
		return next
	}
	return &constraint.AndExpr{X: held, Y: next}
}

// scanHeader reads the leading run of comment and blank lines that a build
// constraint may appear in, and returns the //go:build line it holds and the
// legacy lines that count.
//
// The run ends at the first line carrying anything but a comment. A //go:build
// line counts anywhere in that run as long as no block comment encloses it. A
// legacy line counts only where a blank line separates it from the code below,
// which is the rule that keeps a package's doc comment from constraining it.
func scanHeader(content []byte) (goBuild string, legacy []string, err error) {
	var header headerScan
	// The toolchain skips one leading byte-order mark before it reads the header.
	for raw := range bytes.Lines(bytes.TrimPrefix(content, []byte(byteOrderMark))) {
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
			return ErrMultipleConstraints
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

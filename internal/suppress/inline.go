package suppress

import (
	"cmp"
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// Configuration is one build configuration's load, with the declarations
// enumerated from it and the resolver over them.
type Configuration struct {
	Result  *load.Result
	Resolve *resolve.Resolver
	Symbols []inventory.Symbol
}

// Inline reads every inline directive the configurations compile. A directive is
// one record per code and per declaration any configuration placed it on, or one
// per code bound to nothing where none did; one lacking only its reason is one
// [core.Refusal] per code, and any other comment in the namespace ends the read
// with a [core.MalformedError], the first in reading order: by rendered
// position, then by written byte. It reads count configurations, each through at
// once it is done with the one before.
func Inline(count int, at func(config int) (*Configuration, error)) ([]core.Record, []core.Refusal, error) {
	read := map[writtenAt]*directive{}
	for i := range count {
		one, err := at(i)
		if err != nil {
			return nil, nil, err
		}
		if err := placeAll(one, read); err != nil {
			return nil, nil, err
		}
	}
	directives := make([]*directive, 0, len(read))
	for _, d := range read {
		directives = append(directives, d)
	}
	slices.SortFunc(directives, func(a, b *directive) int {
		return cmp.Or(inventory.ByPosition(a.at, b.at), scope.Order(a.written.file, b.written.file), cmp.Compare(a.written.offset, b.written.offset))
	})
	var records []core.Record
	var refusals []core.Refusal
	for _, d := range directives {
		switch d.read.Verdict {
		case core.WellFormed:
			records = append(records, d.records()...)
		case core.Reasonless:
			refusals = append(refusals, d.refusals()...)
		case core.Malformed:
			return nil, nil, core.MalformedDirective(d.text, d.site())
		}
	}
	return records, refusals, nil
}

// writtenAt is where a comment is written, which every configuration compiling
// its file agrees on whatever line directive renders it.
type writtenAt struct {
	file   string
	offset int
}

// directive is one comment in the namespace: what the grammar made of it, where
// it renders, the written line it binds, and every declaration a configuration
// placed it on.
type directive struct {
	read    core.Directive
	bound   []inventory.Symbol
	text    string
	written writtenAt
	target  line
	at      token.Position
}

// site is where the directive renders and where it is written, as a record
// carries it.
func (d *directive) site() core.Site {
	return core.Site{
		Path: d.at.Filename, Line: d.at.Line, Column: d.at.Column,
		Written: d.written.file + "@" + strconv.Itoa(d.written.offset),
		File:    d.target.file,
	}
}

// place adds the declarations one configuration holds on the line the directive
// targets, each once.
func (d *directive) place(below map[line][]inventory.Symbol) {
	held := below[d.target]
	for i := range held {
		if !slices.ContainsFunc(d.bound, func(placed inventory.Symbol) bool { return placed.ID == held[i].ID }) {
			d.bound = append(d.bound, held[i])
		}
	}
}

// records is one record per code and per declaration the directive bound.
func (d *directive) records() []core.Record {
	records := make([]core.Record, 0, len(d.read.Codes)*max(len(d.bound), 1))
	for _, code := range d.read.Codes {
		record := core.Record{Code: code, Path: d.at.Filename, Reason: d.read.Reason, Site: d.site(), Mechanism: core.Inline}
		if len(d.bound) == 0 {
			records = append(records, record)
			continue
		}
		for i := range d.bound {
			record.Symbol, record.Bound = d.bound[i].Ref, string(d.bound[i].ID)
			records = append(records, record)
		}
	}
	return records
}

// refusals is one refusal per code a directive carrying no reason names.
func (d *directive) refusals() []core.Refusal {
	refusals := make([]core.Refusal, 0, len(d.read.Codes))
	for _, code := range d.read.Codes {
		refusals = append(refusals, core.Refusal{
			Reported: vocab.SuppressionWithoutReason, Code: code, Path: d.at.Filename, Site: d.site(), Mechanism: core.Inline,
		})
	}
	return refusals
}

// placeAll reads the namespaced comments of one configuration into read and
// places each well-formed one on that configuration's declarations. One comment
// is read once whatever the number of package variants that type-check its file,
// because every variant carries the same syntax tree.
func placeAll(c *Configuration, read map[writtenAt]*directive) error {
	below := declarationLines(c.Symbols)
	signatureLines(c, below)
	fset := c.Result.Fset
	seen := map[token.Pos]bool{}
	for _, p := range c.Result.Packages {
		for _, f := range p.Syntax {
			if seen[f.FileStart] {
				continue
			}
			seen[f.FileStart] = true
			if err := placeFile(f, fset, c.Resolve, read, below); err != nil {
				return err
			}
		}
	}
	return nil
}

// placeFile reads every comment of one syntax tree in the namespace into read
// and places each well-formed one.
func placeFile(f *ast.File, fset *token.FileSet, rs *resolve.Resolver, read map[writtenAt]*directive, below map[line][]inventory.Symbol) error {
	var first map[int]token.Pos
	for _, group := range f.Comments {
		for _, comment := range group.List {
			d, err := readComment(comment, fset, rs, read, func() map[int]token.Pos {
				if first == nil {
					first = firstTokens(f, fset)
				}
				return first
			})
			if err != nil {
				return err
			}
			if d != nil && d.read.Verdict == core.WellFormed {
				d.place(below)
			}
		}
	}
	return nil
}

// readComment is the directive one comment is, read once into read, and nil for
// a comment outside the namespace. A position that does not render ends the
// read, because a comment the analysis cannot place is a directive it cannot
// bind.
func readComment(comment *ast.Comment, fset *token.FileSet, rs *resolve.Resolver, read map[writtenAt]*directive,
	first func() map[int]token.Pos,
) (*directive, error) {
	text := strings.TrimSuffix(comment.Text, "\r")
	verdict := core.ReadDirective(text)
	if verdict.Verdict == core.NotADirective {
		return nil, nil
	}
	physical := fset.PositionFor(comment.Slash, false)
	key := writtenAt{file: physical.Filename, offset: physical.Offset}
	if d := read[key]; d != nil {
		return d, nil
	}
	at, err := rs.Render(comment.Slash)
	if err != nil {
		return nil, err
	}
	target := line{file: rs.Positions().File(comment.Slash), line: physical.Line + 1}
	if leading, held := first()[physical.Line]; held && leading < comment.Slash {
		target.line = physical.Line
	}
	d := &directive{text: text, read: verdict, at: at, written: key, target: target}
	read[key] = d
	return d, nil
}

// firstTokens is, per written line of one file, the earliest position at which a
// syntax node begins or ends on it, comments left out, which is what tells a
// comment written after code on its line from one alone on it.
func firstTokens(f *ast.File, fset *token.FileSet) map[int]token.Pos {
	first := make(map[int]token.Pos)
	keep := func(pos token.Pos) {
		if !pos.IsValid() {
			return
		}
		written := fset.PositionFor(pos, false).Line
		if held, seen := first[written]; !seen || pos < held {
			first[written] = pos
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case nil, *ast.CommentGroup, *ast.Comment:
			return false
		}
		keep(n.Pos())
		keep(n.End() - 1)
		return true
	})
	return first
}

// declarationLines indexes the inventory by the line each declaration begins on:
// that of its keyword, or of its name inside a group, a struct or an interface. A
// line declaring several symbols holds each, and a line opening a group none. A
// file and a package have no first token for a directive to sit above, so they
// are left out.
func declarationLines(symbols []inventory.Symbol) map[line][]inventory.Symbol {
	below := make(map[line][]inventory.Symbol, len(symbols))
	for i := range symbols {
		s := &symbols[i]
		if s.Kind == inventory.KindFile || s.Kind == inventory.KindPackage {
			continue
		}
		at := line{file: s.File, line: s.Line}
		below[at] = append(below[at], *s)
	}
	return below
}

// line names one line of one file as the file writes it, which a line directive
// does not move: what binds a directive to the declarations written below it.
type line struct {
	file string
	line int
}

// signatureLines adds to the index every line a wrapped function signature
// declares a receiver, a parameter or a result on, below the line the function
// begins on, and binds each to the function: a part of a signature is suppressed
// through the declaration that holds it, and the directive for one on a later
// line of the signature sits above it, inside the list.
func signatureLines(c *Configuration, below map[line][]inventory.Symbol) {
	byID := make(map[inventory.SymbolID]*inventory.Symbol, len(c.Symbols))
	for i := range c.Symbols {
		byID[c.Symbols[i].ID] = &c.Symbols[i]
	}
	seen := make(map[token.Pos]bool)
	for _, p := range c.Result.Packages {
		for _, f := range p.Syntax {
			if seen[f.FileStart] {
				continue
			}
			seen[f.FileStart] = true
			signatureLinesOf(f, c.Resolve, byID, below)
		}
	}
}

// signatureLinesOf adds the wrapped signature lines of one file's functions.
func signatureLinesOf(f *ast.File, rs *resolve.Resolver, byID map[inventory.SymbolID]*inventory.Symbol,
	below map[line][]inventory.Symbol,
) {
	for _, decl := range f.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc {
			continue
		}
		id, inventoried := rs.At(fn.Name.Pos())
		function := byID[id]
		if !inventoried || function == nil {
			continue
		}
		for _, at := range fieldLines(fn, rs.Positions()) {
			if at.line == function.Line || slices.ContainsFunc(below[at], func(s inventory.Symbol) bool {
				return s.ID == function.ID
			}) {
				continue
			}
			below[at] = append(below[at], *function)
		}
	}
}

// fieldLines is the line every receiver, parameter and result of one function's
// signature begins on, in signature order.
func fieldLines(fn *ast.FuncDecl, positions *inventory.Positions) []line {
	var lines []line
	for _, list := range []*ast.FieldList{fn.Recv, fn.Type.Params, fn.Type.Results} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			lines = append(lines, line{file: positions.File(field.Pos()), line: positions.Line(field.Pos())})
		}
	}
	return lines
}

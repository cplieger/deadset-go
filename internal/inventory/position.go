package inventory

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// ErrSource reports a file the load compiled that cannot be read, or whose bytes
// disagree with the positions the load recorded.
var ErrSource = errors.New("inventory: source unavailable")

// Positions renders a token.Pos as the position a report carries: a path
// relative to the target root with the solidus as separator on every platform,
// and a column counting UTF-16 code units.
//
// The path is made relative lexically, so a root spelled through a symbolic link
// does not match the paths the toolchain reported and nothing under it renders.
type Positions struct {
	Fset *token.FileSet
	read ReadFile
	rel  map[string]string
	src  map[string][]byte
	Root string
}

// NewPositions renders against root, reading source through read.
func NewPositions(fset *token.FileSet, root string, read ReadFile) *Positions {
	return &Positions{
		Fset: fset,
		Root: filepath.Clean(root),
		read: read,
		rel:  make(map[string]string),
		src:  make(map[string][]byte),
	}
}

// Under renders against another root with the same file set and reader, which is
// how a loaded consumer's files render against the consumer's own root.
func (p *Positions) Under(root string) *Positions { return NewPositions(p.Fset, root, p.read) }

// Base returns the file name of the file holding pos, without its directory.
func (p *Positions) Base(pos token.Pos) string {
	return filepath.Base(p.Fset.Position(pos).Filename)
}

// dir returns the directory of the file holding pos, as the file set names it
// before any line directive.
func (p *Positions) dir(pos token.Pos) string {
	return filepath.Dir(p.Fset.PositionFor(pos, false).Filename)
}

// Offset is where a file writes a byte, before any line directive: the file as the
// file set names it, and the byte's offset in it. Two parses of one file write one
// declaration at one Offset, and source order is Offset order, which a rendered
// position under a line directive is not.
type Offset struct {
	File string
	At   int
}

// Offset returns where pos is written. It reads no source.
func (p *Positions) Offset(pos token.Pos) Offset {
	q := p.Fset.PositionFor(pos, false)
	return Offset{File: filepath.Clean(q.Filename), At: q.Offset}
}

// SourceRange is the run of one file's bytes a syntax node covers, its end excluded.
type SourceRange struct {
	File     string
	From, To int
}

// Range returns the run of source node covers. It reads no source.
func (p *Positions) Range(node ast.Node) SourceRange {
	from, to := p.Offset(node.Pos()), p.Offset(node.End())
	return SourceRange{File: from.File, From: from.At, To: to.At}
}

// Holds reports whether the byte at is inside the range.
func (s *SourceRange) Holds(at Offset) bool {
	return at.File == s.File && at.At >= s.From && at.At < s.To
}

// Render converts pos into the position a report carries.
func (p *Positions) Render(pos token.Pos) (token.Position, error) {
	q := p.Fset.Position(pos)
	if !q.IsValid() {
		return token.Position{}, fmt.Errorf("%w: invalid position %d", ErrSource, pos)
	}
	rel := p.Relative(q.Filename)
	if rel == "" {
		return token.Position{}, fmt.Errorf("%w: %s is outside %s", ErrSource, q.Filename, p.Root)
	}
	column, err := p.column(q)
	if err != nil {
		return token.Position{}, err
	}
	q.Filename = rel
	q.Column = column
	return q, nil
}

// symbolID spells the identifier of the declaration written at one rendered
// position: the target-relative path, the line and the column, joined by
// colons. Every pass that resolves an object to a declaration renders the
// object's position and looks the identifier up, so the form is written here and
// nowhere else.
func (*Positions) symbolID(q token.Position) SymbolID {
	return SymbolID(fmt.Sprintf("%s:%d:%d", q.Filename, q.Line, q.Column))
}

// Relative returns name relative to the target root with forward slashes, and an
// empty string when name is not under the root.
func (p *Positions) Relative(name string) string {
	if rel, ok := p.rel[name]; ok {
		return rel
	}
	rel := ""
	if r, err := filepath.Rel(p.Root, filepath.Clean(name)); err == nil && filepath.IsLocal(r) {
		rel = filepath.ToSlash(r)
	}
	p.rel[name] = rel
	return rel
}

// column converts the byte column Go reports into the number of UTF-16 code
// units before it on the same line, counted from one.
func (p *Positions) column(q token.Position) (int, error) {
	src, err := p.source(q.Filename)
	if err != nil {
		return 0, err
	}
	// A line directive naming no column leaves the column unknown, which Go reports
	// as 0 and which counts as the first.
	start := q.Offset - max(q.Column-1, 0)
	if start < 0 || q.Offset > len(src) {
		return 0, fmt.Errorf("%w: %s:%d:%d is outside the file's %d bytes",
			ErrSource, q.Filename, q.Line, q.Column, len(src))
	}
	return Column(src[start:], q.Offset-start), nil
}

// Column is the column of the byte at offset of line, counting UTF-16 code units
// from one, which is what SARIF and the Language Server Protocol count where Go
// counts bytes. An offset past the line counts the whole of it and a negative one
// counts nothing; an offset inside a rune counts each byte of the cut rune as one
// replacement character.
func Column(line []byte, offset int) int {
	offset = min(max(offset, 0), len(line))
	column := 1
	for _, r := range string(line[:offset]) {
		if units := utf16.RuneLen(r); units > 0 {
			column += units
			continue
		}
		// A rune no UTF-16 encoding holds is one code unit, the replacement
		// character the encoder would write for it.
		column++
	}
	return column
}

// ByPosition orders two rendered positions by file, line and column, which is the
// order the inventory reads in and the order every set of a run is reported in. It
// is the whole of the analysis' position order: a comparator that ranks records
// carrying a position calls this rather than comparing the three fields again.
func ByPosition(a, b token.Position) int {
	if c := strings.Compare(a.Filename, b.Filename); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Line, b.Line); c != 0 {
		return c
	}
	return cmp.Compare(a.Column, b.Column)
}

// source returns one file's bytes, reading it at most once.
func (p *Positions) source(name string) ([]byte, error) {
	if src, ok := p.src[name]; ok {
		return src, nil
	}
	src, err := p.read(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSource, name, err)
	}
	p.src[name] = src
	return src, nil
}

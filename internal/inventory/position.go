package inventory

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// ErrSource reports a file the load compiled that cannot be read, or whose bytes
// disagree with the positions the load recorded.
var ErrSource = errors.New("inventory: source unavailable")

// Positions renders a token.Pos as the position a report carries: a path
// relative to the target root with the solidus as separator on every platform,
// and a column counting UTF-16 code units.
//
// The path is placed where both the file's directory and the root resolve
// ([scope.Root.PlaceEntry]), so a root spelled through a symbolic link renders the
// files the toolchain names through the directory it leads to. Positions with no
// root render no path.
type Positions struct {
	Fset   *token.FileSet
	read   ReadFile
	rel    map[string]string
	src    map[string][]byte
	placed scope.Root
	Root   string
}

// NewPositions renders against root, reading source through read.
func NewPositions(fset *token.FileSet, root string, read ReadFile) *Positions {
	p := &Positions{
		Fset: fset,
		Root: filepath.Clean(root),
		read: read,
		rel:  make(map[string]string),
		src:  make(map[string][]byte),
	}
	if root != "" {
		p.placed = scope.RootOf(root)
	}
	return p
}

// Under renders against another root with the same file set and reader, which is
// how a loaded consumer's files render against the consumer's own root.
func (p *Positions) Under(root string) *Positions { return NewPositions(p.Fset, root, p.read) }

// base returns the file name of the file holding pos, without its directory.
func (p *Positions) base(pos token.Pos) string {
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

// Render converts pos into the position a report carries: [Positions.Place], with
// its column in UTF-16 units. Only the written file is read, because a directive
// may name a file that does not exist.
func (p *Positions) Render(pos token.Pos) (token.Position, error) {
	q, moved, err := p.place(pos)
	if err != nil {
		return token.Position{}, err
	}
	w := p.Fset.PositionFor(pos, false)
	line, err := p.before(w)
	if err != nil {
		return token.Position{}, err
	}
	if moved {
		line = line[p.counted(pos, line):]
	}
	q.Column = rendered(q.Column, line)
	return q, nil
}

// Place is the root-relative file and line a report names pos at, reading no file:
// where a line directive renders it, and where the file writes it when the
// directive names a file outside the root, which no report can name. Its column is
// the rendered byte column.
func (p *Positions) Place(pos token.Pos) (token.Position, error) {
	q, _, err := p.place(pos)
	return q, err
}

// place is [Positions.Place], and whether the position is the one a line directive
// renders rather than the written one.
func (p *Positions) place(pos token.Pos) (token.Position, bool, error) {
	w := p.Fset.PositionFor(pos, false)
	if !w.IsValid() {
		return token.Position{}, false, fmt.Errorf("%w: invalid position %d", ErrSource, pos)
	}
	q, moved := p.Fset.Position(pos), true
	rel := p.Relative(q.Filename)
	if rel == "" {
		q, moved, rel = w, false, p.Relative(w.Filename)
	}
	if rel == "" {
		return token.Position{}, false, fmt.Errorf("%w: %s is outside %s", ErrSource, w.Filename, p.Root)
	}
	q.Filename = rel
	return q, moved, nil
}

// counted is the index in line, the written bytes before pos on its line, of the
// first byte pos's rendered column counts: the byte after an inline /*line*/
// directive in effect at pos, else the line start, where a //line directive above
// takes effect. go/token counts the column from there and exposes no directive, so
// the run is the bytes before pos whose rendered columns step by one to pos's.
func (p *Positions) counted(pos token.Pos, line []byte) int {
	if !bytes.Contains(line, []byte("/*line ")) {
		return 0
	}
	at := p.Fset.Position(pos)
	from := len(line)
	for from > 0 {
		q := p.Fset.Position(pos - token.Pos(len(line)-from+1))
		if q.Filename != at.Filename || q.Line != at.Line || q.Column != at.Column-(len(line)-from+1) {
			break
		}
		from--
	}
	return from
}

// rendered is the UTF-16 column of a byte a line directive renders at byte column
// at, where run is the written bytes the byte column counts before it. A directive
// that names no column leaves it unknown, which counts as the first.
func rendered(at int, run []byte) int {
	if at == 0 {
		return 1
	}
	return max(at-(len(run)-(Column(run, len(run))-1)), 1)
}

// symbolID spells the identifier of the declaration written at pos: the
// target-relative path of the file that writes it, the line and the UTF-16 column
// there, joined by colons, all read before any line directive. A line directive
// can render two declarations at one position, never write them at one, so the
// identifier is the rendered position only where no directive moves it.
func (p *Positions) symbolID(pos token.Pos) (SymbolID, error) {
	q := p.Fset.PositionFor(pos, false)
	rel := p.Relative(q.Filename)
	if rel == "" {
		return "", fmt.Errorf("%w: %s is outside %s", ErrSource, q.Filename, p.Root)
	}
	column, err := p.column(q)
	if err != nil {
		return "", err
	}
	return SymbolID(fmt.Sprintf("%s:%d:%d", rel, q.Line, column)), nil
}

// File is the root-relative path of the file that writes pos, which a line
// directive does not move, and empty where the root does not hold it.
func (p *Positions) File(pos token.Pos) string { return p.Relative(p.Offset(pos).File) }

// Line is the line of [Positions.File] that writes pos, which a line directive
// does not move.
func (p *Positions) Line(pos token.Pos) int { return p.Fset.PositionFor(pos, false).Line }

// InTestFile reports whether the file that writes pos is a test file
// ([IsTestFile]). A line directive naming a test file makes no production code a
// test's, so the name is the one the file set holds before any directive.
func (p *Positions) InTestFile(pos token.Pos) bool {
	_, test := IsTestFile(p.Offset(pos).File)
	return test
}

// Relative returns name relative to the target root with forward slashes, and an
// empty string when name is not under the root.
func (p *Positions) Relative(name string) string {
	if rel, ok := p.rel[name]; ok {
		return rel
	}
	rel := ""
	if r, inside := p.placed.PlaceEntry(name); inside {
		rel = r
	}
	p.rel[name] = rel
	return rel
}

// column converts the byte column of q, a position read before any line directive,
// into the number of UTF-16 code units before it on the same line, counted from one.
func (p *Positions) column(q token.Position) (int, error) {
	line, err := p.before(q)
	if err != nil {
		return 0, err
	}
	return Column(line, len(line)), nil
}

// before returns the bytes of q's line before q, q read before any line directive.
func (p *Positions) before(q token.Position) ([]byte, error) {
	src, err := p.source(q.Filename)
	if err != nil {
		return nil, err
	}
	start := q.Offset - (q.Column - 1)
	if start < 0 || q.Offset > len(src) {
		return nil, fmt.Errorf("%w: %s:%d:%d is outside the file's %d bytes",
			ErrSource, q.Filename, q.Line, q.Column, len(src))
	}
	return src[start:q.Offset], nil
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

package graph

import (
	"cmp"
	"errors"
	"fmt"
	"go/token"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// ErrSource reports a file the load compiled that cannot be read, or whose bytes
// disagree with the positions the load recorded.
var ErrSource = errors.New("graph: source unavailable")

// positions renders a token.Pos as the position a report carries: a path
// relative to the target root with the solidus as separator on every platform,
// and a column counting UTF-16 code units.
//
// The path is made relative lexically, so a root spelled through a symbolic link
// does not match the paths the toolchain reported and nothing under it renders.
type positions struct {
	fset *token.FileSet
	read ReadFile
	rel  map[string]string
	src  map[string][]byte
	root string
}

// newPositions renders against root, reading source through read.
func newPositions(fset *token.FileSet, root string, read ReadFile) *positions {
	return &positions{
		fset: fset,
		root: filepath.Clean(root),
		read: read,
		rel:  make(map[string]string),
		src:  make(map[string][]byte),
	}
}

// base returns the file name of the file holding pos, without its directory.
func (p *positions) base(pos token.Pos) string {
	return filepath.Base(p.fset.Position(pos).Filename)
}

// render converts pos into the position a report carries.
func (p *positions) render(pos token.Pos) (token.Position, error) {
	q := p.fset.Position(pos)
	if !q.IsValid() {
		return token.Position{}, fmt.Errorf("%w: invalid position %d", ErrSource, pos)
	}
	rel := p.relative(q.Filename)
	if rel == "" {
		return token.Position{}, fmt.Errorf("%w: %s is outside %s", ErrSource, q.Filename, p.root)
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
func (*positions) symbolID(q token.Position) SymbolID {
	return SymbolID(fmt.Sprintf("%s:%d:%d", q.Filename, q.Line, q.Column))
}

// relative returns name relative to the target root with forward slashes, and an
// empty string when name is not under the root.
func (p *positions) relative(name string) string {
	if rel, ok := p.rel[name]; ok {
		return rel
	}
	rel := ""
	if r, err := filepath.Rel(p.root, filepath.Clean(name)); err == nil && filepath.IsLocal(r) {
		rel = filepath.ToSlash(r)
	}
	p.rel[name] = rel
	return rel
}

// column converts the byte column Go reports into the number of UTF-16 code
// units before it on the same line, counted from one.
func (p *positions) column(q token.Position) (int, error) {
	src, err := p.source(q.Filename)
	if err != nil {
		return 0, err
	}
	start := q.Offset - (q.Column - 1)
	if start < 0 || q.Offset > len(src) {
		return 0, fmt.Errorf("%w: %s:%d:%d is outside the file's %d bytes",
			ErrSource, q.Filename, q.Line, q.Column, len(src))
	}
	return Column(src[start:], q.Offset-start), nil
}

// Column is the column of the byte at offset of line, counting UTF-16 code units
// from one. It is the column every position of a report carries: SARIF counts
// columns in UTF-16 code units, the Language Server Protocol counts them that way
// by default, and Go counts bytes, so the conversion reads the bytes before the
// offset on the line.
//
// The line may run past the offset, so a whole file's remainder is a line here as
// far as the count is concerned. An offset past the line counts the whole of it
// and a negative one counts nothing, which are the two answers a caller can read
// without a second bound check. Every position the toolchain reports falls on a
// rune boundary; an offset inside a rune counts each byte of the cut rune as the
// replacement character one byte decodes to.
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
// is the whole of the graph's position order: a comparator that ranks records
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
func (p *positions) source(name string) ([]byte, error) {
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

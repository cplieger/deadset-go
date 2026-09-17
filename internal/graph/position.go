package graph

import (
	"errors"
	"fmt"
	"go/token"
	"path/filepath"
	"unicode/utf16"
)

// ErrSource reports a file the load compiled that cannot be read, or whose bytes
// disagree with the positions the load recorded.
var ErrSource = errors.New("graph: source unavailable")

// positions renders a token.Pos as the position a report carries: a path
// relative to the target root with the solidus as separator on every platform,
// and a column counting UTF-16 code units.
//
// Containment and the relative path are decided lexically, so a root spelled
// through a symbolic link does not match the paths the toolchain reported.
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

// inside reports whether the file holding pos is under the target root.
func (p *positions) inside(pos token.Pos) bool {
	q := p.fset.Position(pos)
	return q.IsValid() && p.relative(q.Filename) != ""
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

	column := 1
	for _, r := range string(src[start:q.Offset]) {
		if units := utf16.RuneLen(r); units > 0 {
			column += units
			continue
		}
		column++
	}
	return column, nil
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

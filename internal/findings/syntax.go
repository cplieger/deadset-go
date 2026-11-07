package findings

import (
	"go/ast"

	"golang.org/x/tools/go/packages"
)

// Loads is every configuration of the run, in matrix order. At may load a
// configuration again where memory pressure dropped it, and a second load's syntax
// trees and positions are the first's while its type records are its own: a reader
// keeps no type information of one configuration once it reads the next, only what
// identifiers and positions name.
type Loads interface {
	Len() int
	At(config int) (*Configured, error)
}

// each calls visit with every configuration of per in matrix order, one at a time,
// and ends at the first error.
func each(per Loads, visit func(config int, one *Configured) error) error {
	if per == nil {
		return nil
	}
	for c := range per.Len() {
		one, err := per.At(c)
		if err != nil {
			return err
		}
		if err := visit(c, one); err != nil {
			return err
		}
	}
	return nil
}

// typedFile is one file of one build configuration, with the package variant it is
// read from.
type typedFile struct {
	one  *Configured
	pkg  *packages.Package
	file *ast.File
}

// sourcesOf is the one walk every reading of one configuration's syntax shares:
// each file once ([facts.Index.Sources]), since the bodies are the same bytes.
func sourcesOf(one *Configured) []typedFile {
	if one.Facts == nil {
		return nil
	}
	var found []typedFile
	for p, f := range one.Facts.Sources() {
		if p.TypesInfo != nil {
			found = append(found, typedFile{one: one, pkg: p, file: f})
		}
	}
	return found
}

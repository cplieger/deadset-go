package findings

import (
	"go/ast"
	"iter"

	"golang.org/x/tools/go/packages"
)

// typedPackages yields every type-checked package variant of every configuration
// of the run, with its configuration, in the walk order.
func (in *pass) typedPackages() iter.Seq2[*Configured, *packages.Package] {
	return func(yield func(*Configured, *packages.Package) bool) {
		for i := range in.Per {
			one := &in.Per[i]
			if one.Facts == nil {
				continue
			}
			for p := range one.Facts.Typed() {
				if !yield(one, p) {
					return
				}
			}
		}
	}
}

// typedFile is one file of one build configuration, with the package variant it is
// read from.
type typedFile struct {
	one    *Configured
	pkg    *packages.Package
	file   *ast.File
	config int
}

// sourcesOf is the one walk every reading of syntax shares: each file of each
// configuration once ([facts.Index.Sources]), since the bodies are the same bytes.
func sourcesOf(in *Input) []typedFile {
	var found []typedFile
	for c := range in.Per {
		one := &in.Per[c]
		if one.Facts == nil {
			continue
		}
		for p, f := range one.Facts.Sources() {
			if p.TypesInfo != nil {
				found = append(found, typedFile{config: c, one: one, pkg: p, file: f})
			}
		}
	}
	return found
}

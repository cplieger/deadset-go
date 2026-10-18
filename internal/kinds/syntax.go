package kinds

import (
	"go/ast"
	"iter"

	"golang.org/x/tools/go/packages"
)

// typedPackages yields every type-checked package variant of every configuration
// of the run, with its configuration, in the walk order.
func (in *Input) typedPackages() iter.Seq2[*Configured, *packages.Package] {
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

// typedFile is one syntax tree of a type-checked package variant.
type typedFile struct {
	one  *Configured
	pkg  *packages.Package
	file *ast.File
}

// typedFiles yields every syntax tree of every package typedPackages yields.
func (in *Input) typedFiles() iter.Seq[typedFile] {
	return func(yield func(typedFile) bool) {
		for one, p := range in.typedPackages() {
			for _, f := range one.Facts.Files(p) {
				if !yield(typedFile{one: one, pkg: p, file: f}) {
					return
				}
			}
		}
	}
}

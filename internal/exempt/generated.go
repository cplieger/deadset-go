package exempt

import (
	"go/ast"
	"go/token"

	"github.com/cplieger/deadset-go/internal/graph"
)

// detailGenerated is the clause every exemption of the class records: the
// generator owns the file, which is the whole reason, so one text serves every
// declaration the class retains.
const detailGenerated = "declared in a generated file"

// IsGeneratedFile reports whether f carries the standard Go generated-code
// header: a line reading "// Code generated <generator> DO NOT EDIT." before the
// first non-comment, non-blank text of the file.
//
// The rule is go/ast's, so a file this reports on is one the toolchain's own
// readers treat as generated too, and one function answers the question for the
// whole analyzer.
func IsGeneratedFile(f *ast.File) bool { return ast.IsGenerated(f) }

// GeneratedFileDetector retains every declaration written in a generated file:
// the generator owns the file, so a deletion there is undone the next time it
// runs and the declaration to remove is in the generator's input rather than in
// the tree.
//
// Under Options.IncludeGenerated the class retains nothing and every declaration
// of a generated file is judged like any other, which is what a project asks for
// when it configures its generated files as included.
//
// A package and a file are not declarations IN a file: the package clause of one
// generated file would otherwise retain every declaration of the package, and the
// sweep judges neither kind in any case. The site is the package clause of the
// generated file and the clause names the file rather than its generator, because
// go/ast reports that a file is generated without exporting the header line it
// matched or the generator that line names, and matching the header again here
// would be a second implementation of a rule the standard library owns.
func GeneratedFileDetector(in *Input) ([]graph.Exemption, error) {
	if in.Options.IncludeGenerated {
		return nil, nil
	}
	sites, err := generatedSites(in)
	if err != nil {
		return nil, err
	}

	var found []graph.Exemption
	for i := range in.Symbols {
		s := &in.Symbols[i]
		if s.Kind == graph.KindPackage || s.Kind == graph.KindFile {
			continue
		}
		site, generated := sites[s.Pos.Filename]
		if !generated {
			continue
		}
		found = append(found, graph.Exemption{
			ID:     s.ID,
			Class:  string(GeneratedFile),
			Site:   site,
			Detail: detailGenerated,
		})
	}
	return found, nil
}

// generatedSites renders the package clause of every generated file of the
// configuration, keyed by the target-relative path the inventory spells its
// positions with. One file is reached once per variant of its package and renders
// to one entry.
func generatedSites(in *Input) (map[string]token.Position, error) {
	sites := make(map[string]token.Position)
	for _, p := range in.Result.Packages {
		for _, f := range p.Syntax {
			if !IsGeneratedFile(f) {
				continue
			}
			site, err := in.Resolve.Render(f.Package)
			if err != nil {
				return nil, err
			}
			sites[site.Filename] = site
		}
	}
	return sites, nil
}

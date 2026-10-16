package exempt

import (
	"go/token"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// detailGenerated is the clause every exemption of the class records: the
// generator owns the file, which is the whole reason, so one text serves every
// declaration the class retains.
const detailGenerated = "declared in a generated file"

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
	sites, err := GeneratedSites(in.Result, in.Resolve)
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
			Class:  string(vocab.GeneratedFile),
			Site:   site,
			Detail: detailGenerated,
		})
	}
	return found, nil
}

// GeneratedSites renders the package clause of every generated file of one
// configuration's load, keyed by the target-relative path the inventory spells its
// positions with. One file is reached once per variant of its package and renders
// to one entry.
func GeneratedSites(r *load.Result, resolve *graph.Resolver) (map[string]token.Position, error) {
	sites := make(map[string]token.Position)
	for _, clause := range r.Generated {
		site, err := resolve.Render(clause)
		if err != nil {
			return nil, err
		}
		sites[site.Filename] = site
	}
	return sites, nil
}

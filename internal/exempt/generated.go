package exempt

import (
	"go/token"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// detailGenerated is the clause every exemption of the class records: the
// generator owns the file, which is the whole reason, so one text serves every
// declaration the class retains.
const detailGenerated = "declared in a generated file"

// GeneratedFileDetector retains every declaration written in a generated file:
// the generator owns the file, so a deletion there is undone the next time it
// runs. Under Options.IncludeGenerated it retains nothing.
//
// A package and a file are not declarations in a file: the package clause of one
// generated file would otherwise retain every declaration of the package. The site
// is the file's package clause.
func GeneratedFileDetector(in *Input) ([]Exemption, error) {
	if in.Options.IncludeGenerated {
		return nil, nil
	}
	sites := make(map[string]token.Position)
	all := in.Facts.Generated()
	for i := range all {
		sites[all[i].Path] = all[i].Site
	}

	var found []Exemption
	for i := range in.Symbols {
		s := &in.Symbols[i]
		if s.Kind == inventory.KindPackage || s.Kind == inventory.KindFile {
			continue
		}
		site, generated := sites[s.Pos.Filename]
		if !generated {
			continue
		}
		found = append(found, Exemption{
			ID:     s.ID,
			Class:  string(vocab.GeneratedFile),
			Site:   site,
			Detail: detailGenerated,
		})
	}
	return found, nil
}

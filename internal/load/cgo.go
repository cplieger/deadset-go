package load

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"

	"golang.org/x/tools/go/packages"
)

// cgoImportPath is the pseudo-package a file imports to reach C, spelled the way
// an import spec spells it.
const cgoImportPath = `"C"`

// cgoOnly returns the absolute paths of the Go files the toolchain ignored solely
// because they import "C", which are the files the opaque-C check reads.
func cgoOnly(roots []*packages.Package, c Configuration) (map[string]bool, error) {
	ctxt := cgoEnabledContext(c)
	decided := make(map[string]bool)
	for _, p := range roots {
		for _, file := range p.IgnoredFiles {
			if _, done := decided[file]; done {
				continue
			}
			alone, err := excludedByCgoAlone(&ctxt, file)
			if err != nil {
				return nil, err
			}
			decided[file] = alone
		}
	}
	maps.DeleteFunc(decided, func(_ string, alone bool) bool { return !alone })
	return decided, nil
}

// cgoEnabledContext is configuration c with cgo enabled, which is what answers
// whether the configuration would select a file with cgo out of the way.
func cgoEnabledContext(c Configuration) build.Context {
	ctxt := build.Default
	ctxt.GOOS = c.OS
	ctxt.GOARCH = c.Arch
	ctxt.BuildTags = slices.Clone(c.Tags)
	ctxt.CgoEnabled = true
	return ctxt
}

// excludedByCgoAlone reports whether the ignored file at path is one cgo alone
// excludes: it imports "C", and the configuration would select it with cgo
// enabled.
//
// With cgo disabled the toolchain reports a file importing "C" exactly where it
// reports a file no build constraint selects, so the two are told apart by
// reading the file. Both halves of the test carry weight. A file some other rule
// also excludes fails the second half and stays in the population the build
// configurations decide. A file excluded by the cgo build tag without importing
// "C" fails the first half and stays there too, because that tag is an atom a
// configuration names and can therefore be reasoned about.
//
// A Go file whose imports cannot be read is an error rather than a file silently
// treated as importing nothing.
func excludedByCgoAlone(ctxt *build.Context, path string) (bool, error) {
	// IgnoredFiles carries every ignored file, assembly and C sources included;
	// only a Go file can import "C".
	if filepath.Ext(path) != ".go" {
		return false, nil
	}
	imports, err := importsCgo(path)
	if err != nil {
		return false, err
	}
	if !imports {
		return false, nil
	}
	selected, err := ctxt.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return false, fmt.Errorf("read build constraints of %s: %w", path, err)
	}
	return selected, nil
}

// relativeSorted renders one set of absolute paths relative to base, with forward
// slashes, in order, and nothing at all for an empty set.
func relativeSorted(base string, paths map[string]bool) []string {
	if len(paths) == 0 {
		return nil
	}
	rendered := make([]string, 0, len(paths))
	for path := range paths {
		rendered = append(rendered, relativeToSlash(base, path))
	}
	slices.Sort(rendered)
	return slices.Compact(rendered)
}

// relativeToSlash renders path relative to base with forward slashes, falling
// back to path itself when no relative form exists.
func relativeToSlash(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// importsCgo reports whether the Go file at path imports "C".
func importsCgo(path string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return false, fmt.Errorf("read imports of %s: %w", path, err)
	}
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == cgoImportPath {
			return true, nil
		}
	}
	return false, nil
}

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
func cgoOnly(roots []*packages.Package, c *Configuration) (map[string]bool, error) {
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

// cgoEnabledContext is the build context of configuration c with cgo enabled,
// which is what answers whether the configuration would select a file with cgo
// out of the way.
func cgoEnabledContext(c *Configuration) build.Context {
	ctxt := build.Default
	ctxt.GOOS = c.OS
	ctxt.GOARCH = c.Arch
	ctxt.BuildTags = slices.Clone(c.Tags)
	ctxt.CgoEnabled = true
	return ctxt
}

// excludedByCgoAlone reports whether the ignored file at path is one cgo alone
// excludes: it imports "C", and the configuration would select it with cgo
// enabled. With cgo disabled the toolchain ignores such a file exactly as it
// ignores one no constraint selects, so the file is read to tell them apart; a
// file another rule or the cgo tag also excludes stays with the configurations.
// A Go file whose imports cannot be read is an error, never a file importing
// nothing.
func excludedByCgoAlone(ctxt *build.Context, path string) (bool, error) {
	// IgnoredFiles carries every ignored file, assembly and C sources included;
	// only a Go file can import "C".
	if filepath.Ext(path) != goFileExt {
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
		rendered = append(rendered, RelativeSlash(base, path))
	}
	slices.Sort(rendered)
	return slices.Compact(rendered)
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

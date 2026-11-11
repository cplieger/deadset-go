package exempt

import (
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// TestLinknameCgoAsmPluginDetectorRetainsNeitherSideOfALinknameDirective pins
// that a linkname directive is no evidence of this class: the names it joins are
// roots of the analysis, which the graph's root detection keeps.
func TestLinknameCgoAsmPluginDetectorRetainsNeitherSideOfALinknameDirective(t *testing.T) {
	t.Parallel()
	shared := analysisOf(t, "linkname.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(linkname.txtar) error: %v", err)
	}
	if lines := rendered(symbols, got); len(lines) != 0 {
		t.Errorf("LinknameCgoAsmPluginDetector(linkname.txtar) = %q, want none", lines)
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsEveryDeclarationOfAPackageWithAssembly
// pins rule N: no assembly form is read, so every declaration of the package is
// retained, whatever the file names, against the assembly file's first line.
func TestLinknameCgoAsmPluginDetectorRetainsEveryDeclarationOfAPackageWithAssembly(t *testing.T) {
	t.Parallel()
	shared := analysisOf(t, "assembly.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(assembly.txtar) error: %v", err)
	}

	const site = "\tlinkname-cgo-asm-plugin\tadd_amd64.s:1:1\t" + detailNonGoPackage
	want := []string{
		"Sum" + site, "add" + site, "commented" + site, "data" + site,
		"fileLocal" + site, "other" + site, "pair" + site, "pair.left" + site, "width" + site,
	}
	lines := rendered(symbols, got)
	slices.Sort(lines)
	if !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(assembly.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsWhatANonGoSourceOfTheTreeNames pins the
// tree search: a variable and a function of a package with no non-Go source are
// retained when a non-Go file elsewhere in the tree writes their names, an
// underscore file only an #include reaches among them, and a file inside another
// module's directory is not read.
func TestLinknameCgoAsmPluginDetectorRetainsWhatANonGoSourceOfTheTreeNames(t *testing.T) {
	t.Parallel()
	shared := analysisOf(t, "non-go-tree.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(non-go-tree.txtar) error: %v", err)
	}

	want := []string{
		"Probe\tlinkname-cgo-asm-plugin\tb/ref_s390x.s:1:1\t" + detailNonGoName,
		"probe\tlinkname-cgo-asm-plugin\tb/_hidden.s:1:1\t" + detailNonGoName,
		"run\tlinkname-cgo-asm-plugin\tb/_hidden.s:1:1\t" + detailNonGoPackage,
	}
	lines := rendered(symbols, got)
	slices.Sort(lines)
	if !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(non-go-tree.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorReadsASymbolicLinkAsTheBuildDoes pins rule N on
// symbolic links: one to a file is a source of its package, as go/build lists it,
// and one to a directory is not.
func TestLinknameCgoAsmPluginDetectorReadsASymbolicLinkAsTheBuildDoes(t *testing.T) {
	t.Parallel()
	shared := analysisOf(t, "non-go-link.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(non-go-link.txtar) error: %v", err)
	}

	want := []string{
		"run\tlinkname-cgo-asm-plugin\tc/run_amd64.s:1:1\t" + detailNonGoPackage,
		"size\tlinkname-cgo-asm-plugin\tc/run_amd64.s:1:1\t" + detailNonGoPackage,
	}
	lines := rendered(symbols, got)
	slices.Sort(lines)
	if !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(non-go-link.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorReadsNoLinkNoCompilerCanRead pins rule N on
// symbolic links no compiler reads: one to nothing, one through a file and one that
// loops name nothing.
func TestLinknameCgoAsmPluginDetectorReadsNoLinkNoCompilerCanRead(t *testing.T) {
	t.Parallel()
	for _, archive := range []string{"non-go-dangling.txtar", "non-go-loop.txtar"} {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()
			shared := analysisOf(t, archive, Options{})
			got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
			if err != nil {
				t.Fatalf("LinknameCgoAsmPluginDetector(%s) error: %v", archive, err)
			}
			if lines := rendered(shared.inventory(t), got); len(lines) != 0 {
				t.Errorf("LinknameCgoAsmPluginDetector(%s) = %q, want nothing retained", archive, lines)
			}
		})
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsWhereItCannotReadTheTree pins rule N's
// fail-safe: a file and a directory of the tree the run cannot read each may name
// any function or variable, so every one is retained at the first, and Compute
// lists both for the report's notes.
func TestLinknameCgoAsmPluginDetectorRetainsWhereItCannotReadTheTree(t *testing.T) {
	t.Parallel()
	in := inputOf(t, "non-go-unreadable.txtar", Options{})
	secret := filepath.Join(in.Root, "docs", "secret.h")
	pg := filepath.Join(in.Root, "testdata", "pg")
	read, readDir := in.Read, in.ReadDir
	in.Read = func(name string) ([]byte, error) {
		if name == secret {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return read(name)
	}
	in.ReadDir = func(name string) ([]fs.DirEntry, error) {
		if name == pg {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return readDir(name)
	}

	computed, err := Compute(in, map[vocab.Exemption]Detector{vocab.LinknameCgoAsmPlugin: LinknameCgoAsmPluginDetector})
	if err != nil {
		t.Fatalf("Compute(non-go-unreadable.txtar) error: %v", err)
	}

	want := []string{
		"Run\tlinkname-cgo-asm-plugin\tdocs/secret.h:1:1\t" + detailNonGoUnread,
		"main\tlinkname-cgo-asm-plugin\tdocs/secret.h:1:1\t" + detailNonGoUnread,
		"spare\tlinkname-cgo-asm-plugin\tdocs/secret.h:1:1\t" + detailNonGoUnread,
	}
	lines := rendered(in.Symbols, computed.Held)
	slices.Sort(lines)
	if !slices.Equal(lines, want) {
		t.Errorf("Compute(non-go-unreadable.txtar).Held = %q, want %q", lines, want)
	}
	if want := []string{secret, pg}; !slices.Equal(computed.Unread, want) {
		t.Errorf("Compute(non-go-unreadable.txtar).Unread = %q, want %q", computed.Unread, want)
	}
}

func TestLinknameCgoAsmPluginDetectorRetainsTheExportedSymbolsOfAPluginMainPackage(t *testing.T) {
	t.Parallel()
	shared := analysisOf(t, "plugin.txtar", Options{})
	symbols := shared.inventory(t)

	got, err := shared.detect(t, vocab.LinknameCgoAsmPlugin)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(plugin.txtar) error: %v", err)
	}

	want := []string{
		"Handler\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
		"Greet\tlinkname-cgo-asm-plugin\tplug.go:1:1\texported from a plugin's main package",
	}
	if lines := rendered(symbols, got); !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(plugin.txtar) = %q, want %q", lines, want)
	}
}

// TestLinknameCgoAsmPluginDetectorRetainsEveryDeclarationOfACgoPackage pins that
// a Go file importing "C" makes its package hold a non-Go source (rule N). The
// load type-checks the file with the C pseudo-package opaque, so the file is among
// the syntax the class walks; the function exported to C is a root of the analysis
// besides.
func TestLinknameCgoAsmPluginDetectorRetainsEveryDeclarationOfACgoPackage(t *testing.T) {
	t.Parallel()
	in := inputOf(t, "cgo-export.txtar", Options{})

	if len(in.Result.ExcludedByCgo) != 0 {
		t.Fatalf("Setup: load(cgo-export.txtar).ExcludedByCgo = %q, want empty",
			in.Result.ExcludedByCgo)
	}

	got, err := LinknameCgoAsmPluginDetector(in)
	if err != nil {
		t.Fatalf("LinknameCgoAsmPluginDetector(cgo-export.txtar) error: %v", err)
	}
	const site = "\tlinkname-cgo-asm-plugin\texport.go:1:1\t" + detailNonGoPackage
	want := []string{"Exported" + site, "Size" + site}
	lines := rendered(in.Symbols, got)
	slices.Sort(lines)
	if !slices.Equal(lines, want) {
		t.Errorf("LinknameCgoAsmPluginDetector(cgo-export.txtar) = %q, want %q", lines, want)
	}
}

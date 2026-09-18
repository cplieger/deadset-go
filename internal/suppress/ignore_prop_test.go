package suppress

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"pgregory.net/rapid"
)

// The module a drawn inventory declares, and the names a drawn declaration takes.
// The set is small so that one name recurs across files and packages, which is
// what an entry naming one of them must not reach.
const drawnModule = "example.com/drawn"

func drawnNames() []string { return []string{"Helper", "Resolve", "entries"} }

// drawnDeclaration is one declaration a draw placed: the package and the file
// that hold it, and its name.
type drawnDeclaration struct {
	pkg  string
	file string
	name string
}

// ref is the stable symbol reference the declaration carries. Two declarations of
// one package under one name carry one reference, which is what makes the path the
// second half of a match.
func (d drawnDeclaration) ref() string {
	return "go://" + drawnModule + d.pkg + "#" + d.name
}

// drawnInventory is one inventory with the same names spread over files and
// packages, and the declaration one entry names.
type drawnInventory struct {
	declarations []drawnDeclaration
	names        int
}

// drawnInventories draws one to four packages, each with one to three files, each
// declaring one to three of the names, so the same name recurs across files and
// packages by construction.
func drawnInventories() *rapid.Generator[drawnInventory] {
	return rapid.Custom(func(t *rapid.T) drawnInventory {
		var declarations []drawnDeclaration
		packages := rapid.IntRange(1, 4).Draw(t, "the packages")
		for p := range packages {
			pkg := ""
			if p > 0 {
				pkg = fmt.Sprintf("/p%d", p)
			}
			for f := range rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("the files of package %d", p)) {
				file := fmt.Sprintf("p%d/f%d.go", p, f)
				for _, name := range rapid.SliceOfNDistinct(
					rapid.SampledFrom(drawnNames()), 1, len(drawnNames()),
					func(s string) string { return s },
				).Draw(t, "the names "+file+" declares") {
					declarations = append(declarations, drawnDeclaration{pkg: pkg, file: file, name: name})
				}
			}
		}
		return drawnInventory{declarations: declarations, names: len(drawnNames())}
	})
}

// symbols indexes one drawn inventory the way the enumeration would: one
// declaration per line of its own file. No load takes place, so a draw costs no
// package load and shrinks to the declarations that carry a failure.
func (d drawnInventory) symbols() []graph.Symbol {
	symbols := make([]graph.Symbol, 0, len(d.declarations))
	lines := map[string]int{}
	for _, declaration := range d.declarations {
		lines[declaration.file]++
		at := token.Position{Filename: declaration.file, Line: lines[declaration.file], Column: 1}
		symbols = append(symbols, graph.Symbol{
			ID:      graph.SymbolID(fmt.Sprintf("%s:%d:%d", at.Filename, at.Line, at.Column)),
			Ref:     declaration.ref(),
			Name:    declaration.name,
			PkgPath: drawnModule + declaration.pkg,
			Pos:     at,
			EndLine: at.Line,
			Kind:    graph.KindFunc,
		})
	}
	return symbols
}

// describeDrawn prints one drawn inventory so a failure carries the inventory
// rather than only its size.
func (d drawnInventory) describeDrawn() string {
	var out strings.Builder
	for _, s := range d.symbols() {
		fmt.Fprintf(&out, "declaration %s at %s\n", s.Ref, s.ID)
	}
	return out.String()
}

// writeDrawnDocument writes one ignore document in a directory of its own,
// removed when the iteration that drew it ends, and returns its path.
func writeDrawnDocument(t *rapid.T, document string) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "deadset-property")
	if err != nil {
		t.Fatalf("Setup: create a document directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, IgnoreFileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return path
}

// Property dead-code-suite/P16: a suppression matches only what it names.
//
// For any inventory of same-named declarations spread over files and packages, an
// entry naming one code, one symbol and one path binds the declarations at that
// reference in that file and nothing else, and an entry naming a symbol and no
// path is refused rather than matched.
//
// The inventory is built by hand and the entry is read through the production
// reader, so the property measures the matching rule rather than any fixture's
// layout.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a deeper
// local run.
func TestProperty16ASuppressionMatchesOnlyWhatItNames(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		drawn := drawnInventories().Draw(t, "the inventory")
		symbols := drawn.symbols()
		named := rapid.SampledFrom(symbols).Draw(t, "the declaration the entry names")
		code := rapid.SampledFrom([]string{"DS1001", "DS1002", "DS1301"}).Draw(t, "the code the entry names")

		document := fmt.Sprintf(`{"ignore": [{"code": %q, "symbol": %q, "path": %q, "reason": "one entry names one symbol."}]}`,
			code, named.Ref, named.Pos.Filename)
		records, refusals, err := IgnoreFile(writeDrawnDocument(t, document), symbols)
		if err != nil {
			t.Fatalf("IgnoreFile(%s) error: %v\n%s", document, err, drawn.describeDrawn())
		}
		if len(refusals) != 0 {
			t.Fatalf("IgnoreFile(%s) returned %d refusals, want none: %+v\n%s", document, len(refusals), refusals, drawn.describeDrawn())
		}

		// Every declaration at the named reference in the named file is bound, and
		// no other is, which is the whole of the matching rule.
		var want []graph.SymbolID
		for _, s := range symbols {
			if s.Ref == named.Ref && s.Pos.Filename == named.Pos.Filename {
				want = append(want, s.ID)
			}
		}
		bound := make([]graph.SymbolID, 0, len(records))
		for _, r := range records {
			if r.Code != code || r.Symbol != named.Ref || r.Path != named.Pos.Filename {
				t.Fatalf("IgnoreFile(%s) returned the record %+v, which names something the entry does not\n%s",
					document, r, drawn.describeDrawn())
			}
			bound = append(bound, r.Bound)
		}
		if !slices.Equal(bound, want) {
			t.Fatalf("IgnoreFile(%s) bound %v, want %v\n%s", document, bound, want, drawn.describeDrawn())
		}

		// The same entry with its path dropped is refused rather than matched, so a
		// bare name cannot mask a match in another file.
		unscoped := fmt.Sprintf(`{"ignore": [{"code": %q, "symbol": %q, "reason": "one entry names one symbol."}]}`, code, named.Ref)
		records, refusals, err = IgnoreFile(writeDrawnDocument(t, unscoped), symbols)
		if err != nil {
			t.Fatalf("IgnoreFile(%s) error: %v\n%s", unscoped, err, drawn.describeDrawn())
		}
		if len(records) != 0 {
			t.Fatalf("IgnoreFile(%s) returned %d records for an entry naming no path, want none: %+v\n%s",
				unscoped, len(records), records, drawn.describeDrawn())
		}
		if len(refusals) != 1 || refusals[0].Reported != codeUnscoped {
			t.Fatalf("IgnoreFile(%s) returned %+v, want one refusal reported under %s\n%s",
				unscoped, refusals, codeUnscoped, drawn.describeDrawn())
		}
	})
}

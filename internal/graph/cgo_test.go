package graph

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// cgoArchive is the fixture whose every package holds a file the toolchain
// ignores solely for importing "C", and the prefix its references share.
const (
	cgoArchive = "cgo.txtar"
	cgoModule  = "go://example.com/cgo"
)

// cgoGraph is the fixture swept once, which is what every case below reads: one
// load of one module answers what the opaque-C check gave the three passes.
type cgoGraph struct {
	byID       map[SymbolID]Symbol
	byRef      map[string]bool
	references map[string][]string // a reference, and the references made to it
	roots      map[string][]RootKind
	files      map[string]bool // the files the enumeration holds a symbol for
	excluded   []string
	swept      Result
}

// sweepCgo runs the three passes over the cgo fixture under one configuration,
// merges and sweeps them, and gathers what a case reads by reference rather than
// by identifier.
func sweepCgo(t *testing.T) *cgoGraph {
	t.Helper()

	result, target := loadDirUnder(t.Context(), t, extract(t, cgoArchive), linuxAmd64())
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", cgoArchive, err)
	}
	refs, _, err := References(result, target, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: References(%s): %v", cgoArchive, err)
	}
	roots, _, err := Roots(result, target, os.ReadFile, symbols, RootOptions{PublishedAPI: true})
	if err != nil {
		t.Fatalf("Setup: Roots(%s): %v", cgoArchive, err)
	}
	merged, err := Merge([]Configured{{Symbols: symbols, References: refs, Roots: roots}})
	if err != nil {
		t.Fatalf("Setup: Merge(%s): %v", cgoArchive, err)
	}

	g := &cgoGraph{
		byID:       make(map[SymbolID]Symbol, len(merged.Symbols)),
		byRef:      make(map[string]bool, len(merged.Symbols)),
		references: make(map[string][]string),
		roots:      make(map[string][]RootKind),
		files:      make(map[string]bool),
		excluded:   result.ExcludedByCgo,
		swept:      NewMatrix(&merged).Sweep(SweepInput{}),
	}
	for _, s := range merged.Symbols {
		g.byID[s.ID] = s
		g.byRef[s.Ref] = true
		if s.Kind == KindFile {
			g.files[s.Pos.Filename] = true
		}
	}
	for _, r := range merged.References {
		to := g.byID[r.To].Ref
		g.references[to] = append(g.references[to], g.byID[r.From].Ref)
	}
	for _, r := range merged.Roots {
		ref := g.byID[r.ID].Ref
		g.roots[ref] = append(g.roots[ref], r.Kind)
	}
	return g
}

// candidates is the reference of every symbol the sweep reported dead.
func (g *cgoGraph) candidates() []string {
	found := make([]string, 0, len(g.swept.Candidates))
	for _, c := range g.swept.Candidates {
		found = append(found, g.byID[c.ID].Ref)
	}
	return found
}

// The four cases below share one load, because the load is what the fixture costs
// and the four read one graph of it.
func TestTheOpaqueCPassGivesThePassesTheGoOfAFileImportingC(t *testing.T) {
	g := sweepCgo(t)

	t.Run("the declarations the check read", func(t *testing.T) { checkCgoSymbols(t, g) })
	t.Run("the references those files make", func(t *testing.T) { checkCgoReferences(t, g) })
	t.Run("what the sweep reports dead", func(t *testing.T) { checkCgoSweep(t, g) })
	t.Run("the roots those files declare", func(t *testing.T) { checkCgoRoots(t, g) })
}

// checkCgoSymbols reads what the enumeration holds of the files the check read.
func checkCgoSymbols(t *testing.T, g *cgoGraph) {
	t.Helper()

	held := g.byRef
	// One declaration per source site of every file the check read, the file
	// itself included, and a C declaration is no declaration of anything.
	for _, ref := range []string{
		cgoModule + "/called#viaC",
		cgoModule + "/uncalled#viaC",
		cgoModule + "/ctype#viaCType",
		cgoModule + "/ctype#Holder",
		cgoModule + "/ctype#Holder.Text",
		cgoModule + "/lone#viaC",
	} {
		if !held[ref] {
			t.Errorf("Symbols(%s) holds no %s, want the declaration of a file the check read", cgoArchive, ref)
		}
	}
	for _, name := range []string{"called/bridge.go", "uncalled/bridge.go", "ctype/bridge.go", "lone/bridge.go"} {
		if !g.files[name] {
			t.Errorf("Symbols(%s) holds no file symbol for %s, want one", cgoArchive, name)
		}
	}
	for ref := range held {
		if strings.HasPrefix(ref, "go://C") || strings.Contains(ref, "#C.") {
			t.Errorf("Symbols(%s) holds %s, want nothing of the C half", cgoArchive, ref)
		}
	}
	// The one file the check could not read is the declared limit, and the
	// declarations it carries are not in the inventory.
	if want := []string{"broken/bridge.go"}; !slices.Equal(g.excluded, want) {
		t.Errorf("Load(%s).ExcludedByCgo = %v, want %v", cgoArchive, g.excluded, want)
	}
	if held[cgoModule+"/broken#viaC"] {
		t.Errorf("Symbols(%s) holds %s, want it absent: the file it is written in falls back",
			cgoArchive, cgoModule+"/broken#viaC")
	}
}

// checkCgoReferences reads the references the files the check read make.
func checkCgoReferences(t *testing.T, g *cgoGraph) {
	t.Helper()

	cases := map[string]struct {
		symbol string
		want   []string
	}{
		"a helper the file importing \"C\" calls": {
			symbol: cgoModule + "/called#helper",
			want:   []string{cgoModule + "/called#viaC"},
		},
		"a helper nothing calls": {
			symbol: cgoModule + "/uncalled#helper",
			want:   nil,
		},
		"a helper the file that falls back calls": {
			symbol: cgoModule + "/broken#helper",
			want:   nil,
		},
		"a field a file importing \"C\" reads": {
			symbol: cgoModule + "/ctype#Holder.Text",
			want:   []string{cgoModule + "/ctype#viaCType", cgoModule + "/ctype#viaCType"},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := g.references[test.symbol]; !slices.Equal(got, test.want) {
				t.Errorf("References(%s) to %s = %v, want %v", cgoArchive, test.symbol, got, test.want)
			}
		})
	}
}

// checkCgoSweep reads which symbols the sweep of that graph reports dead.
func checkCgoSweep(t *testing.T, g *cgoGraph) {
	t.Helper()
	dead := g.candidates()

	cases := map[string]struct {
		symbol string
		want   bool
	}{
		"the helper the file importing \"C\" calls":      {symbol: cgoModule + "/called#helper", want: false},
		"the helper no file calls":                       {symbol: cgoModule + "/uncalled#helper", want: true},
		"the helper only the file that falls back calls": {symbol: cgoModule + "/broken#helper", want: true},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := slices.Contains(dead, test.symbol); got != test.want {
				t.Errorf("Sweep(%s) reports %s dead = %t, want %t (dead: %v)",
					cgoArchive, test.symbol, got, test.want, dead)
			}
		})
	}
}

// checkCgoRoots reads the roots the files the check read declare.
func checkCgoRoots(t *testing.T, g *cgoGraph) {
	t.Helper()

	cases := map[string]struct {
		symbol string
		want   []RootKind
	}{
		// The directive is read from the comment map of a file the opaque-C check
		// type-checked, so the class reaches the declaration it names.
		"a function under a directive in a file importing \"C\"": {
			symbol: cgoModule + "/called#viaC",
			want:   []RootKind{RootCgoExport},
		},
		"the same in the package whose file calls nothing": {
			symbol: cgoModule + "/uncalled#viaC",
			want:   []RootKind{RootCgoExport},
		},
		// A declaration of such a file is a candidate for every other class the way
		// a declaration of a compiled file is.
		"an exported type declared in a file importing \"C\"": {
			symbol: cgoModule + "/ctype#Holder",
			want:   []RootKind{RootPublishedAPI},
		},
		"an unexported function under no directive": {
			symbol: cgoModule + "/ctype#viaCType",
			want:   nil,
		},
		// The file that falls back is outside the analysis, so nothing it declares
		// is a root of anything.
		"a function of the file that falls back": {
			symbol: cgoModule + "/broken#viaC",
			want:   nil,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := g.roots[test.symbol]; !slices.Equal(got, test.want) {
				t.Errorf("Roots(%s)[%s] = %v, want %v", cgoArchive, test.symbol, got, test.want)
			}
		})
	}
}

package exempt

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	"pgregory.net/rapid"
)

// methodNames is the closed set of names a drawn type declares and a drawn
// interface requires. Every one of them has the same signature, so satisfaction
// turns on the set of names alone and the oracle is the drawn sets.
func methodNames() []string { return []string{"Alpha", "Beta", "Gamma"} }

// drawnProgram is one program as it was drawn: the method set each interface
// requires, the method set each type declares, and the conversions the program
// writes, each naming a type and the interface a value of it reaches.
type drawnProgram struct {
	interfaces  [][]string
	types       [][]string
	conversions [][2]int
}

// drawnMethodSet draws a set of method names of at least least methods, in the
// pool's own order so the rendering of one draw is one program.
func drawnMethodSet(least int) *rapid.Generator[[]string] {
	return rapid.Custom(func(t *rapid.T) []string {
		var drawn []string
		for _, name := range methodNames() {
			if rapid.Bool().Draw(t, "the set declares "+name) {
				drawn = append(drawn, name)
			}
		}
		if len(drawn) < least {
			drawn = methodNames()[:least]
		}
		return drawn
	})
}

// drawnPrograms draws one to three interfaces, one to three concrete types and a
// subset of the conversions the drawn sets make legal. A conversion is legal only
// where the type declares every method the interface requires, because a program
// that writes an illegal one does not compile and a property over generated source
// only reasons about programs the toolchain accepts.
func drawnPrograms() *rapid.Generator[drawnProgram] {
	return rapid.Custom(func(t *rapid.T) drawnProgram {
		p := drawnProgram{
			interfaces: rapid.SliceOfN(drawnMethodSet(1), 1, 3).Draw(t, "the interfaces"),
			types:      rapid.SliceOfN(drawnMethodSet(0), 1, 3).Draw(t, "the types"),
		}
		for typ := range p.types {
			for iface := range p.interfaces {
				if !answers(p.types[typ], p.interfaces[iface]) {
					continue
				}
				if rapid.Bool().Draw(t, fmt.Sprintf("T%02d reaches I%02d", typ, iface)) {
					p.conversions = append(p.conversions, [2]int{typ, iface})
				}
			}
		}
		return p
	})
}

// answers reports whether a type declaring declared satisfies an interface
// requiring required.
func answers(declared, required []string) bool {
	for _, name := range required {
		if !slices.Contains(declared, name) {
			return false
		}
	}
	return true
}

// render writes the drawn program as one Go source file.
func (p drawnProgram) render() string {
	var b strings.Builder
	b.WriteString("// Package target holds drawn interfaces, drawn types and the conversions between them.\npackage target\n")
	for i, required := range p.interfaces {
		fmt.Fprintf(&b, "\n// I%02d requires %s.\ntype I%02d interface {\n", i, strings.Join(required, ", "), i)
		for _, name := range required {
			fmt.Fprintf(&b, "\t%s() int\n", name)
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "\n// Reach%02d holds a parameter of I%02d.\nfunc Reach%02d(_ I%02d) int { return %d }\n", i, i, i, i, i)
	}
	for i, declared := range p.types {
		fmt.Fprintf(&b, "\n// T%02d declares %s.\ntype T%02d struct{}\n", i, strings.Join(declared, ", "), i)
		for _, name := range declared {
			fmt.Fprintf(&b, "\n// %s answers an interface requiring it.\nfunc (T%02d) %s() int { return %d }\n", name, i, name, i)
		}
	}
	for i, c := range p.conversions {
		fmt.Fprintf(&b, "\n// Site%02d passes a T%02d into an I%02d parameter.\nfunc Site%02d() int { return Reach%02d(T%02d{}) }\n",
			i, c[0], c[1], i, c[1], c[0])
	}
	return b.String()
}

// oracle is the retained set the drawn sets alone decide: for every conversion the
// program writes, the method of the converted type answering each method the
// interface requires. It is computed from the draw and never from the loaded
// program, so it is not a second copy of the detection.
func (p drawnProgram) oracle() map[string]bool {
	retained := make(map[string]bool)
	for _, c := range p.conversions {
		for _, name := range p.interfaces[c[1]] {
			retained[fmt.Sprintf("T%02d.%s", c[0], name)] = true
		}
	}
	return retained
}

// declaredMethods names every method the drawn program declares, which is the
// population the property's if-and-only-if is stated over.
func (p drawnProgram) declaredMethods() map[string]bool {
	declared := make(map[string]bool)
	for i, names := range p.types {
		for _, name := range names {
			declared[fmt.Sprintf("T%02d.%s", i, name)] = true
		}
	}
	return declared
}

// writeProgram writes one rendered program as a module in a directory of its own,
// removed when the iteration that drew it ends.
func writeProgram(t *rapid.T, source string) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "deadset-exempt-property")
	if err != nil {
		t.Fatalf("Setup: create a module directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for name, data := range map[string]string{
		"go.mod":    "module example.test/target\n\ngo 1.27.1\n",
		"target.go": source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
	}
	return dir
}

// inputUnder loads one written program and assembles the input the class reads.
func inputUnder(t *rapid.T, dir string) *Input {
	t.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	result, err := load.Load(t.Context(), doc, load.HostConfiguration())
	if err != nil {
		t.Fatalf("Setup: load.Load(%s): %v", dir, err)
	}
	symbols, err := graph.Symbols(&result, doc.Target.Path, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", dir, err)
	}
	resolve, err := graph.NewResolver(&result, doc.Target.Path, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", dir, err)
	}
	return &Input{
		Result: &result, Symbols: symbols, Resolve: resolve,
		Root: doc.Target.Path, Read: os.ReadFile,
	}
}

// retainedMethods names the methods one exemption set retains, spelled the way the
// oracle spells them.
func retainedMethods(in *Input, held []graph.Exemption) map[string]bool {
	byID := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		if in.Symbols[i].Kind == graph.KindMethod {
			byID[in.Symbols[i].ID] = strings.TrimPrefix(in.Symbols[i].Ref, "go://example.test/target#")
		}
	}
	retained := make(map[string]bool)
	for i := range held {
		if name, ok := byID[held[i].ID]; ok {
			retained[name] = true
		}
	}
	return retained
}

// Property dead-code-suite/P5: for any set of concrete types, interfaces and
// conversion sites, a method is retained by the interface-satisfaction class if and
// only if a value of its receiver's type reaches an interface the method helps
// satisfy.
//
// The subject is what the type checker answers about one real program, so each
// iteration writes the drawn program to a module of its own and loads it. That
// costs about one second under the race detector, which is why the drawn shapes
// are small: at most three interfaces, three types and the conversions between
// them.
//
// This runs at rapid's default of 100 checks; -rapid.checks raises it for a deeper
// local run.
func TestProperty05InterfaceSatisfactionRetainsExactlyTheSatisfyingMethods(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := drawnPrograms().Draw(t, "the program")
		source := p.render()
		in := inputUnder(t, writeProgram(t, source))

		held, err := InterfaceSatisfactionDetector(in)
		if err != nil {
			t.Fatalf("InterfaceSatisfactionDetector over the drawn program error: %v\n%s", err, source)
		}

		got, want := retainedMethods(in, held), p.oracle()
		for name := range p.declaredMethods() {
			if got[name] == want[name] {
				continue
			}
			verb := "retained"
			if want[name] {
				verb = "did not retain"
			}
			t.Fatalf("InterfaceSatisfactionDetector %s %s; retained %v, want %v\n%s",
				verb, name, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)), source)
		}
		for name := range got {
			if !p.declaredMethods()[name] {
				t.Fatalf("InterfaceSatisfactionDetector retained %s, which the program does not declare\n%s", name, source)
			}
		}
	})
}

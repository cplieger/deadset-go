package kinds

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
)

// theConsumer is the consumer identifier every class case of this file names.
const theConsumer = "example.com/consumer"

func TestTheReachabilityClassIsWhatTheRunKnowsAboutTheConsumers(t *testing.T) {
	for name, one := range map[string]struct {
		resolved  config.Config
		consumers Consumers
		want      Class
	}{
		"an application, whose every caller is in the graph": {
			resolved: applicationConfig(),
			want:     Certain,
		},
		"a library whose declared consumer loaded, with the set declared complete": {
			resolved:  libraryConfig(),
			consumers: Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}, Complete: true},
			want:      Certain,
		},
		"a library whose declared consumer loaded, with the set not declared complete": {
			resolved:  libraryConfig(),
			consumers: Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}},
			want:      Certain,
		},
		"a library whose declared consumer did not load": {
			resolved:  libraryConfig(),
			consumers: Consumers{Declared: []string{theConsumer}, Complete: true},
			want:      Probable,
		},
		"a library with no consumer information at all": {
			resolved:  libraryConfig(),
			consumers: Consumers{},
			want:      Possible,
		},
		"a library whose consumer set is declared complete and declares none": {
			resolved:  libraryConfig(),
			consumers: Consumers{Complete: true},
			want:      Possible,
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := one.resolved
			resolved.Consumers.Complete = one.consumers.Complete
			in := inputOf(t, "class-visibility.txtar", resolved, one.consumers)

			result := computed(t, in, declarationEmitters())

			found := findingOf(t, result.Findings, unusedMemberCode, "Counter.Stale")
			if found.Class != one.want {
				t.Errorf("the pass reports the class %q for an exported field of %s, want %q",
					found.Class, name, one.want)
			}
			if found.Confidence != one.want {
				t.Errorf("the pass reports the confidence %q, want %q: every shipped kind's ceiling is certain",
					found.Confidence, one.want)
			}
			if !slices.Equal(found.ConsumersLoaded, one.consumers.Loaded) && len(one.consumers.Loaded) > 0 {
				t.Errorf("the pass names the loaded consumers %v, want %v",
					found.ConsumersLoaded, one.consumers.Loaded)
			}
		})
	}
}

func TestEveryFindingNamesTheConsumersTheRunLoaded(t *testing.T) {
	consumers := Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}, Complete: true}
	resolved := libraryConfig()
	resolved.Consumers.Complete = true
	in := inputOf(t, "class-visibility.txtar", resolved, consumers)

	result := computed(t, in, declarationEmitters())

	if len(result.Findings) == 0 {
		t.Fatal("the pass reports nothing over the class fixture, so this test pins nothing")
	}
	for i := range result.Findings {
		if !slices.Equal(result.Findings[i].ConsumersLoaded, []string{theConsumer}) {
			t.Errorf("the pass reports %s about %s naming the loaded consumers %v, want [%s]",
				result.Findings[i].Code, result.Findings[i].Symbol.Name,
				result.Findings[i].ConsumersLoaded, theConsumer)
		}
	}
}

func TestTheClassOfADeclarationNoConsumerCanReachIsCertainWithoutAnyConsumerInformation(t *testing.T) {
	for name, one := range map[string]struct {
		id   graph.SymbolID
		want Class
	}{
		"an exported declaration of an internal package":   {id: internalID, want: Certain},
		"an unexported declaration":                        {id: helperID, want: Certain},
		"an exported declaration of an importable package": {id: exportedID, want: Possible},
	} {
		t.Run(name, func(t *testing.T) {
			in := handInput(libraryConfig())

			if got := in.ClassOf(one.id); got != one.want {
				t.Errorf("ClassOf(%s) = %q for %s of a library with no consumer information, want %q",
					one.id, got, name, one.want)
			}
		})
	}
}

// A type parameter of an exported function of a library's importable surface is
// certain with no consumer information at all, because a caller supplies a type
// argument by position and so no reference to the parameter can exist outside the
// declaration that introduces it. The function that declares it is possible under
// the same run, which is what tells the two apart.
func TestTheClassOfATypeParameterOfAFunctionIsCertainWithNoConsumerInformation(t *testing.T) {
	in := inputOf(t, "precedence-importable.txtar", libraryConfig(), Consumers{})

	result := computed(t, in, packageEmitters())
	found := findingOf(t, result.Findings, typeParameterCode, "Convert[T]")
	if found.Class != Certain || found.Confidence != Certain {
		t.Errorf("the pass reports the type parameter of an exported function of a library with the class %q and the confidence %q, want %q for both",
			found.Class, found.Confidence, Certain)
	}

	container := narrowedIDOf(t, in, "go://example.com/app/api#Convert")
	if got := in.ClassOf(container); got != Possible {
		t.Errorf("ClassOf(the function that declares the type parameter) = %q, want %q: the type parameter's class is the parameter's own rule and not its container's",
			got, Possible)
	}
}

// The corpus fixture whose consumer section the run loads states the class of a
// published declaration of a library: certain, because every consumer the scope
// declared loaded, and the fixture declares no complete consumer set. It is the
// fixture this rule was got backwards against, so it is pinned here against the
// published expectation rather than against a local one.
func TestTheCorpusConversionFixtureIsCertainWithItsConsumerLoaded(t *testing.T) {
	const fixture = "interface-satisfaction-conversion"
	in := corpusInput(t, fixture, libraryConfig())
	if in.Consumers.Complete {
		t.Fatal("Setup: the fixture's run declares the consumer set complete, so this test would pin the old rule")
	}
	if !slices.Equal(in.Consumers.Loaded, []string{"example.com/consumer"}) {
		t.Fatalf("Setup: the run loaded the consumers %v, want the fixture's own consumer module",
			in.Consumers.Loaded)
	}

	dead := narrowedIDOf(t, in, "go://example.com/target#DeadExport")
	if got := in.ClassOf(dead); got != Certain {
		t.Errorf("ClassOf(%s) = %q with the fixture's consumer loaded and the set not declared complete, want %q",
			"go://example.com/target#DeadExport", got, Certain)
	}

	// The expectation's row for the same declaration is a finding: the
	// unreferenced-exported kind reports over a published API whose declared
	// consumers all loaded, which is the fixture's run, and the completeness
	// declaration the fixture does not carry decides nothing about it.
	result := computed(t, in, packageEmitters())
	found := findingOf(t, result.Findings, unusedExportedCode, "DeadExport")
	if found.Class != Certain || found.Confidence != Certain {
		t.Errorf("the pass reports %s with the class %q and the confidence %q, want %q for both",
			found.Symbol.Ref, found.Class, found.Confidence, Certain)
	}
	if !slices.Equal(found.ConsumersLoaded, []string{"example.com/consumer"}) {
		t.Errorf("the pass reports %s naming the loaded consumers %v, want the fixture's own consumer module",
			found.Symbol.Ref, found.ConsumersLoaded)
	}
}

func TestTheClosedWorldFactAndTheConsumerInformationFactAreSeparate(t *testing.T) {
	const otherConsumer = "example.com/other"

	for name, one := range map[string]struct {
		consumers   Consumers
		closedWorld bool // what the narrowing kinds read
		allLoaded   bool // what the severity map reads
	}{
		"no consumer declared, and nothing asserted about the set": {
			consumers: Consumers{},
		},
		"a declared consumer that loaded, with the set not declared complete": {
			consumers: Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}},
			allLoaded: true,
		},
		"a declared consumer that loaded, with the set declared complete": {
			consumers:   Consumers{Declared: []string{theConsumer}, Loaded: []string{theConsumer}, Complete: true},
			closedWorld: true,
			allLoaded:   true,
		},
		"a declared consumer that did not load": {
			consumers: Consumers{Declared: []string{theConsumer}},
		},
		"a declared consumer that did not load, with the set declared complete": {
			consumers: Consumers{Declared: []string{theConsumer}, Complete: true},
		},
		"one of two declared consumers loaded, with the set declared complete": {
			consumers: Consumers{
				Declared: []string{theConsumer, otherConsumer},
				Loaded:   []string{theConsumer},
				Complete: true,
			},
		},
		"a set declared complete that declares no consumer": {
			consumers:   Consumers{Complete: true},
			closedWorld: true,
			allLoaded:   true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := &Input{Consumers: one.consumers}

			if got := in.consumersLoaded(); got != one.closedWorld {
				t.Errorf("consumersLoaded() = %t for %s, want %t: the narrowing kinds need the completeness declaration",
					got, name, one.closedWorld)
			}
			if got := in.consumersAllLoaded(); got != one.allLoaded {
				t.Errorf("consumersAllLoaded() = %t for %s, want %t: the severity map reads what the run loaded",
					got, name, one.allLoaded)
			}
		})
	}
}

// The completeness declaration is the claim that no reference exists outside the
// loaded graph, which two preconditions rest on and no other kind does: the narrowing
// kinds' closed world, and the same precondition applied to a signature for the
// unused-result kind. A third reader is how the declaration reached a kind family it
// does not govern once already: what a body never reads is decided by the body, and
// no caller can change it.
func TestTheCompletenessDeclarationHasTwoReaders(t *testing.T) {
	want := []string{"intrafunc.go:callersUnknown", "narrowing.go:closed"}

	if got := callersOf(t, "consumersLoaded"); !slices.Equal(got, want) {
		t.Errorf("consumersLoaded is called by %v, want %v: completeness is the claim that no reference exists outside the graph, and freeSignature does not rest on one",
			got, want)
	}
}

// callersOf names every function of this package, its tests excluded, that calls the
// method named, as file and function, sorted and once each. The declaration itself is
// no call, and neither is a doc comment naming it.
func callersOf(t *testing.T, method string) []string {
	t.Helper()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Setup: list the package's own source: %v", err)
	}

	var held []string
	set := token.NewFileSet()
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, declares := decl.(*ast.FuncDecl)
			if !declares || fn.Body == nil {
				continue
			}
			if calls(fn.Body, method) {
				held = append(held, path+":"+fn.Name.Name)
			}
		}
	}
	slices.Sort(held)
	return slices.Compact(held)
}

// calls reports whether one body selects and calls the method named.
func calls(body *ast.BlockStmt, method string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		if selector, selects := call.Fun.(*ast.SelectorExpr); selects && selector.Sel.Name == method {
			found = true
		}
		return !found
	})
	return found
}

func TestTheClassOfADeclarationOfAMainPackageIsCertain(t *testing.T) {
	in := inputOf(t, "declarations-unused.txtar", libraryConfig(), Consumers{})

	found := false
	for id, ref := range in.Refs {
		if ref != "go://example.com/app#main" {
			continue
		}
		found = true
		if got := in.ClassOf(id); got != Certain {
			t.Errorf("ClassOf(%s) = %q for a declaration of a main package, want %q", ref, got, Certain)
		}
	}
	if !found {
		t.Fatal("the fixture's inventory holds no declaration of its main package, so this test pins nothing")
	}
}

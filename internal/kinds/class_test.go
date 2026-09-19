package kinds

import (
	"slices"
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

	// The expectation's row for the same declaration is a finding, and the
	// unreferenced-exported kind is allowed for a published API whose consumer set
	// is not declared complete, so the row needs the assertion the corpus format
	// cannot yet carry. Its class is the one above either way.
	complete := libraryConfig()
	complete.Consumers.Complete = true
	declared := corpusInput(t, fixture, complete)
	result := computed(t, declared, packageEmitters())
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

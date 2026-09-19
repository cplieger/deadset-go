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

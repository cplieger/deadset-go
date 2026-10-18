package kinds

import (
	"go/token"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"pgregory.net/rapid"
)

// drawnInventory draws one inventory of candidates: a few declarations, each in a
// package of a shape the class rule distinguishes and either exported or not.
func drawnInventory(t *rapid.T) []inventory.Symbol {
	packages := []string{
		"example.com/app/pub",
		"example.com/app/internal/store",
		"example.com/app/pub_test",
	}
	count := rapid.IntRange(1, 6).Draw(t, "the number of declarations")
	symbols := make([]inventory.Symbol, count)
	for i := range count {
		name := "Decl" + strconv.Itoa(i)
		if !rapid.Bool().Draw(t, "declaration "+strconv.Itoa(i)+" is exported") {
			name = "decl" + strconv.Itoa(i)
		}
		pkgPath := rapid.SampledFrom(packages).Draw(t, "the package of declaration "+strconv.Itoa(i))
		line := 10 + i
		symbols[i] = inventory.Symbol{
			ID:       inventory.SymbolID("catalog.go:" + strconv.Itoa(line) + ":6"),
			Ref:      "go://" + pkgPath + "#" + name,
			Name:     name,
			PkgPath:  pkgPath,
			Pos:      token.Position{Filename: "catalog.go", Line: line, Column: 6},
			EndLine:  line,
			Kind:     inventory.KindField,
			Exported: name[0] >= 'A' && name[0] <= 'Z',
			Configs:  inventory.ConfigSet(0b1),
		}
	}
	return symbols
}

// drawnConsumers draws one consumer set: what the scope declared, which of those
// loaded, and whether the configuration declares the set complete.
func drawnConsumers(t *rapid.T, label string) Consumers {
	declared := rapid.SliceOfNDistinct(
		rapid.SampledFrom([]string{"example.com/one", "example.com/two"}),
		0, 2, rapid.ID,
	).Draw(t, label+": the declared consumers")
	loaded := declared
	if len(declared) > 0 && rapid.Bool().Draw(t, label+": a declared consumer did not load") {
		loaded = declared[:len(declared)-1]
	}
	return Consumers{
		Declared: declared,
		Loaded:   loaded,
		Complete: rapid.Bool().Draw(t, label+": the set is declared complete"),
	}
}

// drawnSeverities draws one severity map that names the observed kind explicitly, so
// the run's consumer knowledge cannot move it: a kind the configuration does not
// name resolves to the Contract's default, and one of those defaults is the one the
// consumer model turns off.
func drawnSeverities(t *rapid.T, label string) map[string]vocab.Severity {
	severities := []vocab.Severity{vocab.Warn, vocab.Deny}
	named := map[string]vocab.Severity{
		string(vocab.UnusedMember): rapid.SampledFrom(severities).Draw(t, label+": the severity of the observed kind"),
	}
	for _, code := range []vocab.Code{vocab.UnusedExported, vocab.UnusedUnexported, vocab.TestOnlyUse} {
		if rapid.Bool().Draw(t, label+": the configuration names "+string(code)) {
			named[string(code)] = rapid.SampledFrom(append(severities, vocab.Allow)).
				Draw(t, label+": the severity of "+string(code))
		}
	}
	return named
}

// propInput is the input one drawn inventory, configuration and consumer set make,
// with every declaration a candidate so every one of them is reported.
func propInput(symbols []inventory.Symbol, resolved config.Config, consumers Consumers) *Input {
	candidates := make([]liveness.Candidate, len(symbols))
	refs := make(map[inventory.SymbolID]string, len(symbols))
	for i := range symbols {
		candidates[i] = liveness.Candidate{
			ID: symbols[i].ID, Relation: liveness.ReferenceCounting, Configs: inventory.ConfigSet(0b1),
		}
		refs[symbols[i].ID] = symbols[i].Ref
	}
	held := make([]inventory.Symbol, len(symbols))
	copy(held, symbols)
	return &Input{
		Config:    &resolved,
		Merged:    &liveness.Merged{Symbols: held, Configurations: 1},
		Sweep:     &liveness.Result{Candidates: candidates},
		Refs:      refs,
		Matrix:    []string{"linux-amd64"},
		Consumers: consumers,
	}
}

// everyCandidate is the emitter of the observed kind: it reports every candidate, so
// a drawn inventory produces one finding per declaration whatever the class and the
// severity are.
func everyCandidate(in *Input) ([]Finding, error) {
	var found []Finding
	for i := range in.Sweep.Candidates {
		one, held := in.finding(in.Sweep.Candidates[i].ID, vocab.UnusedMember,
			"the field has no reference in the target")
		if !held {
			continue
		}
		found = append(found, one)
	}
	return found, nil
}

// classes and severities are the two dials of one pass, keyed by declaration.
func dials(t *rapid.T, in *Input) (map[string]vocab.Class, map[string]vocab.Severity) {
	result, err := Compute(in, map[vocab.Code]Emitter{vocab.UnusedMember: everyCandidate})
	if err != nil {
		t.Fatalf("Compute() = error %v, want the findings of the pass", err)
	}
	classes := make(map[string]vocab.Class, len(result.Findings))
	severities := make(map[string]vocab.Severity, len(result.Findings))
	for i := range result.Findings {
		classes[result.Findings[i].Symbol.Ref] = result.Findings[i].Class
		severities[result.Findings[i].Symbol.Ref] = result.Findings[i].Severity
	}
	return classes, severities
}

// TestTheClassAndTheSeverityAreIndependentDials is property dead-code-suite/P13:
// changing the severity map or the assertion that the consumer set is complete
// changes no finding's reachability class, and changing the consumer set or the
// target kind changes no finding's severity.
//
// Completeness is on the severity side of the property because it is what opens the
// narrowing kinds on a published API: what the class reads is which declared
// consumers the run loaded, and an assertion about the consumers it did not load
// moves neither.
//
// The severity map the property draws names the observed kind explicitly, which is
// what the second half is about: the severity comes from the configuration. The
// minimum confidence is possible, so no finding is withheld by the class it carries.
func TestTheClassAndTheSeverityAreIndependentDials(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		symbols := drawnInventory(t)
		consumers := drawnConsumers(t, "the first run")
		kind := report.TargetKind(rapid.SampledFrom([]string{
			string(report.TargetKindApplication), string(report.TargetKindLibrary),
		}).Draw(t, "the target kind"))

		first := defaultConfig()
		first.Target.Kind = kind
		first.Analysis.MinConfidence = vocab.Possible
		first.Consumers.Complete = consumers.Complete
		first.Severity = drawnSeverities(t, "the first run")
		classes, severities := dials(t, propInput(symbols, first, consumers))

		// The severity map moves, and so does the assertion that the consumer set
		// is complete, which is a dial of the severity and not of the class.
		second := first
		second.Severity = drawnSeverities(t, "the second run")
		otherCompleteness := consumers
		otherCompleteness.Complete = !consumers.Complete
		second.Consumers.Complete = otherCompleteness.Complete
		movedSeverities, _ := dials(t, propInput(symbols, second, otherCompleteness))
		for ref, class := range classes {
			if got := movedSeverities[ref]; got != class {
				t.Fatalf("the class of %s is %q under one severity map and %q under another, want the same class",
					ref, class, got)
			}
		}

		// The consumer set and the target kind move, and the severity map does not.
		third := first
		third.Target.Kind = report.TargetKind(rapid.SampledFrom([]string{
			string(report.TargetKindApplication), string(report.TargetKindLibrary),
		}).Draw(t, "the target kind of the third run"))
		otherConsumers := drawnConsumers(t, "the third run")
		third.Consumers.Complete = otherConsumers.Complete
		_, movedClasses := dials(t, propInput(symbols, third, otherConsumers))
		for ref, severity := range severities {
			if got := movedClasses[ref]; got != severity {
				t.Fatalf("the severity of %s is %q under one consumer set and target kind and %q under another, want the same severity",
					ref, severity, got)
			}
		}
	})
}

package findings

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// leftOutAnswer is how one kind answers for a package the run leaves out that may
// name any export of the target ([pass.leftOutReaches]).
type leftOutAnswer int

const (
	// lowered: the kind reports from the reachability class, possible for such an
	// export ([pass.classOf]).
	lowered leftOutAnswer = iota + 1

	// withheld: the kind decides from the references or call sites it sees, so
	// it reports no such export ([pass.closed], [intrafunc.callersUnknown]).
	withheld

	// imported: every package such a package imports counts as imported.
	imported

	// required: every module such a package imports stays required, which the
	// deps package and the command's left-out rows answer.
	required

	// unrelated: the kind decides from a function body, a declaration's own text,
	// the build matrix or a configuration document, which no caller changes.
	unrelated
)

// leftOutAnswers is every Go kind with an emitter and how it answers for a
// left-out package. A kind added to [Emitters] has to say which.
var leftOutAnswers = map[vocab.Code]leftOutAnswer{
	vocab.UnusedExported:              lowered,
	vocab.UnusedUnexported:            lowered,
	vocab.UnusedMember:                lowered,
	vocab.TestOnlyUse:                 lowered,
	vocab.TestOfDeadCode:              lowered,
	vocab.DeprecatedUnused:            lowered,
	vocab.UnreachableExport:           lowered,
	vocab.UnusedInterface:             lowered,
	vocab.UncalledInterfaceMethod:     lowered,
	vocab.UnusedSatisfactionAssertion: lowered,
	vocab.WriteOnlySymbol:             lowered,
	vocab.UnusedEnumMember:            lowered,
	vocab.UnnecessaryExport:           withheld,
	vocab.UnnecessaryExposure:         withheld,
	vocab.UnusedResult:                withheld,
	vocab.FileNeverImported:           imported,
	vocab.UnusedDependency:            required,
	vocab.UnusedModuleDirective:       required,
	vocab.UnusedTypeParameter:         unrelated, // the declaration's own text
	vocab.UnusedParameter:             unrelated, // the body: no caller makes it read a parameter
	vocab.UnusedReceiver:              unrelated, // the body
	vocab.UnreachableStatement:        unrelated, // the body
	vocab.DeadStore:                   unrelated, // the body
	vocab.UnreachableCase:             unrelated, // the body
	vocab.FileNeverBuilt:              unrelated, // the build matrix
	vocab.SuppressionWithoutReason:    unrelated, // a suppression record
	vocab.UnscopedIgnoreEntry:         unrelated, // a suppression record
	vocab.UnmatchedRoot:               unrelated, // the configuration document
}

func TestEveryKindSaysHowItAnswersForALeftOutPackage(t *testing.T) {
	t.Parallel()
	for code := range Emitters {
		if leftOutAnswers[code] == 0 {
			t.Errorf("%s has an emitter and does not say how it answers for a package the run leaves out", code)
		}
	}
	for code := range leftOutAnswers {
		if _, wired := Emitters[code]; !wired {
			t.Errorf("%s says how it answers for a left-out package and has no emitter", code)
		}
	}
}

// Where a package the run leaves out imports every package of the target, every
// kind answers for it as [leftOutAnswers] says: no finding about an export is
// certain or probable, a kind deciding from what it sees reports no export, and no
// file is never imported. Each fixture is analyzed as an application with and
// without that package, and each answer must have something to answer for
// without it.
func TestEveryKindAnswersForTheCallersOfALeftOutPackage(t *testing.T) {
	t.Parallel()
	measured := make(map[leftOutAnswer]int)
	for _, archive := range fixtures(t) {
		shared := analysisOf(t, archive, asApplication, Consumers{})
		if shared.leftOutRefused != nil {
			t.Fatalf("Compute(%s beside a left-out package) = error %v, want the findings of the pass", archive, shared.leftOutRefused)
		}
		for _, found := range slices.Concat(shared.leftOut.Findings, shared.leftOut.Withheld) {
			answer, export := leftOutAnswers[found.Code], shared.exports[declarationOf(&found)]
			switch {
			case answer == lowered && export && found.Confidence != vocab.Possible:
				t.Errorf("%s: %s about %s is %s beside a left-out package that may name it, want possible", archive, found.Code, found.Symbol.Ref, found.Confidence)
			case answer == withheld && export:
				t.Errorf("%s: %s reports %s, which a left-out package may name, want no finding", archive, found.Code, found.Symbol.Ref)
			case answer == imported:
				t.Errorf("%s: %s reports %s, which a left-out package imports, want no finding", archive, found.Code, found.Symbol.Ref)
			}
		}
		without := shared.findings(t, everyKind)
		for _, found := range slices.Concat(without.Findings, without.Withheld) {
			if shared.exports[declarationOf(&found)] || leftOutAnswers[found.Code] == imported {
				measured[leftOutAnswers[found.Code]]++
			}
		}
	}
	for _, answer := range []leftOutAnswer{lowered, withheld, imported} {
		if measured[answer] == 0 {
			t.Errorf("no fixture reports an export under a kind answering %d without the left-out package, so that answer is measured over nothing", answer)
		}
	}
}

// leaveOutEveryPackage is in with each configuration given one left-out package
// that imports every package of the target, sharing nothing it changes with in.
func leaveOutEveryPackage(in *Input) *Input {
	held := *in
	per := slices.Clone(in.Per.(configs))
	for i := range per {
		if per[i].Result == nil {
			continue
		}
		r := *per[i].Result
		var ids []load.PackageID
		for _, p := range r.Packages {
			if id := r.ID(p); len(p.GoFiles) > 0 && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		r.LeftOut = []load.LeftOutPackage{{Dir: "/left-out", Path: "example.com/left-out", Imports: ids, Reaches: ids}}
		per[i].Result = &r
	}
	held.Per = per
	return &held
}

// exportsOf is every exported production declaration of in's inventory.
func exportsOf(in *Input) map[inventory.SymbolID]bool {
	held := make(map[inventory.SymbolID]bool)
	for i := range in.Merged.Symbols {
		if symbol := &in.Merged.Symbols[i]; symbol.Exported && !symbol.InTestFile {
			held[symbol.ID] = true
		}
	}
	return held
}

// declarationOf is the declaration of the inventory a finding is about or is a
// part of.
func declarationOf(found *Finding) inventory.SymbolID {
	if found.id != "" {
		return found.id
	}
	return found.decl
}

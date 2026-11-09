package findings

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/edges"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"pgregory.net/rapid"
)

// The module and file a drawn declaration is written in, which is what a drawn
// reference and a drawn entry's path both spell.
const (
	drawnModulePath = "example.com/app"
	drawnFilePath   = "app.go"
)

// The codes a drawn finding carries, in the two classes a suppression record is held
// by. A kind whose subject the analysis judged DEAD never produces its finding at all
// under a record, because the mark made the subject live before the sweep ran. A kind
// whose subject the analysis holds LIVE produces its finding under a record like any
// other, and the pass is what withholds it, so a document drawing from this class
// alone is one the sweep's answer says nothing about.
var (
	deadSubjectCodes = []vocab.Code{
		vocab.UnusedExported, vocab.UnusedUnexported, vocab.UnusedMember, vocab.UnusedInterface,
	}
	liveSubjectCodes = []vocab.Code{
		vocab.UnnecessaryExport, vocab.WriteOnlySymbol, vocab.UnusedParameter,
	}
)

// The column a drawn part is written at, which is inside the declaration's own line
// and is what gives a finding about a part an identity of its own.
const drawnPartColumn = 20

// drawnFinding is one finding a drawn document records: the code it carries and the
// declaration it is about.
type drawnFinding struct {
	code   vocab.Code
	symbol string
	id     inventory.SymbolID
	line   int
}

// part reports whether this finding is about a part of its declaration rather than
// about the declaration, which is the subject a record reaches through the
// declaration its own reference names.
func (d drawnFinding) part() bool { return d.code == vocab.UnusedParameter }

// finding is this drawn finding as an emitter of its own kind returns it, which is
// what the pass withholds under the record naming it.
func (d drawnFinding) finding() draft {
	at := Position{Path: drawnFilePath, Line: d.line, Column: 1, EndLine: d.line}
	subject := Subject{Ref: d.symbol, Name: "Drawn", SizeLines: 1}
	if d.part() {
		at.Column = drawnPartColumn
		subject.Kind = parameterSubject
		one := findingAt(d.code, subject, at, "drawn part "+subject.Name+" of a declaration the analysis keeps")
		one.decl = d.id
		return one
	}
	return draft{
		Code:     d.code,
		Position: at,
		Symbol:   subject,
		Message:  "drawn subject " + subject.Name + " of a document written by the property",
		id:       d.id,
	}
}

// drawnFindings draws a set of findings about distinct declarations, one per
// declaration, which is what a suppression document is written from.
//
// Both classes are drawn every iteration, so every iteration measures a record on a
// finding the marks change and one on a finding they do not.
func drawnFindings(t *rapid.T) []drawnFinding {
	codes := slices.Concat(
		rapid.SliceOfN(rapid.SampledFrom(deadSubjectCodes), 1, 4).Draw(t, "dead subjects"),
		rapid.SliceOfN(rapid.SampledFrom(liveSubjectCodes), 1, 4).Draw(t, "live subjects"),
	)

	found := make([]drawnFinding, 0, len(codes))
	for i, code := range codes {
		name := "Drawn" + strconv.Itoa(i)
		found = append(found, drawnFinding{
			code:   code,
			symbol: "go://" + drawnModulePath + "#" + name,
			id:     inventory.SymbolID(drawnFilePath + ":" + strconv.Itoa(i+1) + ":1"),
			line:   i + 1,
		})
	}
	return found
}

// drawnEmitters is one emitter per drawn code, each returning the drawn findings of
// that code, beside the stale-suppression kind that reads what the pass withheld.
//
// The kinds are stubbed because the property is about the documents and the
// withholding rather than about any kind's rule: a drawn finding stands for whatever
// its kind would have produced, which is what lets one document draw across the two
// classes at once.
func drawnEmitters(found []drawnFinding) map[vocab.Code]Emitter {
	byCode := make(map[vocab.Code][]draft, len(found))
	for _, one := range found {
		byCode[one.code] = append(byCode[one.code], one.finding())
	}
	table := map[vocab.Code]Emitter{}
	for code, produced := range byCode {
		table[code] = func(*pass) ([]draft, error) { return slices.Clone(produced), nil }
	}
	return table
}

// inventoryOf is the inventory a drawn finding set is about, which is what a
// document's entries and rows bind against.
func inventoryOf(found []drawnFinding) []inventory.Symbol {
	symbols := make([]inventory.Symbol, 0, len(found))
	for i := range found {
		symbols = append(symbols, inventory.Symbol{
			ID:       found[i].id,
			Ref:      found[i].symbol,
			Kind:     inventory.KindFunc,
			Pos:      token.Position{Filename: drawnFilePath, Line: found[i].line, Column: 1},
			File:     drawnFilePath,
			Exported: true,
		})
	}
	return symbols
}

// inputOfRecords is the input a findings pass reads over hand-built records: the
// inventory the records bind against and the records themselves. The stubbed kinds
// read no sweep, so the sweep with the marks withdrawn is the one sweep.
func inputOfRecords(records []core.Record, symbols []inventory.Symbol) *Input {
	resolved := defaultConfig()
	resolved.Target.Kind = report.TargetKindApplication
	return &Input{
		Config: &resolved,
		Merged: &liveness.Merged{Symbols: symbols},
		Sweep:  &liveness.Result{},
		Marks:  records,
	}
}

// TestASuppressionDocumentRoundTrips is property dead-code-suite/P15: writing the
// reported findings into a mechanism's document suppresses exactly those findings on
// the next run and reports no stale entry. The ignore file and the baseline go
// through their own readers; inline records are built from directives directly. The
// findings go through the whole pass, since a record for a kind whose subject is
// live is in effect only where the pass withheld the finding. What a mark seeds (a
// symbol reachable only through a suppressed one stays unreported) is the sweep's
// answer, pinned by this unit's fixture tests.
func TestASuppressionDocumentRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		found := drawnFindings(t)
		symbols := inventoryOf(found)

		for _, mechanism := range []core.Mechanism{
			core.Inline, core.Ignore, core.Baseline,
		} {
			records, refusals := recordsOf(t, dir, mechanism, found, symbols)
			if len(refusals) != 0 {
				t.Fatalf("the %s document of %d findings was refused: %+v", mechanism, len(found), refusals)
			}
			if len(records) != len(found) {
				t.Fatalf("the %s document of %d findings read back as %d records, want one per finding",
					mechanism, len(found), len(records))
			}

			in := inputOfRecords(records, symbols)
			result, err := Compute(in, drawnEmitters(found))
			if err != nil {
				t.Fatalf("Compute() over the %s document = _, %v, want the findings the document leaves",
					mechanism, err)
			}
			if len(result.Findings) != 0 {
				t.Errorf("Compute() over the %s document of %d findings reports %v, want none: the document names every one of them",
					mechanism, len(found), summary(result.Findings))
			}
			inEffect, reasons := result.InEffect, result.Reasons
			if inEffect != len(found) || reasons != len(found) {
				t.Errorf("Totals() over the %s document of %d findings = %d in effect and %d reasons, want %d and %d",
					mechanism, len(found), inEffect, reasons, len(found), len(found))
			}
		}
	})
}

// recordsOf is the records one mechanism's document holds for a drawn finding set.
// The ignore file and the baseline are written and read back; the inline records are
// built directly, one per finding, at the line above the declaration.
func recordsOf(t *rapid.T, dir string, mechanism core.Mechanism, found []drawnFinding,
	symbols []inventory.Symbol,
) ([]core.Record, []core.Refusal) {
	switch mechanism {
	case core.Baseline:
		recorded := make([]core.Recorded, 0, len(found))
		for i := range found {
			recorded = append(recorded, core.Recorded{
				Code: found[i].code, Symbol: found[i].symbol, Path: drawnFilePath,
			})
		}
		var written bytes.Buffer
		identity := core.Provenance{Analyzer: analyzerName, Version: "1.6.0"}
		if err := core.WriteBaseline(&written, recorded, identity, nil); err != nil {
			t.Fatalf("core.WriteBaseline(%d findings) = %v, want the document", len(found), err)
		}
		records, refusals, err := suppress.Baseline(writeTemp(t, dir, core.BaselineFileName, written.String()), symbols)
		if err != nil {
			t.Fatalf("suppress.Baseline(the document just written) = _, _, %v, want its rows", err)
		}
		return records, refusals

	case core.Ignore:
		document := "{\n  \"ignore\": [\n"
		for i := range found {
			if i > 0 {
				document += ",\n"
			}
			document += "    {\"code\": " + strconv.Quote(string(found[i].code)) +
				", \"symbol\": " + strconv.Quote(found[i].symbol) +
				", \"path\": " + strconv.Quote(drawnFilePath) +
				", \"reason\": \"adjudicated by the maintainer\"}"
		}
		document += "\n  ]\n}\n"
		records, refusals, err := suppress.IgnoreFile(writeTemp(t, dir, core.IgnoreFileName, document), symbols)
		if err != nil {
			t.Fatalf("suppress.IgnoreFile(the document just written) = _, _, %v, want its entries", err)
		}
		return records, refusals

	default:
		records := make([]core.Record, 0, len(found))
		for i := range found {
			records = append(records, core.Record{
				Code:      found[i].code,
				Symbol:    found[i].symbol,
				Path:      drawnFilePath,
				Reason:    "adjudicated by the maintainer",
				Bound:     string(found[i].id),
				Site:      core.Site{Path: drawnFilePath, File: drawnFilePath, Line: i + 1, Column: 1},
				Mechanism: core.Inline,
			})
		}
		return records, nil
	}
}

// writeTemp writes one document into dir under name and returns its path. The
// directory is the test's own and is reused across iterations, because each document
// is read as soon as it is written.
func writeTemp(t *rapid.T, dir, name, document string) string {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return path
}

// TestAnythingTheConfigurationNamesThatMatchesNothingIsReported is property
// dead-code-suite/P17: every suppression, configured root, root pattern and declared
// edge side that matches nothing is reported as its own record.
//
// A suppression and a configured string are each a finding; an edge side is an
// evaluation whose state is absent, because an analyzer never reports a stale edge
// and the merge is what does. Every drawn suppression sits at a site of its own, so
// the collapse by site does not fold two of them together and the count is exact.
func TestAnythingTheConfigurationNamesThatMatchesNothingIsReported(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		found := drawnFindings(t)
		configured := rapid.SliceOfN(rapid.SampledFrom([]string{
			"go://" + drawnModulePath + "#Gone",
			"go://" + drawnModulePath + "#Gone*",
			"go://" + drawnModulePath + "#Go?e",
			"go://" + drawnModulePath + "/internal/queue#List.Push",
		}), 0, 4).Draw(t, "roots")

		records := make([]core.Record, 0, len(found))
		declared := &edges.Document{}
		for i := range found {
			records = append(records, core.Record{
				Code:      found[i].code,
				Symbol:    found[i].symbol,
				Path:      drawnFilePath,
				Reason:    "adjudicated by the maintainer",
				Site:      core.Site{Path: core.IgnoreFileName, Line: i + 2, Column: 5},
				Mechanism: core.Ignore,
			})
			declared.Edges = append(declared.Edges, edges.Edge{
				ID:       "wire/drawn" + strconv.Itoa(i),
				Provides: found[i].symbol,
				UsedBy:   "ts://@example/app/src/wire.ts#Drawn" + strconv.Itoa(i),
			})
		}

		// Nothing bound, so nothing was suppressed and nothing is enumerated.
		in := inputOfRecords(records, nil)
		in.Unmatched = make([]roots.Unmatched, 0, len(configured))
		for _, root := range configured {
			in.Unmatched = append(in.Unmatched, roots.Unmatched{Source: root})
		}
		in.Edges = declared

		result, err := Compute(in, nil)
		if err != nil {
			t.Fatalf("Compute() = error %v, want the findings of the pass", err)
		}
		if len(result.Stale) != len(records) {
			t.Errorf("Compute() over %d records that bound nothing reported %d stale, want one each: %+v",
				len(records), len(result.Stale), result.Stale)
		}
		if inEffect, reasons := result.InEffect, result.Reasons; inEffect != 0 || reasons != len(records) {
			t.Errorf("Totals() over %d records that bound nothing = %d in effect and %d reasons, want 0 and %d",
				len(records), inEffect, reasons, len(records))
		}

		unmatched, err := unmatchedRoots(mustPass(t, in))
		if err != nil {
			t.Fatalf("UnmatchedRoots() = _, %v, want every configured string reported", err)
		}
		if len(unmatched) != len(configured) {
			t.Errorf("UnmatchedRoots() over %d configured strings reported %d, want one each: %v",
				len(configured), len(unmatched), draftSummary(unmatched))
		}
		for i := range unmatched {
			if unmatched[i].Symbol.Ref != in.Unmatched[i].Source {
				t.Errorf("UnmatchedRoots()[%d] names %q, want the configured string %q",
					i, unmatched[i].Symbol.Ref, in.Unmatched[i].Source)
			}
		}

		kept, evaluations := mustPass(t, in).evaluate(nil)
		if len(kept) != 0 {
			t.Errorf("Evaluate() over no finding kept %v, want none", summary(kept))
		}
		if len(evaluations) != len(declared.Edges) {
			t.Errorf("Evaluate() published %d records over %d declared edges, want one per own side",
				len(evaluations), len(declared.Edges))
		}
		for i := range evaluations {
			if evaluations[i].State != stateAbsent || evaluations[i].Finding != nil {
				t.Errorf("Evaluate() published %+v, want the absent state and no finding: nothing is enumerated",
					asNamed(evaluations)[i])
			}
		}
		if !slices.IsSortedFunc(evaluations, func(a, b evaluation) int {
			return strings.Compare(a.Edge, b.Edge)
		}) {
			t.Errorf("Evaluate() published %+v, want the records ordered by edge", asNamed(evaluations))
		}
	})
}

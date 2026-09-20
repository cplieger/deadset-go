package kinds

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/edges"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/suppress"
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
	deadSubjectCodes = []string{
		unusedExportedCode, unusedUnexportedCode, unusedMemberCode, unusedInterfaceCode,
	}
	liveSubjectCodes = []string{
		unnecessaryExportCode, writeOnlyCode, unusedParameterCode,
	}
)

// The column a drawn part is written at, which is inside the declaration's own line
// and is what gives a finding about a part an identity of its own.
const drawnPartColumn = 20

// drawnFinding is one finding a drawn document records: the code it carries and the
// declaration it is about.
type drawnFinding struct {
	code   string
	symbol string
	id     graph.SymbolID
	line   int
}

// live reports whether the analysis holds this finding's subject live, which decides
// where the record naming its code is held: the sweep's mark for a dead subject, the
// pass's own withholding for a live one.
func (d drawnFinding) live() bool { return slices.Contains(liveSubjectCodes, d.code) }

// part reports whether this finding is about a part of its declaration rather than
// about the declaration, which is the subject a record reaches through the
// declaration its own reference names.
func (d drawnFinding) part() bool { return d.code == unusedParameterCode }

// finding is this drawn finding as an emitter of its own kind returns it, which is
// what the pass withholds under the record naming it.
func (d drawnFinding) finding() Finding {
	at := Position{Path: drawnFilePath, Line: d.line, Column: 1, EndLine: d.line}
	subject := Subject{Ref: d.symbol, Name: "Drawn", SizeLines: 1}
	if d.part() {
		at.Column = drawnPartColumn
		subject.Kind = parameterSubject
		return findingAt(d.code, subject, at, "drawn part of a declaration the analysis keeps")
	}
	return Finding{
		Code:     d.code,
		Position: at,
		Symbol:   subject,
		Live:     d.live(),
		Message:  "drawn subject of a document written by the property",
		id:       d.id,
	}
}

// drawnFindings draws a set of findings about distinct declarations, one per
// declaration, which is what a suppression document is written from.
//
// Both classes are drawn every iteration, so no iteration measures the mark alone:
// a document holding only dead subjects is one the sweep's Suppressed answer already
// accounts for, and the class the pass has to withhold would go unexercised.
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
			id:     graph.SymbolID(drawnFilePath + ":" + strconv.Itoa(i+1) + ":1"),
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
func drawnEmitters(found []drawnFinding) map[string]Emitter {
	byCode := make(map[string][]Finding, len(found))
	for _, one := range found {
		byCode[one.code] = append(byCode[one.code], one.finding())
	}
	table := map[string]Emitter{staleSuppressionCode: StaleSuppressions}
	for code, produced := range byCode {
		table[code] = func(*Input) ([]Finding, error) { return slices.Clone(produced), nil }
	}
	return table
}

// inventoryOf is the inventory a drawn finding set is about, which is what a
// document's entries and rows bind against.
func inventoryOf(found []drawnFinding) []graph.Symbol {
	symbols := make([]graph.Symbol, 0, len(found))
	for i := range found {
		symbols = append(symbols, graph.Symbol{
			ID:       found[i].id,
			Ref:      found[i].symbol,
			Kind:     graph.KindFunc,
			Pos:      token.Position{Filename: drawnFilePath, Line: found[i].line, Column: 1},
			Exported: true,
		})
	}
	return symbols
}

// inputOfRecords is the input a findings pass reads over hand-built records: the
// inventory the records bind against, the records themselves, and the sweep answer
// naming the declarations a mark held back, which is the dead-subject half of the
// drawn set and nothing else.
func inputOfRecords(records []suppress.Record, symbols []graph.Symbol, suppressed []graph.SymbolID) *Input {
	resolved := config.Default()
	resolved.Target.Kind = config.Application
	return &Input{
		Config: &resolved,
		Merged: &graph.Merged{Symbols: symbols},
		Sweep:  &graph.Result{Suppressed: suppressed},
		Marks:  records,
	}
}

// TestASuppressionDocumentRoundTrips is property dead-code-suite/P15: writing the
// reported findings into a mechanism's document suppresses exactly those findings on
// the next run and reports no stale entry.
//
// Each mechanism is drawn over the same finding set. The ignore file and the
// baseline are written and read back through their own readers, so the property runs
// over the documents rather than over records built beside them; the inline
// mechanism has no document of its own, its records being the source's directives,
// so its records are built directly.
//
// The findings go through the whole pass rather than through the self-check kind
// alone, because a record for a kind whose subject the analysis holds live is in
// effect only where the pass withheld the finding, and the drawn set holds such a
// finding every iteration. What the sweep answers about a mark is the other half and
// is drawn beside it.
//
// The half of the property about what a mark seeds, that a symbol reachable only
// through a suppressed one is unreported and every other finding stays reported, is
// the sweep's answer rather than this pass's, and the fixture tests of this file's
// unit pin it over a loaded module.
func TestASuppressionDocumentRoundTrips(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		found := drawnFindings(t)
		symbols := inventoryOf(found)
		suppressed := make([]graph.SymbolID, 0, len(found))
		for i := range found {
			if !found[i].live() {
				suppressed = append(suppressed, found[i].id)
			}
		}

		for _, mechanism := range []suppress.Mechanism{
			suppress.MechanismInline, suppress.MechanismIgnore, suppress.MechanismBaseline,
		} {
			records, refusals := recordsOf(t, dir, mechanism, found, symbols)
			if len(refusals) != 0 {
				t.Fatalf("the %s document of %d findings was refused: %+v", mechanism, len(found), refusals)
			}
			if len(records) != len(found) {
				t.Fatalf("the %s document of %d findings read back as %d records, want one per finding",
					mechanism, len(found), len(records))
			}

			in := inputOfRecords(records, symbols, suppressed)
			result, err := Compute(in, drawnEmitters(found))
			if err != nil {
				t.Fatalf("Compute() over the %s document = _, %v, want the findings the document leaves",
					mechanism, err)
			}
			if len(result.Findings) != 0 {
				t.Errorf("Compute() over the %s document of %d findings reports %v, want none: the document names every one of them",
					mechanism, len(found), summary(result.Findings))
			}
			inEffect, reasons := Totals(in)
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
func recordsOf(t *rapid.T, dir string, mechanism suppress.Mechanism, found []drawnFinding,
	symbols []graph.Symbol,
) ([]suppress.Record, []suppress.Refusal) {
	switch mechanism {
	case suppress.MechanismBaseline:
		recorded := make([]suppress.Recorded, 0, len(found))
		for i := range found {
			recorded = append(recorded, suppress.Recorded{
				Code: found[i].code, Symbol: found[i].symbol, Path: drawnFilePath,
			})
		}
		var written bytes.Buffer
		identity := suppress.Provenance{Analyzer: analyzerName, Version: "1.6.0"}
		if err := suppress.WriteBaseline(&written, recorded, identity); err != nil {
			t.Fatalf("suppress.WriteBaseline(%d findings) = %v, want the document", len(found), err)
		}
		records, refusals, err := suppress.Baseline(writeTemp(t, dir, suppress.BaselineFileName, written.String()), symbols)
		if err != nil {
			t.Fatalf("suppress.Baseline(the document just written) = _, _, %v, want its rows", err)
		}
		return records, refusals

	case suppress.MechanismIgnore:
		document := "{\n  \"ignore\": [\n"
		for i := range found {
			if i > 0 {
				document += ",\n"
			}
			document += "    {\"code\": " + strconv.Quote(found[i].code) +
				", \"symbol\": " + strconv.Quote(found[i].symbol) +
				", \"path\": " + strconv.Quote(drawnFilePath) +
				", \"reason\": \"adjudicated by the maintainer\"}"
		}
		document += "\n  ]\n}\n"
		records, refusals, err := suppress.IgnoreFile(writeTemp(t, dir, suppress.IgnoreFileName, document), symbols)
		if err != nil {
			t.Fatalf("suppress.IgnoreFile(the document just written) = _, _, %v, want its entries", err)
		}
		return records, refusals

	default:
		records := make([]suppress.Record, 0, len(found))
		for i := range found {
			records = append(records, suppress.Record{
				Code:      found[i].code,
				Symbol:    found[i].symbol,
				Path:      drawnFilePath,
				Reason:    "adjudicated by the maintainer",
				Bound:     found[i].id,
				Site:      token.Position{Filename: drawnFilePath, Line: i + 1, Column: 1},
				Mechanism: suppress.MechanismInline,
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
	rapid.Check(t, func(t *rapid.T) {
		found := drawnFindings(t)
		roots := rapid.SliceOfN(rapid.SampledFrom([]string{
			"go://" + drawnModulePath + "#Gone",
			"go://" + drawnModulePath + "#Gone*",
			"go://" + drawnModulePath + "#Go?e",
			"go://" + drawnModulePath + "/internal/queue#List.Push",
		}), 0, 4).Draw(t, "roots")

		records := make([]suppress.Record, 0, len(found))
		declared := &edges.Document{}
		for i := range found {
			records = append(records, suppress.Record{
				Code:      found[i].code,
				Symbol:    found[i].symbol,
				Path:      drawnFilePath,
				Reason:    "adjudicated by the maintainer",
				Site:      token.Position{Filename: suppress.IgnoreFileName, Line: i + 2, Column: 5},
				Mechanism: suppress.MechanismIgnore,
			})
			declared.Edges = append(declared.Edges, edges.Edge{
				ID:       "wire/drawn" + strconv.Itoa(i),
				Provides: found[i].symbol,
				UsedBy:   "ts://@example/app/src/wire.ts#Drawn" + strconv.Itoa(i),
			})
		}

		// Nothing bound, so nothing was suppressed and nothing is enumerated.
		in := inputOfRecords(records, nil, nil)
		in.Unmatched = make([]graph.Unmatched, 0, len(roots))
		for _, root := range roots {
			in.Unmatched = append(in.Unmatched, graph.Unmatched{Source: root})
		}
		in.Edges = declared

		stale, err := StaleSuppressions(in)
		if err != nil {
			t.Fatalf("StaleSuppressions() = _, %v, want every record reported", err)
		}
		if len(stale) != len(records) {
			t.Errorf("StaleSuppressions() over %d records that bound nothing reported %d, want one each: %v",
				len(records), len(stale), summary(stale))
		}
		if inEffect, reasons := Totals(in); inEffect != 0 || reasons != len(records) {
			t.Errorf("Totals() over %d records that bound nothing = %d in effect and %d reasons, want 0 and %d",
				len(records), inEffect, reasons, len(records))
		}

		unmatched, err := UnmatchedRoots(in)
		if err != nil {
			t.Fatalf("UnmatchedRoots() = _, %v, want every configured string reported", err)
		}
		if len(unmatched) != len(roots) {
			t.Errorf("UnmatchedRoots() over %d configured strings reported %d, want one each: %v",
				len(roots), len(unmatched), summary(unmatched))
		}
		for i := range unmatched {
			if unmatched[i].Symbol.Ref != in.Unmatched[i].Source {
				t.Errorf("UnmatchedRoots()[%d] names %q, want the configured string %q",
					i, unmatched[i].Symbol.Ref, in.Unmatched[i].Source)
			}
		}

		kept, evaluations := Evaluate(in, nil)
		if len(kept) != 0 {
			t.Errorf("Evaluate() over no finding kept %v, want none", summary(kept))
		}
		if len(evaluations) != len(declared.Edges) {
			t.Errorf("Evaluate() published %d records over %d declared edges, want one per own side",
				len(evaluations), len(declared.Edges))
		}
		for i := range evaluations {
			if evaluations[i].State != StateAbsent || evaluations[i].Finding != nil {
				t.Errorf("Evaluate() published %+v, want the absent state and no finding: nothing is enumerated",
					asNamed(evaluations)[i])
			}
		}
		if !slices.IsSortedFunc(evaluations, func(a, b Evaluation) int {
			return strings.Compare(a.Edge, b.Edge)
		}) {
			t.Errorf("Evaluate() published %+v, want the records ordered by edge", asNamed(evaluations))
		}
	})
}

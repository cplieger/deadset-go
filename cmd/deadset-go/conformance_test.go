package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/corpus"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// committedGaps decodes the declared gaps committed beside this source.
func committedGaps(t *testing.T) corpus.Gaps {
	t.Helper()
	var gaps corpus.Gaps
	if err := corpus.DecodeStrict(readCommitted(t, conformanceGapsFile), &gaps); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceGapsFile, err)
	}
	return gaps
}

func readCommitted(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("Setup: read %s: %v", name, err)
	}
	return body
}

// TestCorpusAnswerReadsTheCommittedConformanceDocuments pins the record every
// report states about this analyzer to the two committed documents: the corpus
// version, the result, the digest of the results document's bytes and the gaps.
func TestSelfReadsTheCommittedConformanceDocuments(t *testing.T) {
	t.Parallel()

	answered, err := selfOf()
	if err != nil {
		t.Fatalf("selfOf() = error %v, want the committed conformance record", err)
	}
	results := readCommitted(t, conformanceResultsFile)
	var document corpus.Results
	if err := corpus.DecodeStrict(results, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceResultsFile, err)
	}
	if got, want := answered.Analyzer.Conformance.CorpusVersion, vocab.CorpusVersion; got != want || document.CorpusVersion != want {
		t.Errorf("this analyzer answers corpus version %q and %s records %q, want %q as the pinned corpus publishes it",
			got, conformanceResultsFile, document.CorpusVersion, want)
	}
	if got := string(answered.Analyzer.Conformance.Result); got != document.Result {
		t.Errorf("this analyzer states the corpus result %q, want %q as %s records it", got, document.Result, conformanceResultsFile)
	}
	digest := sha256.Sum256(results)
	if want := "sha256:" + hex.EncodeToString(digest[:]); answered.Analyzer.Conformance.Digest != want {
		t.Errorf("this analyzer states the digest %q, want %q, the digest of the committed %s",
			answered.Analyzer.Conformance.Digest, want, conformanceResultsFile)
	}
	declared := committedGaps(t)
	if got, want := len(answered.Gaps), len(declared.Gaps); got != want {
		t.Fatalf("this analyzer declares %d gaps, want %d as %s declares them", got, want, conformanceGapsFile)
	}
	for i, gap := range declared.Gaps {
		held := answered.Gaps[i]
		if held.Fixture != gap.Fixture || held.Symbol != gap.Symbol || held.Capability != gap.Capability || held.Reason != gap.Reason {
			t.Errorf("this analyzer declares the gap %+v, want %+v as %s declares it", held, gap, conformanceGapsFile)
		}
	}
}

// TestTheDeclaredGapsNameTheProductAndTheCorpusTheyAnswer pins the committed gaps
// to this analyzer and the corpus: a gap naming a fixture or a capability the
// corpus does not hold covers nothing.
func TestTheDeclaredGapsNameTheProductAndTheCorpusTheyAnswer(t *testing.T) {
	t.Parallel()

	gaps := committedGaps(t)
	if gaps.Product.Name != name {
		t.Errorf("%s names the product %q, want %q as a report names this analyzer", conformanceGapsFile, gaps.Product.Name, name)
	}
	if gaps.CorpusVersion != vocab.CorpusVersion {
		t.Errorf("%s declares gaps against corpus %q, want %q", conformanceGapsFile, gaps.CorpusVersion, vocab.CorpusVersion)
	}
	doc, err := corpus.ReadDocument(spec.Corpus)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, gap := range gaps.Gaps {
		fixture, err := corpus.LoadFixture(spec.Corpus, doc, gap.Fixture)
		if err != nil {
			t.Errorf("%s declares a gap for the fixture %s, which the corpus does not hold: %v", conformanceGapsFile, gap.Fixture, err)
			continue
		}
		exercised := slices.ContainsFunc(fixture.Expect.Expect, func(row corpus.Row) bool {
			return (gap.Symbol == "" || gap.Symbol == row.Symbol) && slices.Contains(corpus.Capabilities(&row), gap.Capability)
		})
		if !exercised {
			t.Errorf("%s declares a gap on %s in %s, and no expectation it names exercises that capability", conformanceGapsFile, gap.Capability, gap.Fixture)
		}
	}
}

// TestTheBinaryReportsTheConformanceRecordItCommitted holds the report the built
// command writes to the committed record: the corpus version, the result and the
// digest of the committed results document.
func TestTheBinaryReportsTheConformanceRecordItCommitted(t *testing.T) {
	t.Parallel()
	results := readCommitted(t, conformanceResultsFile)
	digest := sha256.Sum256(results)
	var committed corpus.Results
	if err := corpus.DecodeStrict(results, &committed); err != nil {
		t.Fatalf("Setup: decode %s: %v", conformanceResultsFile, err)
	}

	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dir, err)
	}
	if err := writeFiles(dir, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"app.go":           "// Command app is the target of a conformance-block assertion.\npackage main\n\nfunc main() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}); err != nil {
		t.Fatalf("Setup: write the target module: %v", err)
	}
	binary := commandBinary(t)
	command := exec.CommandContext(t.Context(), binary, "analyze", "--target=./app", "--report=report.json")
	command.Dir = base
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s analyze over a clean module = %v, want the clean code\nstderr: %s", binary, err, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(base, "report.json"))
	if err != nil {
		t.Fatalf("read the report the run wrote: %v", err)
	}
	var document struct {
		Analyzer struct {
			Conformance report.Conformance `json:"conformance"`
		} `json:"analyzer"`
		DeclaredGaps []conformanceGap `json:"declared_gaps"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the report the run wrote: %v", err)
	}
	want := report.Conformance{CorpusVersion: committed.CorpusVersion, Result: report.Result(committed.Result), Digest: "sha256:" + hex.EncodeToString(digest[:])}
	if document.Analyzer.Conformance != want {
		t.Errorf("the report states the conformance block %+v, want %+v as the committed documents record it", document.Analyzer.Conformance, want)
	}
	if got, want := len(document.DeclaredGaps), len(committedGaps(t).Gaps); got != want {
		t.Errorf("the report names %d declared gaps, want %d as %s declares them", got, want, conformanceGapsFile)
	}
}

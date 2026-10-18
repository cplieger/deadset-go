package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// otherLanguageRoots configures one root of each other language beside a Go root
// that names nothing, which only the Go one is about.
const otherLanguageRoots = `{"target": {"kind": "application"}, "roots": {"patterns": [` +
	`"ts://@example/app/src/a.ts#main", "js://@example/app/b.js#x", "py://app#run", "go://example.com/app#Absent"]}}`

func TestAnalyzeReadsOnlyTheConfiguredRootsOfItsOwnLanguage(t *testing.T) {
	dir := applicationModule(t, otherLanguageRoots)

	got := runAnalyze(t, dir)
	if got.code != vocab.ExitFindings {
		t.Fatalf("analyze with other languages' roots = %d, want %d\nstderr:\n%s", got.code, vocab.ExitFindings, got.stderr)
	}
	var unmatched []string
	for _, f := range envelopeAt(t, got.reportPath).Findings {
		if f.Code == vocab.UnmatchedRoot {
			unmatched = append(unmatched, f.Symbol.Ref)
		}
	}
	if want := []string{"go://example.com/app#Absent"}; !slices.Equal(unmatched, want) {
		t.Errorf("analyze with other languages' roots reported the unmatched roots %v, want %v", unmatched, want)
	}
}

func TestPrintRootsReadsOnlyTheConfiguredRootsOfItsOwnLanguage(t *testing.T) {
	dir := applicationModule(t, otherLanguageRoots)

	var stdout, stderr bytes.Buffer
	if got := invoke(t.Context(), []string{"print-roots", "--target=" + dir}, &stdout, &stderr); got != vocab.ExitFindings {
		t.Fatalf("print-roots with other languages' roots = %d, want %d\nstderr: %q", got, vocab.ExitFindings, stderr.String())
	}
	want := string(vocab.UnmatchedRoot) + ": roots.patterns names nothing: go://example.com/app#Absent\n"
	if got := stderr.String(); got != want {
		t.Errorf("print-roots with other languages' roots wrote %q to stderr, want %q", got, want)
	}
}

func TestPrintRootsNamesEveryReasonOfAGoFuzzEntryExportedToC(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		"fuzz/plain.go":    "package fuzz\n\nfunc FuzzPlain(data []byte) int { return len(data) }\n",
		"fuzz/bridge.go":   "package fuzz\n\nimport \"C\"\n\n//export FuzzBoth\nfunc FuzzBoth(data []byte) int { return len(data) }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	var stdout, stderr bytes.Buffer
	if got := invoke(t.Context(), []string{"print-roots", "--target=" + dir}, &stdout, &stderr); got != vocab.ExitClean {
		t.Fatalf("print-roots = %d, want %d\nstderr: %q", got, vocab.ExitClean, stderr.String())
	}
	const both = "go://example.com/app/fuzz#FuzzBoth"
	want := both + "\tcgo-export\n" + both + "\tgo-fuzz\n"
	if got := stdout.String(); !strings.Contains(got, want) {
		t.Errorf("print-roots wrote\n%s\nwant it to hold\n%s", got, want)
	}
}

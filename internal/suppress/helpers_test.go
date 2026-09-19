package suppress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"github.com/cplieger/deadset-go/internal/scope"
	spec "github.com/cplieger/deadset-spec"
	"golang.org/x/tools/txtar"
)

// The build configuration every fixture of this package is loaded for. A
// directive is read from the source a configuration compiles, and one
// configuration is enough to measure that.
const (
	fixtureOS   = "linux"
	fixtureArch = "amd64"
)

// corpusPath is the published token corpus every case of this package's grammar
// tests is drawn from.
const corpusPath = "contract/grammar/suppression-corpus.json"

// corpusCase is one case of that corpus. The input is either an inline
// comment with its line offset or the entry object itself, which is why it stays
// raw until the case's kind is known.
type corpusCase struct {
	Input json.RawMessage `json:"input"`
	Kind  string          `json:"kind"`
	Rule  string          `json:"rule"`

	// Reports names one code per finding the input produces, in the order the
	// grammar's rules apply, and is absent on a case that produces at most one.
	// It is what says how many findings an input is, for the two inputs the count
	// is not otherwise readable from.
	Reports  []string `json:"reports"`
	Reason   string   `json:"reason"`
	Accepted bool     `json:"accepted"`
}

// inlineInput is the input of an inline case: the text of the comment from its
// first solidus to the end of its line, and the comment's line number minus the
// first line of the declaration it targets.
type inlineInput struct {
	Comment    string `json:"comment"`
	LineOffset int    `json:"line_offset"`
}

// readCorpus returns every case of the published corpus whose kind is kind.
func readCorpus(t *testing.T, kind string) []corpusCase {
	t.Helper()
	body, err := spec.Contract.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", corpusPath, err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatalf("Setup: decode %s: %v", corpusPath, err)
	}
	of := make([]corpusCase, 0, len(cases))
	for _, c := range cases {
		if c.Kind == kind {
			of = append(of, c)
		}
	}
	if len(of) == 0 {
		t.Fatalf("Setup: %s holds no %s case", corpusPath, kind)
	}
	return of
}

// readPage returns one page of the published grammar.
func readPage(t *testing.T, path string) string {
	t.Helper()
	body, err := spec.Contract.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", path, err)
	}
	return string(body)
}

// extract writes one txtar archive of testdata into a directory of its own and
// returns the directory, which is the target root the load resolves.
func extract(t *testing.T, archive string) string {
	t.Helper()
	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	dir := t.TempDir()
	for _, f := range parsed.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return dir
}

// loadDir resolves one directory through the production load, so the Need bits
// the enumeration depends on have one owner.
func loadDir(t *testing.T, dir string) (*load.Result, string) {
	t.Helper()

	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	result, err := load.Load(t.Context(), doc, load.Configuration{
		ID: fixtureOS + "-" + fixtureArch, OS: fixtureOS, Arch: fixtureArch,
	})
	if err != nil {
		t.Fatalf("Setup: load.Load(%s): %v", dir, err)
	}
	return &result, doc.Target.Path
}

// loaded extracts one archive, loads it and enumerates its declarations, so a
// reader is measured over the production path from the archive to the inventory
// rather than over symbols written by hand.
func loaded(t *testing.T, archive string) (*load.Result, string, []graph.Symbol) {
	t.Helper()

	result, root := loadDir(t, extract(t, archive))
	symbols, err := graph.Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", archive, err)
	}
	return result, root, symbols
}

// writeIgnoreFile writes one ignore document into a directory of its own and
// returns its path.
func writeIgnoreFile(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), IgnoreFileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return path
}

// resolverOf is the resolver over one load and its inventory, which is the one the
// composition root builds once per configuration and hands to every pass that
// renders a position.
func resolverOf(t *testing.T, result *load.Result, root string, symbols []graph.Symbol) *graph.Resolver {
	t.Helper()

	resolve, err := graph.NewResolver(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", root, err)
	}
	return resolve
}

// writeDocument writes one suppression document under name into a directory of its
// own and returns its path.
func writeDocument(t *testing.T, name, document string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return path
}

package edges

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v4"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The document every test of a well-formed read is measured over: two edges, one
// pairing a Go symbol with a TypeScript one and one pairing two Go symbols, so a
// read answers both the one-own-side case and the two-own-side case. The
// identifiers are written out of order, so a read that keeps document order
// fails.
const declared = `{
  "description": "Each edge pairs a Go wire type with the TypeScript generated from it.",
  "edges": {
    "wire/ServerEvent": {
      "because": "generated",
      "provides": "go://example.com/app#ServerEvent",
      "used_by": "ts://@example/app/src/wire.ts#ServerEvent"
    },
    "internal.bridge": {
      "provides": "go://example.com/app#Bridge",
      "used_by": "go://example.com/app/internal/queue#List"
    }
  }
}
`

// target writes one edges document at the root of a target of its own and
// returns the target root.
func target(t *testing.T, document string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, vocab.EdgesFileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return dir
}

// read reads one document and fails the test where the read refused it.
func read(t *testing.T, document string) *Document {
	t.Helper()

	held, err := Read(t.Context(), target(t, document))
	if err != nil {
		t.Fatalf("Read(a well-formed document) = _, %v, want the declared edges", err)
	}
	return held
}

func TestReadCarriesEveryDeclaredEdgeInIdentifierOrder(t *testing.T) {
	held := read(t, declared)

	want := []Edge{
		{ID: "internal.bridge", Provides: "go://example.com/app#Bridge", UsedBy: "go://example.com/app/internal/queue#List"},
		{ID: "wire/ServerEvent", Provides: "go://example.com/app#ServerEvent", UsedBy: "ts://@example/app/src/wire.ts#ServerEvent"},
	}
	if !reflect.DeepEqual(held.Edges, want) {
		t.Errorf("Read(declared).Edges = %+v, want %+v", held.Edges, want)
	}
}

func TestOwnReturnsEverySideOfTheLanguageAndNoOther(t *testing.T) {
	held := read(t, declared)

	want := []Reference{
		{Edge: "internal.bridge", Side: provides, Symbol: "go://example.com/app#Bridge"},
		{Edge: "internal.bridge", Side: usedBy, Symbol: "go://example.com/app/internal/queue#List"},
		{Edge: "wire/ServerEvent", Side: provides, Symbol: "go://example.com/app#ServerEvent"},
	}
	if got := held.Own("go"); !reflect.DeepEqual(got, want) {
		t.Errorf("Own(%q) = %+v, want %+v", "go", got, want)
	}
	ts := held.Own("ts")
	if len(ts) != 1 || ts[0].Side != usedBy || ts[0].Symbol != "ts://@example/app/src/wire.ts#ServerEvent" {
		t.Errorf("Own(%q) = %+v, want the one used_by side of the generated pair", "ts", ts)
	}
	if len(held.Own("python")) != 0 {
		t.Errorf("Own(%q) = %+v, want no side: a side no analyzer evaluated has no record", "python", held.Own("python"))
	}
}

func TestOwnOfANilDocumentIsNoSide(t *testing.T) {
	var absent *Document
	if got := absent.Own("go"); got != nil {
		t.Errorf("(*Document)(nil).Own(%q) = %+v, want no side", "go", got)
	}
}

func TestReadOfAnAbsentDocumentIsAnEmptyOne(t *testing.T) {
	held, err := Read(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("Read(a target with no edges document) = _, %v, want an empty document and no error", err)
	}
	if held == nil || len(held.Edges) != 0 {
		t.Errorf("Read(a target with no edges document) = %+v, want an empty document", held)
	}
}

func TestReadOfAnEmptyObjectIsADocumentWithNoEdge(t *testing.T) {
	if held := read(t, `{"edges": {}}`); len(held.Edges) != 0 {
		t.Errorf("Read(a document declaring no edge) carries %+v, want no edge", held.Edges)
	}
}

// Every document the Contract refuses is refused as malformed, naming the place
// at fault, the list form the edges member once took among them.
func TestReadRefusesTheDocumentsTheContractRefuses(t *testing.T) {
	const edge = `{"provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}`
	for _, tc := range []struct {
		name, document, names string
	}{
		{name: "the edges written as a list", document: `{"edges": [{"id": "wire/A", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`, names: "edges"},
		{name: "no edges member", document: `{"description": "nothing declared"}`, names: "edges"},
		{name: "an undeclared top-level key", document: `{"edges": {}, "pairs": {}}`, names: "pairs"},
		{name: "an identifier written twice", document: `{"edges": {"wire/A": ` + edge + `, "wire/A": ` + edge + `}}`, names: "wire/A"},
		{name: "an identifier outside its form", document: `{"edges": {"wire A": ` + edge + `}}`, names: "edges.wire A"},
		{name: "an edge naming no used_by side", document: `{"edges": {"wire/A": {"provides": "go://example.com/app#A"}}}`, names: "edges.wire/A.used_by"},
		{name: "an edge under an identifier of digits alone", document: `{"edges": {"1": {"provides": "go://example.com/app#A"}}}`, names: "edges.1.used_by"},
		{name: "an undeclared key of an edge", document: `{"edges": {"wire/A": {"provides": "go://example.com/app#A", "used_by": "ts://x#A", "line": 3}}}`, names: "edges.wire/A.line"},
		{name: "a wildcard side", document: `{"edges": {"wire/A": {"provides": "go://example.com/app#*", "used_by": "ts://x#A"}}}`, names: "edges.wire/A.provides"},
		{name: "a bare name", document: `{"edges": {"wire/A": {"provides": "A", "used_by": "ts://x#A"}}}`, names: "edges.wire/A.provides"},
		{name: "a scope holding whitespace", document: `{"edges": {"wire/A": {"provides": "go://example.com/a pp#A", "used_by": "ts://x#A"}}}`, names: "edges.wire/A.provides"},
		{name: "a fragment holding whitespace", document: `{"edges": {"wire/A": {"provides": "go://example.com/app#A B", "used_by": "ts://x#A"}}}`, names: "edges.wire/A.provides"},
		{name: "content after the document", document: `{"edges": {}} {}`, names: vocab.EdgesFileName},
		{name: "not JSON", document: `{`, names: vocab.EdgesFileName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := Read(t.Context(), target(t, tc.document))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Read(%s) = %+v, %v, want an error satisfying errors.Is(err, ErrMalformed)", tc.document, held, err)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("Read(%s) = %v, want it to name %q", tc.document, err, tc.names)
			}
		})
	}
}

// A reference outside the grammar that still names one symbol is accepted: its
// side evaluates absent, which is what a misspelled reference is owed.
func TestReadAcceptsAReferenceOutsideTheGrammarThatStillNamesOneSymbol(t *testing.T) {
	for _, ref := range []string{
		"go://example.com/app#Not.A.Real..Path",
		"go://example.com/app#'quoted*star'",
		"go://example.com/app#Type[computed*]",
		"go://example.com/app#A\fB",
		"go://example.com/a\fpp#A",
	} {
		document := `{"edges": {"wire/A": {"provides": ` + quote(ref) + `, "used_by": "ts://x#A"}}}`
		if held := read(t, document); len(held.Edges) != 1 || held.Edges[0].Provides != ref {
			t.Errorf("Read(a side %q) = %+v, want the one edge carrying it", ref, held.Edges)
		}
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\f", `\f`).Replace(s) + `"`
}

// Every published edges example reads, and every published negative of the
// edges schema is refused.
func TestReadHoldsThePublishedExamples(t *testing.T) {
	body, err := spec.Examples.ReadFile("examples/edges/declared.json")
	if err != nil {
		t.Fatalf("Setup: read examples/edges/declared.json: %v", err)
	}
	if held := read(t, string(body)); len(held.Edges) == 0 {
		t.Errorf("Read(examples/edges/declared.json) carries no edge, want the declared ones")
	}
	for _, name := range []string{"edges-identifier-form.json", "edges-object-absent.json", "edges-reference-wildcard.json", "edges-unknown-member.json"} {
		body, err := spec.Examples.ReadFile("examples/negatives/" + name)
		if err != nil {
			t.Fatalf("Setup: read examples/negatives/%s: %v", name, err)
		}
		if _, err := Read(t.Context(), target(t, string(body))); !errors.Is(err, ErrMalformed) {
			t.Errorf("Read(examples/negatives/%s) = %v, want it refused as malformed", name, err)
		}
	}
}

func TestReadRefusesADocumentOverTheBound(t *testing.T) {
	_, err := Read(t.Context(), target(t, `{"edges": {}}`+strings.Repeat(" ", vocab.DocumentMaxBytes)))
	if !errors.Is(err, ErrMalformed) || !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Errorf("Read(a document over the bound) = %v, want it refused as malformed and too large", err)
	}
}

// A document name resolving outside the target, or to anything but a regular
// file, is refused without being read, and not as a malformed document.
func TestReadRefusesADocumentNameThatLeavesTheTargetOrIsNoFile(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(outside, []byte(declared), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", outside, err)
	}
	escaping := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(escaping, vocab.EdgesFileName)); err != nil {
		t.Fatalf("Setup: symlink: %v", err)
	}
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, vocab.EdgesFileName), 0o750); err != nil {
		t.Fatalf("Setup: mkdir: %v", err)
	}
	for _, tc := range []struct {
		name, dir  string
		notRegular bool
	}{
		{name: "a link out of the target", dir: escaping},
		{name: "a directory", dir: directory, notRegular: true},
	} {
		held, err := Read(t.Context(), tc.dir)
		if err == nil || errors.Is(err, ErrMalformed) {
			t.Errorf("Read(%s) = %+v, %v, want a refusal that is not a malformed document", tc.name, held, err)
		}
		if tc.notRegular && !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Errorf("Read(%s) = %v, want an error matching atomicfile.ErrNotRegular", tc.name, err)
		}
	}
}

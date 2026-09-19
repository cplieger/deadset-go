package edges

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The document every test of a well-formed read is measured over: two edges, one
// pairing a Go symbol with a TypeScript one and one pairing two Go symbols, so a
// read answers both the one-own-side case and the two-own-side case.
const declared = `{
  "description": "Each edge pairs a Go wire type with the TypeScript generated from it.",
  "edges": [
    {
      "id": "wire/ServerEvent",
      "because": "generated",
      "provides": "go://example.com/app#ServerEvent",
      "used_by": "ts://@example/app/src/wire.ts#ServerEvent"
    },
    {
      "id": "internal.bridge",
      "provides": "go://example.com/app#Bridge",
      "used_by": "go://example.com/app/internal/queue#List"
    }
  ]
}
`

// write writes one document into a directory of its own and returns its path.
func write(t *testing.T, document string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
	return path
}

// read reads one document and fails the test where the read refused it.
func read(t *testing.T, document string) *Document {
	t.Helper()

	held, err := Read(write(t, document))
	if err != nil {
		t.Fatalf("Read(a well-formed document) = _, %v, want the declared edges", err)
	}
	return held
}

func TestReadCarriesEveryDeclaredEdgeInDocumentOrder(t *testing.T) {
	held := read(t, declared)

	if held.Description != "Each edge pairs a Go wire type with the TypeScript generated from it." {
		t.Errorf("Read(the declared document).Description = %q, want the document's own description", held.Description)
	}
	want := []Edge{
		{
			ID:       "wire/ServerEvent",
			Because:  "generated",
			Provides: "go://example.com/app#ServerEvent",
			UsedBy:   "ts://@example/app/src/wire.ts#ServerEvent",
		},
		{
			ID:       "internal.bridge",
			Provides: "go://example.com/app#Bridge",
			UsedBy:   "go://example.com/app/internal/queue#List",
		},
	}
	if len(held.Edges) != len(want) {
		t.Fatalf("Read(the declared document) carries %d edges, want %d: %+v", len(held.Edges), len(want), held.Edges)
	}
	for i := range want {
		if held.Edges[i] != want[i] {
			t.Errorf("Read(the declared document).Edges[%d] = %+v, want %+v", i, held.Edges[i], want[i])
		}
	}
}

func TestOwnReturnsEverySideOfTheLanguageAndNoOther(t *testing.T) {
	held := read(t, declared)

	want := []Reference{
		{Edge: "wire/ServerEvent", Side: Provides, Symbol: "go://example.com/app#ServerEvent"},
		{Edge: "internal.bridge", Side: Provides, Symbol: "go://example.com/app#Bridge"},
		{Edge: "internal.bridge", Side: UsedBy, Symbol: "go://example.com/app/internal/queue#List"},
	}
	got := held.Own("go")
	if len(got) != len(want) {
		t.Fatalf("Own(%q) returned %d sides, want %d: %+v", "go", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Own(%q)[%d] = %+v, want %+v", "go", i, got[i], want[i])
		}
	}

	ts := held.Own("ts")
	if len(ts) != 1 || ts[0].Side != UsedBy || ts[0].Symbol != "ts://@example/app/src/wire.ts#ServerEvent" {
		t.Errorf("Own(%q) = %+v, want the one used_by side of the generated pair", "ts", ts)
	}
	if len(held.Own("python")) != 0 {
		t.Errorf("Own(%q) = %+v, want no side: a side no analyzer evaluated has no record",
			"python", held.Own("python"))
	}
}

func TestOwnOfANilDocumentIsNoSide(t *testing.T) {
	var absent *Document
	if got := absent.Own("go"); got != nil {
		t.Errorf("(*Document)(nil).Own(%q) = %+v, want no side", "go", got)
	}
}

func TestReadOfAnAbsentDocumentIsAnEmptyOne(t *testing.T) {
	held, err := Read(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("Read(an absent document) = _, %v, want an empty document and no error", err)
	}
	if held == nil || len(held.Edges) != 0 {
		t.Errorf("Read(an absent document) = %+v, want an empty document", held)
	}
}

func TestReadOfAnEmptyArrayIsADocumentWithNoEdge(t *testing.T) {
	held := read(t, `{"edges": []}`)
	if len(held.Edges) != 0 {
		t.Errorf("Read(a document declaring no edge) carries %+v, want no edge", held.Edges)
	}
}

func TestReadRefusesTheDocumentsTheGrammarRefuses(t *testing.T) {
	for name, document := range map[string]string{
		"no edges member":             `{"description": "nothing declared"}`,
		"an undeclared top-level key": `{"edges": [], "pairs": []}`,
		"the edges member twice": `{"edges": [], "edges": [
      {"id": "a", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a member of one edge twice": `{"edges": [
      {"id": "a", "id": "b", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"an undeclared key on an edge": `{"edges": [
      {"id": "a", "why": "generated", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a value of the wrong type": `{"edges": [
      {"id": ["a"], "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"an edge naming no id": `{"edges": [
      {"provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"an edge naming no provides side": `{"edges": [
      {"id": "a", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"an edge naming no used_by side": `{"edges": [
      {"id": "a", "provides": "go://example.com/app#A"}]}`,
		"an identifier outside its form": `{"edges": [
      {"id": "wire ServerEvent", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a wildcard in the fragment of a side": `{"edges": [
      {"id": "a", "provides": "go://example.com/app#Server*", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a wildcard in the scope of a side": `{"edges": [
      {"id": "a", "provides": "go://example.com/app/internal/*#A", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a bare name on a side": `{"edges": [
      {"id": "a", "provides": "ServerEvent", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a side with no fragment separator": `{"edges": [
      {"id": "a", "provides": "go://example.com/app", "used_by": "ts://@example/app/src/a.ts#A"}]}`,
		"a second value after the document": `{"edges": []} {"edges": []}`,
		"a document that is not an object":  `["edges"]`,
	} {
		t.Run(name, func(t *testing.T) {
			held, err := Read(write(t, document))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Read(%s) = %+v, %v, want an error satisfying errors.Is(err, ErrMalformed)", name, held, err)
			}
			if held != nil {
				t.Errorf("Read(%s) returned %+v with its error, want no document", name, held)
			}
			var malformed *MalformedError
			if !errors.As(err, &malformed) {
				t.Fatalf("Read(%s) = %v, want one errors.As reads as *MalformedError", name, err)
			}
			if malformed.Site.Filename != FileName {
				t.Errorf("Read(%s) refused at %s, want a site in %s", name, malformed.Site, FileName)
			}
			if malformed.Want == "" {
				t.Errorf("Read(%s) error = %q, want it to name the form expected", name, malformed.Error())
			}
		})
	}
}

func TestReadAcceptsAReferenceOutsideTheGrammarThatStillNamesOneSymbol(t *testing.T) {
	// The wildcard is the one character a pattern is written with, and it stands
	// for itself inside a quoted component and inside a computed key, so each of
	// these names one symbol. The question mark is no wildcard of this grammar at
	// all: a reference carrying one names at most one symbol, and where it names
	// none the side evaluates absent and the merge reports the edge.
	for name, side := range map[string]string{
		"a wildcard inside a quoted component": `ts://@example/app/src/a.ts#Headers.'a*b'`,
		"a wildcard inside a computed key":     `ts://@example/app/src/a.ts#Sizes.[k*2]`,
		"a question mark in a quoted name":     `ts://@example/app/src/a.ts#Flags.'active?'`,
		"a question mark outside the grammar":  `ts://@example/app/src/a.ts#Serve?`,
	} {
		t.Run(name, func(t *testing.T) {
			held := read(t, `{"edges": [
      {"id": "a", "provides": "go://example.com/app#A", "used_by": `+strconv.Quote(side)+`}]}`)
			if len(held.Edges) != 1 || held.Edges[0].UsedBy != side {
				t.Errorf("Read(a document whose used_by side is %s) carries %+v, want the side as written",
					side, held.Edges)
			}
		})
	}
}

func TestReadNamesThePositionOfTheBraceThatOpensTheEdgeAtFault(t *testing.T) {
	const document = `{
  "edges": [
    {"id": "first", "provides": "go://example.com/app#A", "used_by": "ts://@example/app/src/a.ts#A"},
    {"id": "second", "provides": "go://example.com/app#B", "used_by": "ServerEvent"}
  ]
}
`
	_, err := Read(write(t, document))
	var malformed *MalformedError
	if !errors.As(err, &malformed) {
		t.Fatalf("Read(a document whose second edge names a bare name) = _, %v, want a *MalformedError", err)
	}
	if malformed.Site.Line != 4 || malformed.Site.Column != 5 {
		t.Errorf("Read(a document whose second edge names a bare name) refused at %s, want %s:4:5",
			malformed.Site, FileName)
	}
}

func TestReadCountsAColumnInUTF16CodeUnits(t *testing.T) {
	// The description holds one astral character, which is two UTF-16 code units
	// and four bytes, so a byte column and the column a report carries differ.
	const document = "{\n  \"description\": \"\U0001F600\",\n  \"edges\": [\n    {\"id\": \"a\", \"provides\": \"go://example.com/app#A\", \"used_by\": \"bare\"}\n  ]\n}\n"

	_, err := Read(write(t, document))
	var malformed *MalformedError
	if !errors.As(err, &malformed) {
		t.Fatalf("Read(a document carrying an astral character) = _, %v, want a *MalformedError", err)
	}
	if malformed.Site.Line != 4 || malformed.Site.Column != 5 {
		t.Errorf("Read(a document carrying an astral character) refused at %s, want %s:4:5",
			malformed.Site, FileName)
	}
}

func TestReadReportsADocumentItCannotRead(t *testing.T) {
	_, err := Read(t.TempDir())
	if err == nil {
		t.Fatal("Read(a directory) = _, nil, want an error naming the path")
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrMalformed) {
		t.Errorf("Read(a directory) = _, %v, want neither an absent document nor a malformed one", err)
	}
}

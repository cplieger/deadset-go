package graph

import (
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
)

// A write carries the statement or element its deletion deletes where the write is
// that statement's one target, and nothing where deleting it deletes no statement.
func TestReferencesGiveAWriteTheSpanItsDeletionDeletes(t *testing.T) {
	result, root := loadDir(t, extract(t, "write-spans.txtar"), "linux", "amd64")
	symbols, rs, err := inventoryOf(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: inventoryOf(write-spans.txtar): %v", err)
	}
	refs, _, err := References(result, rs, symbols, TestClasses{})
	if err != nil {
		t.Fatalf("References(write-spans.txtar) error: %v", err)
	}
	names := make(map[string]string, len(symbols))
	for i := range symbols {
		names[string(symbols[i].ID)] = symbols[i].Name
	}

	var got []string
	for i := range refs {
		r := &refs[i]
		if r.Kind != RefWrite {
			continue
		}
		span := "none"
		if r.Span != (inventory.SourceRange{}) {
			src, err := os.ReadFile(r.Span.File)
			if err != nil {
				t.Fatalf("read the span's file %s: %v", r.Span.File, err)
			}
			span = strconv.Quote(string(src[r.Span.From:r.Span.To]))
		}
		got = append(got, names[string(r.To)]+" "+span)
	}
	want := []string{
		`count "count = 1"`,
		`outer.hits "box.hits++"`,
		`outer.hits "hits: 2"`,
		`table "delete(table, \"a\")"`,
		"gone none",
		"count none",
		"other none",
		"key none",
		"outer.in none",
		`inner.leaf "box.in.leaf = 5"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("References(write-spans.txtar) writes =\n%v\nwant\n%v", got, want)
	}
}

func TestViaStringNamesEveryRule(t *testing.T) {
	for v := viaIdentifier; v <= viaConstantRepetition; v++ {
		if got := v.String(); got == "" || got == "via("+strconv.Itoa(int(v))+")" {
			t.Errorf("via(%d).String() = %q, want the rule's spelling", v, got)
		}
	}
	if got, want := via(255).String(), "via(255)"; got != want {
		t.Errorf("via(255).String() = %q, want %q", got, want)
	}
}

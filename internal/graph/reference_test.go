package graph

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// analysis is one fixture's symbol inventory and the references over it.
type analysis struct {
	names   map[SymbolID]string
	symbols []Symbol
	refs    []Reference
	rules   []TestFileRule
}

// analyze extracts one archive, loads it for one configuration, enumerates its
// symbols and walks its references.
func analyze(t *testing.T, archive string) analysis {
	t.Helper()

	dir := extract(t, archive)
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", archive, err)
	}
	refs, rules, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References(%s) error: %v", archive, err)
	}

	names := make(map[SymbolID]string, len(symbols))
	for _, s := range symbols {
		names[s.ID] = s.Name
	}
	return analysis{names: names, symbols: symbols, refs: refs, rules: rules}
}

// name spells one end of a reference as the declaration it is, and as the raw
// identifier when no symbol answers to it, so a golden row is readable and a
// row naming no declaration is visible.
func (a analysis) name(id SymbolID) string {
	if name, ok := a.names[id]; ok {
		return name
	}
	return string(id)
}

// render prints one reference per line, tab-separated.
func (a analysis) render() string {
	var b strings.Builder
	for _, r := range a.refs {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%t\t%s:%d:%d\n",
			a.name(r.From), a.name(r.To), r.Kind, r.Test, r.Pos.Filename, r.Pos.Line, r.Pos.Column)
	}
	return b.String()
}

// count returns how many references of one kind run between two declarations.
func (a analysis) count(from, to string, kind RefKind) int {
	found := 0
	for _, r := range a.refs {
		if a.name(r.From) == from && a.name(r.To) == to && r.Kind == kind {
			found++
		}
	}
	return found
}

// positions lists where the references of one kind to one declaration are
// written.
func (a analysis) positions(to string, kind RefKind) []string {
	var found []string
	for _, r := range a.refs {
		if a.name(r.To) == to && r.Kind == kind {
			found = append(found, fmt.Sprintf("%s:%d:%d", r.Pos.Filename, r.Pos.Line, r.Pos.Column))
		}
	}
	return found
}

// goldenTable reads one committed table, writing it from got first when the
// regeneration gate is set.
func goldenTable(name, got string) (string, error) {
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			return "", err
		}
	}
	want, err := os.ReadFile(path)
	return string(want), err
}

func TestReferencesGoldenTableOverEveryKind(t *testing.T) {
	const run = "TestReferencesGoldenTableOverEveryKind"

	got := analyze(t, "reference-kinds.txtar").render()
	want, err := goldenTable("reference-kinds.golden", got)
	if err != nil {
		t.Fatalf("Setup: reference-kinds.golden (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s): %v", run, err)
	}
	if got != want {
		t.Errorf("References(reference-kinds.txtar) golden mismatch (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s)\n--- want\n%s\n+++ got\n%s",
			run, want, got)
	}
}

func TestReferencesRecordsTheEnclosingDeclaration(t *testing.T) {
	a := analyze(t, "reference-kinds.txtar")

	cases := []struct {
		from string
		to   string
		kind RefKind
		want int
	}{
		// A method selector lands on the field, from the method that selects it.
		{from: "(*Catalog).Size", to: "Catalog.entries", kind: RefRead, want: 1},
		// A promoted member is reached through the embedded field, which the
		// selector references as well as the member itself.
		{from: "Promoted", to: "Buffered.Catalog", kind: RefRead, want: 2},
		{from: "Promoted", to: "Catalog.entries", kind: RefRead, want: 1},
		{from: "Promoted", to: "Catalog.pair", kind: RefRead, want: 1},
		{from: "Promoted", to: "Catalog.pair.low", kind: RefRead, want: 1},
		{from: "Promoted", to: "Buffered.Fetcher", kind: RefRead, want: 1},
		{from: "Promoted", to: "Fetcher.Fetch", kind: RefCall, want: 1},
		// A self-reference counts, because the call is written in a declaration
		// the graph holds.
		{from: "Depth", to: "Depth", kind: RefCall, want: 1},
		// A type-switch case and a type assertion name their type.
		{from: "Classify", to: "Catalog", kind: RefAssert, want: 1},
		{from: "Classify", to: "Fetcher", kind: RefAssert, want: 1},
		{from: "Assert", to: "Catalog", kind: RefAssert, want: 1},
		{from: "Classify", to: "Tier", kind: RefConversion, want: 1},
		// An embedded field references its type from the field the language
		// names after that type; an embedded interface has no field of its own,
		// so the interface that embeds it is what references it.
		{from: "Buffered.Catalog", to: "Catalog", kind: RefEmbed, want: 1},
		{from: "Buffered.Fetcher", to: "Fetcher", kind: RefEmbed, want: 1},
		{from: "Reader", to: "Fetcher", kind: RefEmbed, want: 1},
		// A type-set element is a type use rather than an embed.
		{from: "Stored", to: "Catalog", kind: RefTypeUse, want: 1},
		{from: "Stored", to: "Buffered", kind: RefTypeUse, want: 1},
		// A field's type, a variable's type and a type parameter's constraint
		// reference from the declaration that carries them.
		{from: "Catalog.entries", to: "Tier", kind: RefTypeUse, want: 1},
		{from: "Catalog.pair.low", to: "Tier", kind: RefTypeUse, want: 1},
		{from: "lonely", to: "Tier", kind: RefTypeUse, want: 1},
		{from: "Constrained[F]", to: "Fetcher", kind: RefTypeUse, want: 1},
		{from: "Pair[A]", to: "Fetcher", kind: RefTypeUse, want: 1},
		{from: "Pair[B]", to: "Fetcher", kind: RefTypeUse, want: 1},
		// A method's receiver and its results reference from the method.
		{from: "(*Catalog).Size", to: "Catalog", kind: RefTypeUse, want: 1},
		{from: "(*Catalog).Size", to: "Tier", kind: RefTypeUse, want: 1},
		{from: "(*Catalog).Size", to: "Tier", kind: RefConversion, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.from+"-to-"+tc.to+"-"+tc.kind.String(), func(t *testing.T) {
			if got := a.count(tc.from, tc.to, tc.kind); got != tc.want {
				t.Errorf("References(reference-kinds.txtar) holds %d %s references from %s to %s, want %d",
					got, tc.kind, tc.from, tc.to, tc.want)
			}
		})
	}
}

func TestReferencesResolvesEveryEndToASymbol(t *testing.T) {
	a := analyze(t, "reference-kinds.txtar")
	if len(a.refs) == 0 {
		t.Fatal("References(reference-kinds.txtar) returned no reference, want the fixture's own")
	}

	for _, r := range a.refs {
		if _, ok := a.names[r.From]; !ok {
			t.Errorf("References(reference-kinds.txtar) reference at %s:%d:%d has From %q, want a declaration the inventory holds",
				r.Pos.Filename, r.Pos.Line, r.Pos.Column, r.From)
		}
		if _, ok := a.names[r.To]; !ok {
			t.Errorf("References(reference-kinds.txtar) reference at %s:%d:%d has To %q, want a declaration the inventory holds",
				r.Pos.Filename, r.Pos.Line, r.Pos.Column, r.To)
		}
	}
}

func TestReferencesLeavesADeclarationSiteUnreferenced(t *testing.T) {
	a := analyze(t, "reference-kinds.txtar")

	// Every declaration answers for itself: the identifier that declares a
	// symbol is not a use of it, so a symbol nothing else names carries nothing.
	unreferenced := []string{"lonely", "Stored", "Reader", "Reader.Read", "Pair", "Promoted"}
	for _, name := range unreferenced {
		t.Run(name, func(t *testing.T) {
			var found []string
			for _, r := range a.refs {
				if a.name(r.To) == name {
					found = append(found, fmt.Sprintf("%s at %s:%d:%d", r.Kind, r.Pos.Filename, r.Pos.Line, r.Pos.Column))
				}
			}
			if len(found) != 0 {
				t.Errorf("References(reference-kinds.txtar) references %s %d times (%v), want none", name, len(found), found)
			}
		})
	}
}

func TestReferencesGoldenTableOverTheWriteShapes(t *testing.T) {
	const run = "TestReferencesGoldenTableOverTheWriteShapes"

	got := analyze(t, "reference-writes.txtar").render()
	want, err := goldenTable("reference-writes.golden", got)
	if err != nil {
		t.Fatalf("Setup: reference-writes.golden (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s): %v", run, err)
	}
	if got != want {
		t.Errorf("References(reference-writes.txtar) golden mismatch (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s)\n--- want\n%s\n+++ got\n%s",
			run, want, got)
	}
}

func TestReferencesSplitsReadsFromWrites(t *testing.T) {
	a := analyze(t, "reference-writes.txtar")

	cases := []struct {
		name string
		from string
		to   string
		kind RefKind
		want int
	}{
		{name: "assignment_writes", from: "Assign", to: "count", kind: RefWrite, want: 1},
		{name: "assignment_reads_back", from: "Assign", to: "count", kind: RefRead, want: 1},
		{name: "compound_writes", from: "Compound", to: "count", kind: RefWrite, want: 1},
		{name: "compound_records_no_read", from: "Compound", to: "count", kind: RefRead, want: 0},
		{name: "increment_writes", from: "Increment", to: "count", kind: RefWrite, want: 1},
		{name: "increment_records_no_read", from: "Increment", to: "count", kind: RefRead, want: 0},
		{name: "address_of_reads", from: "Address", to: "count", kind: RefRead, want: 1},
		{name: "address_of_records_no_write", from: "Address", to: "count", kind: RefWrite, want: 0},
		{name: "selector_writes_its_member", from: "Selector", to: "Counter.total", kind: RefWrite, want: 1},
		{name: "selector_reads_what_it_reaches_through", from: "Selector", to: "shared", kind: RefRead, want: 1},
		{name: "selector_does_not_write_that_value", from: "Selector", to: "shared", kind: RefWrite, want: 0},
		{name: "range_assignment_writes_its_key", from: "Loop", to: "count", kind: RefWrite, want: 1},
		{name: "two_value_assignment_writes_the_field", from: "Swap", to: "Counter.total", kind: RefWrite, want: 1},
		{name: "two_value_assignment_reads_the_field", from: "Swap", to: "Counter.total", kind: RefRead, want: 1},
		{name: "two_value_assignment_writes_the_variable", from: "Swap", to: "count", kind: RefWrite, want: 1},
		{name: "two_value_assignment_reads_the_variable", from: "Swap", to: "count", kind: RefRead, want: 1},
		// A store through an index or a key writes the collection: a slice or a
		// map a package only ever stores into holds nothing anything reads.
		{name: "indexed_collection_is_written", from: "Element", to: "Counter.names", kind: RefWrite, want: 1},
		{name: "indexed_collection_is_not_read", from: "Element", to: "Counter.names", kind: RefRead, want: 0},
		{name: "keyed_collection_is_written", from: "Entry", to: "index", kind: RefWrite, want: 1},
		{name: "keyed_collection_is_not_read", from: "Entry", to: "index", kind: RefRead, want: 0},
		{name: "delete_writes_the_collection", from: "Remove", to: "entries", kind: RefWrite, want: 1},
		{name: "delete_does_not_read_the_collection", from: "Remove", to: "entries", kind: RefRead, want: 0},
		// An indirection writes through a value it reads: the pointer is read to
		// find the pointee, which is not a declaration.
		{name: "pointer_written_through_is_read", from: "Indirect", to: "slot", kind: RefRead, want: 1},
		{name: "pointer_written_through_is_not_written", from: "Indirect", to: "slot", kind: RefWrite, want: 0},
		// A field a struct literal keys is an initialising store, whatever the
		// literal spells of its own type; a key of a map literal names no field
		// and is a read of what it does name.
		{name: "literal_field_key_writes_the_field", from: "Keyed", to: "Record.label", kind: RefWrite, want: 1},
		{name: "literal_field_key_does_not_read_the_field", from: "Keyed", to: "Record.label", kind: RefRead, want: 0},
		{name: "literal_field_value_reads_what_it_names", from: "Keyed", to: "high", kind: RefRead, want: 1},
		{name: "elided_literal_field_key_writes_the_field", from: "Elided", to: "Record.label", kind: RefWrite, want: 1},
		{name: "map_literal_key_is_read", from: "Mapped", to: "high", kind: RefRead, want: 1},
		{name: "map_literal_key_is_not_written", from: "Mapped", to: "high", kind: RefWrite, want: 0},
		// The read inside the append-back idiom is how the store is written, so
		// both identifiers write; an append landing elsewhere reads its first
		// argument.
		{name: "append_back_writes_at_both_identifiers", from: "Grow", to: "queue", kind: RefWrite, want: 2},
		{name: "append_back_records_no_read", from: "Grow", to: "queue", kind: RefRead, want: 0},
		{name: "append_landing_elsewhere_reads_its_argument", from: "Spare", to: "queue", kind: RefRead, want: 1},
		{name: "append_landing_elsewhere_writes_its_target", from: "Spare", to: "spare", kind: RefWrite, want: 1},
		// The idiom is matched on the name chain both sides are written as, so an
		// append back to a field writes it twice, and an append whose sides name
		// two different fields or two different values reads the one it appends
		// from.
		{name: "append_back_to_a_field_writes_it_at_both_selectors", from: "Fill", to: "Counter.names", kind: RefWrite, want: 2},
		{name: "append_back_to_a_field_records_no_read", from: "Fill", to: "Counter.names", kind: RefRead, want: 0},
		{name: "append_between_two_fields_writes_the_one_it_lands_in", from: "Crossed", to: "Bucket.spare", kind: RefWrite, want: 1},
		{name: "append_between_two_fields_reads_the_one_it_appends_from", from: "Crossed", to: "Bucket.names", kind: RefRead, want: 1},
		{name: "append_between_two_fields_does_not_write_the_one_it_appends_from", from: "Crossed", to: "Bucket.names", kind: RefWrite, want: 0},
		{name: "append_between_two_values_writes_the_field_it_lands_in", from: "Mismatched", to: "Bucket.names", kind: RefWrite, want: 1},
		{name: "append_between_two_values_reads_the_field_it_appends_from", from: "Mismatched", to: "Counter.names", kind: RefRead, want: 1},
		// A compound assignment through a key writes the collection and records no
		// read, the way the plain compound assignment above does.
		{name: "compound_assignment_through_a_key_writes_the_collection", from: "Accumulate", to: "tally", kind: RefWrite, want: 1},
		{name: "compound_assignment_through_a_key_records_no_read", from: "Accumulate", to: "tally", kind: RefRead, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.count(tc.from, tc.to, tc.kind); got != tc.want {
				t.Errorf("References(reference-writes.txtar) holds %d %s references from %s to %s, want %d",
					got, tc.kind, tc.from, tc.to, tc.want)
			}
		})
	}
}

func TestReferencesKeepsEveryWritePositionOfOneSymbol(t *testing.T) {
	a := analyze(t, "reference-writes.txtar")

	want := []string{
		"counter.go:23:2",  // Assign
		"counter.go:28:24", // Compound
		"counter.go:31:20", // Increment
		"counter.go:50:34", // Swap
		"counter.go:54:6",  // Loop
	}
	if got := a.positions("count", RefWrite); !slices.Equal(got, want) {
		t.Errorf("References(reference-writes.txtar) writes count at %v, want %v", got, want)
	}
}

func TestReferencesGoldenTableOverThePackageVariants(t *testing.T) {
	const run = "TestReferencesGoldenTableOverThePackageVariants"

	got := analyze(t, "variants.txtar").render()
	want, err := goldenTable("variants-references.golden", got)
	if err != nil {
		t.Fatalf("Setup: variants-references.golden (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s): %v", run, err)
	}
	if got != want {
		t.Errorf("References(variants.txtar) golden mismatch (run UPDATE_GOLDEN=1 go test ./internal/graph/ -run %s)\n--- want\n%s\n+++ got\n%s",
			run, want, got)
	}
}

func TestReferencesRecordsAProductionReferenceOncePerVariantSet(t *testing.T) {
	a := analyze(t, "variants.txtar")

	// The package and its in-package test variant type-check catalog.go
	// independently, so a walk per variant would record the production call
	// twice and report three references where the source holds two.
	if got := a.count("Resolve", "normalize", RefCall); got != 1 {
		t.Errorf("References(variants.txtar) holds %d production calls from Resolve to normalize, want 1", got)
	}
	if got := a.count("TestNormalize", "normalize", RefCall); got != 1 {
		t.Errorf("References(variants.txtar) holds %d calls from TestNormalize to normalize, want 1", got)
	}
	total := 0
	for _, r := range a.refs {
		if a.name(r.To) == "normalize" {
			total++
		}
	}
	if total != 2 {
		t.Errorf("References(variants.txtar) references normalize %d times, want 2", total)
	}
}

func TestReferencesInspectsEachSourceFileOnce(t *testing.T) {
	dir := extract(t, "variants.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(variants.txtar): %v", err)
	}

	p, err := newReferencePass(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: newReferencePass(variants.txtar): %v", err)
	}
	if err := p.walk(result.Packages); err != nil {
		t.Fatalf("References(variants.txtar) walk error: %v", err)
	}

	// catalog.go is compiled by two variants and inspected for the first of
	// them; each test file is compiled by the one variant that holds it. A file is
	// keyed by the path the toolchain named it by, which is what tells one
	// module's file from another module's file of the same name, so the assertion
	// reads the base names.
	reached := make(map[string]int, len(p.reached))
	for named, count := range p.reached {
		reached[filepath.Base(named)] = count
	}
	want := map[string]int{"catalog.go": 2, "catalog_test.go": 1, "external_test.go": 1}
	if !maps.Equal(reached, want) {
		t.Errorf("References(variants.txtar) reached %v, want %v", reached, want)
	}

	seen := make(map[Reference]int, len(p.refs))
	for _, r := range p.refs {
		seen[r]++
		if seen[r] > 1 {
			t.Errorf("References(variants.txtar) recorded one %s reference from %q to %q at %s:%d:%d %d times, want once",
				r.Kind, r.From, r.To, r.Pos.Filename, r.Pos.Line, r.Pos.Column, seen[r])
		}
	}
}

func TestReferencesOrderIsTheSameOnEveryCall(t *testing.T) {
	dir := extract(t, "reference-kinds.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(reference-kinds.txtar): %v", err)
	}

	first, _, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References first call error: %v", err)
	}
	second, _, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References second call error: %v", err)
	}
	if !slices.Equal(first, second) {
		t.Errorf("References returned a different order on the second call\n--- first\n%v\n+++ second\n%v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if byUse(first[i-1], first[i]) >= 0 {
			t.Fatalf("References order is not strictly increasing at %d: %s to %s then %s to %s",
				i, first[i-1].From, first[i-1].To, first[i].From, first[i].To)
		}
	}
}

func TestReferencesRefusesAnIncompleteRequest(t *testing.T) {
	dir := extract(t, "reference-kinds.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(reference-kinds.txtar): %v", err)
	}

	cases := []struct {
		name    string
		result  *load.Result
		read    ReadFile
		wantErr error
	}{
		{name: "no result", result: nil, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		{name: "no file set", result: &load.Result{Packages: result.Packages}, read: os.ReadFile, wantErr: ErrIncompleteLoad},
		{name: "no reader", result: result, read: nil, wantErr: ErrIncompleteLoad},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, rules, err := References(tc.result, root, tc.read, symbols)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("References(%s) error = %v, want one satisfying errors.Is(err, %v)", tc.name, err, tc.wantErr)
			}
			if refs != nil || rules != nil {
				t.Errorf("References(%s) returned %d references and %d rules, want none", tc.name, len(refs), len(rules))
			}
		})
	}
}

func TestReferencesReportsAFileTheTargetRootDoesNotHold(t *testing.T) {
	result, root, file := outsideRoot(t)

	refs, rules, err := References(result, root, readOutsideRootSource, nil)
	if !errors.Is(err, ErrSource) {
		t.Fatalf("References(a file outside the root) error = %v, want one satisfying errors.Is(err, %v)", err, ErrSource)
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("References(a file outside the root) error = %v, want it to name %s", err, file)
	}
	if refs != nil || rules != nil {
		t.Errorf("References(a file outside the root) returned %d references and %d rules, want none", len(refs), len(rules))
	}
}

func TestReferencesReportsAnUnreadableSource(t *testing.T) {
	dir := extract(t, "reference-kinds.txtar")
	result, root := loadDir(t, dir, "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(reference-kinds.txtar): %v", err)
	}

	refuse := func(string) ([]byte, error) { return nil, os.ErrPermission }
	refs, rules, err := References(result, root, refuse, symbols)
	if !errors.Is(err, ErrSource) {
		t.Fatalf("References with a refusing reader error = %v, want one satisfying errors.Is(err, %v)", err, ErrSource)
	}
	if refs != nil || rules != nil {
		t.Errorf("References with a refusing reader returned %d references and %d rules, want none", len(refs), len(rules))
	}
}

func TestRefKindStringNamesEveryKind(t *testing.T) {
	cases := []struct {
		kind RefKind
		want string
	}{
		{RefRead, "read"},
		{RefWrite, "write"},
		{RefCall, "call"},
		{RefTypeUse, "type-use"},
		{RefConversion, "conversion"},
		{RefEmbed, "embed"},
		{RefAssert, "assert"},
		{RefKind(200), "RefKind(200)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("RefKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// kinds maps every symbol of one analysis to its kind, so a test can pick out the
// files and the packages the reference pass named.
func (a analysis) kinds() map[SymbolID]SymbolKind {
	held := make(map[SymbolID]SymbolKind, len(a.symbols))
	for i := range a.symbols {
		held[a.symbols[i].ID] = a.symbols[i].Kind
	}
	return held
}

func TestReferencesRecordsEveryImportOfATargetPackageFromTheImportingFile(t *testing.T) {
	a := analyze(t, "imports.txtar")
	kinds := a.kinds()

	var found []string
	for _, r := range a.refs {
		if kinds[r.From] != KindFile {
			continue
		}
		found = append(found, fmt.Sprintf("%s to %s %s %s:%d:%d",
			a.name(r.From), a.name(r.To), r.Kind, r.Pos.Filename, r.Pos.Line, r.Pos.Column))
	}

	// An import resolves to a package name declared at the import spec, which is
	// a position no symbol is written at, so the identifier walk records nothing
	// and this rule is what gives the file and package kinds an edge to read. The
	// blank import is one of the two: a package imported for its initialization is
	// reached. The set is every reference a file makes, so the two packages the
	// fixture holds for the negative are here by their absence: the standard
	// library package the file also imports is no symbol of the inventory, and the
	// target package no file imports carries no reference at all.
	want := []string{
		"main.go to example.com/imports/registered read main.go:6:2",
		"main.go to example.com/imports/used read main.go:7:2",
	}
	if !slices.Equal(found, want) {
		t.Errorf("References(imports.txtar) recorded %v from a file, want %v", found, want)
	}
}

func TestReferencesLeavesTheCandidateSetToTheDeclarations(t *testing.T) {
	a := analyze(t, "imports.txtar")
	kinds := a.kinds()

	// The import edges reach a package symbol, and a package is not a subject of
	// either relation, so a package every file imports and a package no file
	// imports are alike to the sweep: what the rule added is an edge for the file
	// and package kinds to read, not a change to what is reported.
	for _, c := range New(a.symbols, a.refs, nil).Sweep(Mode{}).Candidates {
		if kind := kinds[c.ID]; kind == KindPackage || kind == KindFile {
			t.Errorf("Sweep over imports.txtar reported the %s %s, want only declarations", kind, a.name(c.ID))
		}
	}
}

// comparisonsPackage is the prefix every reference of the comparison fixture's one
// package carries.
const comparisonsPackage = "go://example.com/comparisons#"

// refNames maps every symbol of one analysis to its reference without the part
// every symbol of the package shares, which is what tells a field from the type of
// the same name where a display name cannot.
func (a analysis) refNames(prefix string) map[SymbolID]string {
	held := make(map[SymbolID]string, len(a.symbols))
	for i := range a.symbols {
		held[a.symbols[i].ID] = strings.TrimPrefix(a.symbols[i].Ref, prefix)
	}
	return held
}

// fieldReads names, per declaration, every field the declaration reads, sorted, so
// a table states one shape's reads without depending on the order two reads at one
// position are kept in.
func (a analysis) fieldReads() map[string][]string {
	names := a.refNames(comparisonsPackage)
	kinds := a.kinds()
	reads := make(map[string][]string)
	for _, r := range a.refs {
		if r.Kind != RefRead || kinds[r.To] != KindField {
			continue
		}
		reads[names[r.From]] = append(reads[names[r.From]], names[r.To])
	}
	for from := range reads {
		slices.Sort(reads[from])
	}
	return reads
}

func TestReferencesRecordsAReadOfEveryFieldAComparisonReads(t *testing.T) {
	reads := analyze(t, "comparisons.txtar").fieldReads()

	cases := map[string][]string{
		// Equality reads every field of the type, and so does its negation.
		"compareEqual":   {"equal.a", "equal.b"},
		"compareUnequal": {"unequal.c"},
		// A map compares its keys, so the key type is read where the map type is
		// written and again at every index into it.
		"table":     {"indexed.d"},
		"readIndex": {"indexed.d"},
		// A switch compares the tag against each case, which is three comparisons
		// at three sites and so three reads of the one field.
		"switchTag": {"tagged.e", "tagged.e", "tagged.e"},
		// The walk follows a field of struct type and an array of them, so a
		// nested field is read once however many ways the outer struct reaches it.
		"compareNested": {"leaf.f", "nested.inner", "nested.list"},
		"compareArray":  {"leaf.f"},
		// An embedded field is a field, and the fields it promotes are read with
		// it, because equality compares the whole of it.
		"compareEmbedding": {"embedded.g", "embedding.embedded", "embedding.own"},
		// The interface-typed field is read and the walk stops there, so no field
		// of the type that satisfies the interface is read: which type a value
		// holds at the comparison is not known here.
		"compareOpaque": {"opaque.held"},
		// The two shapes that read nothing: comparing pointers compares addresses,
		// and an ordered comparison is not defined over a struct.
		"comparePointers": nil,
		"order":           nil,
	}
	for from, want := range cases {
		t.Run(from, func(t *testing.T) {
			if got := reads[from]; !slices.Equal(got, want) {
				t.Errorf("References(comparisons.txtar) recorded %s reading the fields %v, want %v",
					from, got, want)
			}
		})
	}
}

func TestReferencesRecordsAComparisonsReadsAtTheComparisonSite(t *testing.T) {
	a := analyze(t, "comparisons.txtar")
	names := a.refNames(comparisonsPackage)
	kinds := a.kinds()

	sites := make(map[string][]string)
	for _, r := range a.refs {
		if r.Kind != RefRead || kinds[r.To] != KindField {
			continue
		}
		from := names[r.From]
		sites[from] = append(sites[from], fmt.Sprintf("%d:%d", r.Pos.Line, r.Pos.Column))
	}

	cases := map[string][]string{
		// The equality operator's own position, so a maintainer reading the report
		// is sent to the comparison rather than to the declaration.
		"compareEqual": {"5:47", "5:47"},
		// The tag and each case expression, each where it is written.
		"switchTag": {"20:9", "21:7", "23:7"},
		// The key type where the map type is written, and the key expression where
		// the index is written.
		"table":     {"13:16"},
		"readIndex": {"15:51"},
	}
	for from, want := range cases {
		t.Run(from, func(t *testing.T) {
			if got := sites[from]; !slices.Equal(got, want) {
				t.Errorf("References(comparisons.txtar) recorded %s reading fields at %v, want %v",
					from, got, want)
			}
		})
	}
}

package exempt

import (
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// exemptionRows renders one class's answer as one row per retained symbol, the
// declaration's name and the clause the exemption records, and fails the test for
// a row naming another class, a symbol outside the inventory or no site.
func exemptionRows(t *testing.T, in *Input, class Class, found []graph.Exemption) []string {
	t.Helper()
	names := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		names[in.Symbols[i].ID] = in.Symbols[i].Name
	}
	rows := make([]string, 0, len(found))
	for _, e := range found {
		name, held := names[e.ID]
		if !held {
			t.Fatalf("exemption %q names %q, which is no symbol of the inventory", e.Detail, e.ID)
		}
		if e.Class != string(class) {
			t.Errorf("exemption on %s carries class %q, want %q", name, e.Class, class)
		}
		if e.Site.Filename == "" || e.Site.Line == 0 {
			t.Errorf("exemption on %s carries site %q, want a file and a line", name, e.Site)
		}
		rows = append(rows, name+"\t"+e.Detail)
	}
	return rows
}

func TestEnumGroupRetainsEveryMemberOfAGroupWhoseValuesCanArriveByConversion(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		want    []string
	}{
		{
			name:    "a_conversion_method_on_the_type_retains_the_repeated_specifications_too",
			archive: "enum-group-method.txtar",
			want: []string{
				"TierLow\tdeclares a String method",
				"TierMid\tdeclares a String method",
				"TierHigh\tdeclares a String method",
			},
		},
		{
			name:    "a_conversion_from_an_integer_and_a_field_a_decoder_writes",
			archive: "enum-group-wire.txtar",
			want: []string{
				"LevelOff\tconverted from int",
				"LevelWarn\tconverted from int",
				"LevelError\tconverted from int",
				"KindUnknown\tdecoded by encoding/json.Unmarshal",
				"KindFile\tdecoded by encoding/json.Unmarshal",
				"KindDirectory\tdecoded by encoding/json.Unmarshal",
			},
		},
		{
			name:    "no_conversion_method_no_conversion_and_no_decoder",
			archive: "enum-group-plain.txtar",
			want:    []string{},
		},
		{
			name:    "a_const_block_naming_iota_nowhere_is_no_group",
			archive: "enum-group-no-iota.txtar",
			want:    []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := inputOf(t, test.archive, Options{})
			found, err := EnumGroupDetector(in)
			if err != nil {
				t.Fatalf("EnumGroupDetector(%s) error: %v", test.archive, err)
			}
			got := exemptionRows(t, in, EnumGroup, found)
			if !slices.Equal(got, test.want) {
				t.Errorf("EnumGroupDetector(%s) retained\n%v\nwant\n%v", test.archive, got, test.want)
			}
		})
	}
}

func TestEnumGroupLeavesTheUnreferencedMemberOfAPlainGroupASweepCandidate(t *testing.T) {
	const archive = "enum-group-plain.txtar"
	in := inputOf(t, archive, Options{})

	found, err := EnumGroupDetector(in)
	if err != nil {
		t.Fatalf("EnumGroupDetector(%s) error: %v", archive, err)
	}
	if len(found) != 0 {
		t.Fatalf("EnumGroupDetector(%s) retained %v, want no exemption", archive,
			exemptionRows(t, in, EnumGroup, found))
	}

	references, _, err := graph.References(in.Result, in.Root, in.Read, in.Symbols)
	if err != nil {
		t.Fatalf("Setup: graph.References(%s): %v", archive, err)
	}
	roots, _, err := graph.Roots(in.Result, in.Root, in.Read, in.Symbols, graph.RootOptions{PublishedAPI: true})
	if err != nil {
		t.Fatalf("Setup: graph.Roots(%s): %v", archive, err)
	}

	// The class retained nothing on this fixture, so the mode carries no
	// exemption and the sweep answers over the graph alone.
	result := graph.New(in.Symbols, references, roots).Sweep(graph.SweepInput{})

	names := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		names[in.Symbols[i].ID] = in.Symbols[i].Name
	}
	candidates := make(map[string]graph.Candidate, len(result.Candidates))
	for _, c := range result.Candidates {
		candidates[names[c.ID]] = c
	}

	write, held := candidates["ModeWrite"]
	if !held {
		t.Fatalf("Sweep(%s) reported no candidate for ModeWrite, want one; candidates %v",
			archive, slices.Sorted(maps.Keys(candidates)))
	}
	if write.Relation != graph.ReferenceCounting || write.ProductionRefs != 0 || write.TestRefs != 0 {
		t.Errorf("Sweep(%s) candidate ModeWrite = relation %s, production %d, test %d; want %s, 0, 0",
			archive, write.Relation, write.ProductionRefs, write.TestRefs, graph.ReferenceCounting)
	}
	if _, reported := candidates["ModeRead"]; reported {
		t.Errorf("Sweep(%s) reported ModeRead as a candidate, want it live: one reference names it",
			archive)
	}
}

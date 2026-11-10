package exempt

import (
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// exemptionRows renders one class's answer as one row per retained symbol, the
// declaration's name and the clause the exemption records, and fails the test for
// a row naming another class, a symbol outside the inventory or no site.
func exemptionRows(t *testing.T, symbols []inventory.Symbol, class vocab.Exemption, found []Exemption) []string {
	t.Helper()
	names := symbolNames(symbols)
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

func TestEnumGroupRetainsAConstantWhoseDeletionItCannotProveSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		archive string
		site    string // the file every exemption's evidence is written in, where set
		want    []string
	}{
		{
			name:    "a_string_method_retains_nothing_and_a_member_after_the_last_named_one_moves_nothing",
			archive: "enum-group-method.txtar",
			want:    []string{},
		},
		{
			name:    "a_first_specification_the_next_one_repeats_cannot_be_removed_alone",
			archive: "enum-group-wire.txtar",
			want:    []string{"KindUnknown\tdeleting it alone leaves its block unable to compile"},
		},
		{
			name:    "every_rule_of_the_class",
			archive: "enum-group-rules.txtar",
			want: []string{
				"rangedMid\tdeleting it renumbers rangedZ, and a value of ranged is ranged over",
				"convMid\tdeleting it renumbers convZ, and a value of conv is converted from int",
				"shiftedMid\tdeleting it moves shiftedZ, which may then not compile",
				"dividedMid\tdeleting it moves dividedZ, which may then not compile",
				"smallMid\tdeleting it moves smallZ, which may then not compile",
				"kindMid\tdeleting it changes how kindP compares with kindQ",
				"cycOneMid\tdeleting it renumbers cycOneZ, and a value of cycOne is passed to encoding/json.Unmarshal",
				"cycTwoMid\tdeleting it renumbers cycTwoZ, and a value of cycTwo is passed to encoding/json.Unmarshal",
				"genMid\tdeleting it renumbers genZ, and a value of gen is a type argument",
				"b\tdeleting it renumbers c, which code names",
				"jointM1\tdeleting it changes how jointZ compares with joint(1)",
				"jointM2\tdeleting it changes how jointZ compares with joint(1)",
			},
		},
		{
			name:    "no_constant_expression_is_evaluated",
			archive: "enum-group-totality.txtar",
			want: []string{
				"on\tdeleting it moves mid, which may then not compile",
				"cMid\tdeleting it moves cZ, which may then not compile",
				"dMid\tdeleting it moves dHidden, which may then not compile",
				"tickMid\tdeleting it moves tickLast, which may then not compile",
				"lanesNone\tdeleting it alone leaves its block unable to compile",
				"lanesMid\tdeleting it renumbers lanesTwo, and a value of lanes is passed to fmt.Println",
			},
		},
		{
			name:    "a_group_of_a_type_the_target_does_not_declare_is_judged_as_every_constant",
			archive: "enum-group-outside-type.txtar",
			want:    []string{},
		},
		{
			name:    "a_const_block_naming_iota_nowhere",
			archive: "enum-group-no-iota.txtar",
			want:    []string{},
		},
		{
			name:    "a_type_read_by_a_package_and_its_test_variant_is_one_type",
			archive: "enum-group-test-variant.txtar",
			site:    "main_test.go",
			want:    []string{"modeMid\tdeleting it renumbers modeZ, and a value of mode is converted from int"},
		},
		{
			name:    "a_generic_of_a_module_loaded_twice_retains_its_type_argument",
			archive: "enum-group-module-twice.txtar",
			want:    []string{"levelMid\tdeleting it renumbers levelZ, and a value of level is a type argument"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			shared := analysisOf(t, test.archive, Options{})
			symbols := shared.inventory(t)
			found, err := shared.detect(t, vocab.EnumGroup)
			if err != nil {
				t.Fatalf("EnumGroupDetector(%s) error: %v", test.archive, err)
			}
			got := exemptionRows(t, symbols, vocab.EnumGroup, found)
			if !slices.Equal(got, test.want) {
				t.Errorf("EnumGroupDetector(%s) retained\n%q\nwant\n%q", test.archive, got, test.want)
			}
			for _, e := range found {
				if test.site != "" && e.Site.Filename != test.site {
					t.Errorf("EnumGroupDetector(%s) retained %s against %s, want %s", test.archive, e.ID, e.Site, test.site)
				}
			}
		})
	}
}

func TestEnumGroupRecognitionIsTheKindsToo(t *testing.T) {
	t.Parallel()
	in := inputOf(t, "enum-group-totality.txtar", Options{})
	names := symbolNames(in.Symbols)
	var got []string
	for id, owner := range EnumeratedMembers(in.Resolve, in.Facts) {
		got = append(got, names[id]+" of "+owner)
	}
	slices.Sort(got)
	want := []string{
		"c0 of cplx", "cMid of cplx", "cZ of cplx",
		"dA of div", "dHidden of div", "dMid of div",
		"lanesMid of lanes", "lanesNone of lanes", "lanesTwo of lanes",
		"mid of flag", "off of flag", "on of flag", "wide of lanes",
	}
	if !slices.Equal(got, want) {
		t.Errorf("EnumeratedMembers(enum-group-totality.txtar) =\n%q\nwant\n%q (a time.Duration group is no enumerated type)", got, want)
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
			exemptionRows(t, in.Symbols, vocab.EnumGroup, found))
	}

	// The class retained nothing on this fixture, so the sweep answers over the
	// graph alone.
	names := symbolNames(in.Symbols)
	candidates := make(map[string]SweptCandidate)
	for _, c := range SweepCandidates(t, in, roots.RootOptions{PublishedAPI: true}, nil) {
		candidates[names[c.ID]] = c
	}

	write, held := candidates["ModeWrite"]
	if !held {
		t.Fatalf("Sweep(%s) reported no candidate for ModeWrite, want one; candidates %v",
			archive, slices.Sorted(maps.Keys(candidates)))
	}
	if write.Relation != "reference-counting" || write.ProductionRefs != 0 || write.TestRefs != 0 {
		t.Errorf("Sweep(%s) candidate ModeWrite = relation %s, production %d, test %d; want reference-counting, 0, 0",
			archive, write.Relation, write.ProductionRefs, write.TestRefs)
	}
	if _, reported := candidates["ModeRead"]; reported {
		t.Errorf("Sweep(%s) reported ModeRead as a candidate, want it live: one reference names it",
			archive)
	}
}

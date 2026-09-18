package exempt

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// verbRefs spells the references of one type's members in the verbs fixture.
func verbRefs(typeName string, members ...string) []string {
	return qualify("example.com/verbs", typeName, members...)
}

func TestFormatVerbContractRetainsTheMethodEveryVerbFamilyCalls(t *testing.T) {
	in := inputOf(t, "format-verb-firing.txtar", Options{})
	refs := retainedRefs(t, in, FormatVerbContractDetector)

	for _, test := range []struct {
		family   string
		typeName string
		want     []string
	}{
		{family: "the default verb", typeName: "VerbV", want: verbRefs("VerbV", "String")},
		{family: "the string verb through a pointer", typeName: "VerbS", want: verbRefs("VerbS", "String")},
		{family: "the quoted verb with a width and a precision", typeName: "VerbQ", want: verbRefs("VerbQ", "String")},
		{family: "the lower-case hexadecimal verb", typeName: "VerbX", want: verbRefs("VerbX", "String")},
		{family: "the upper-case hexadecimal verb", typeName: "VerbUpperX", want: verbRefs("VerbUpperX", "String")},
		{family: "a form carrying no format string", typeName: "BarePrint", want: verbRefs("BarePrint", "String")},
		{family: "the other form carrying none", typeName: "BarePrintln", want: verbRefs("BarePrintln", "String")},
		{family: "the operand an explicit index selects", typeName: "IndexChosen", want: verbRefs("IndexChosen", "String")},
		{family: "the operand an explicit index steps over", typeName: "IndexSkipped", want: nil},
		{family: "a format string the call computes", typeName: "Computed", want: verbRefs("Computed", "String")},
		{family: "operands spread from a slice", typeName: "Spread", want: verbRefs("Spread", "String")},
		{family: "an operand past the format's last verb", typeName: "Surplus", want: verbRefs("Surplus", "String")},
		{family: "the wrapping verb of the error-construction function", typeName: "Wrapped", want: verbRefs("Wrapped", "Error")},
		{family: "an element of a container", typeName: "Element", want: verbRefs("Element", "String")},
		{family: "a method of the retained name in another shape", typeName: "Shaped", want: nil},
	} {
		t.Run(strings.ReplaceAll(test.family, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("FormatVerbContractDetector(format-verb-firing.txtar) retained, for %s reaching %s,\ngot  %v\nwant %v",
					test.typeName, test.family, got, test.want)
			}
		})
	}
}

func TestFormatVerbContractRetainsNothingWhereNoVerbAsksForAString(t *testing.T) {
	in := inputOf(t, "format-verb-quiet.txtar", Options{})
	if got := retainedRefs(t, in, FormatVerbContractDetector); len(got) > 0 {
		t.Errorf("FormatVerbContractDetector(format-verb-quiet.txtar) retained %v, want nothing", got)
	}
}

func TestFormatVerbContractNamesTheClassTheVerbAndTheSite(t *testing.T) {
	in := inputOf(t, "format-verb-firing.txtar", Options{})
	refs := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		refs[in.Symbols[i].ID] = in.Symbols[i].Ref
	}

	type record struct{ ref, class, site, detail string }
	var got []record
	for _, e := range retained(t, in, FormatVerbContractDetector) {
		got = append(got, record{ref: refs[e.ID], class: e.Class, site: e.Site.String(), detail: e.Detail})
	}

	want := []record{
		{"go://example.com/verbs#BarePrint.String", "format-verb-contract", "bare_forms.go:31:57", "formatted by fmt.Sprint"},
		{"go://example.com/verbs#BarePrintln.String", "format-verb-contract", "bare_forms.go:34:87", "formatted by fmt.Fprintln"},
		{"go://example.com/verbs#Computed.String", "format-verb-contract", "computed.go:17:84", "formatted by fmt.Sprintf under every verb it may carry"},
		{"go://example.com/verbs#VerbV.String", "format-verb-contract", "default_verb.go:17:57", "formatted by fmt.Sprintf under %v"},
		{"go://example.com/verbs#Element.String", "format-verb-contract", "elements.go:17:73", "formatted by fmt.Sprintf under %v"},
		{"go://example.com/verbs#VerbX.String", "format-verb-contract", "hex_verbs.go:31:79", "formatted by fmt.Fprintf under %x"},
		{"go://example.com/verbs#VerbUpperX.String", "format-verb-contract", "hex_verbs.go:34:80", "formatted by fmt.Appendf under %X"},
		{"go://example.com/verbs#IndexChosen.String", "format-verb-contract", "indexed.go:25:39", "formatted by fmt.Sprintf under %s"},
		{"go://example.com/verbs#VerbQ.String", "format-verb-contract", "quoted_verb.go:17:61", "formatted by fmt.Sprintf under %q"},
		{"go://example.com/verbs#Spread.String", "format-verb-contract", "spread.go:15:63", "formatted by fmt.Sprintf under every verb it may carry"},
		{"go://example.com/verbs#VerbS.String", "format-verb-contract", "string_verb.go:17:58", "formatted by fmt.Sprintf under %s"},
		{"go://example.com/verbs#Surplus.String", "format-verb-contract", "surplus.go:15:75", "formatted by fmt.Sprintf past the format's last verb"},
		{"go://example.com/verbs#Wrapped.Error", "format-verb-contract", "wrapped.go:17:68", "formatted by fmt.Errorf under %w"},
	}
	slices.SortFunc(got, func(a, b record) int { return strings.Compare(a.site, b.site) })
	slices.SortFunc(want, func(a, b record) int { return strings.Compare(a.site, b.site) })
	if !slices.Equal(got, want) {
		t.Errorf("FormatVerbContractDetector(format-verb-firing.txtar) recorded\ngot  %+v\nwant %+v", got, want)
	}
}

// TestStringOperandsBindsTheOperandsTheFormattingPackageAsksForAString pins the
// format-string grammar against what the formatting package does with the same
// string: each want below is the set of operand positions the package asks for a
// string, over operands whose string method records the call.
func TestStringOperandsBindsTheOperandsTheFormattingPackageAsksForAString(t *testing.T) {
	for _, test := range []struct {
		format string
		verbs  string
		count  int
		want   []int
	}{
		{format: "%v", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%s", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%q", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%x", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%X", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%d", verbs: stringVerbs, count: 1, want: nil},
		{format: "%T", verbs: stringVerbs, count: 1, want: nil},
		{format: "%p", verbs: stringVerbs, count: 1, want: nil},
		{format: "%#v", verbs: stringVerbs, count: 1, want: nil},
		{format: "%#+v", verbs: stringVerbs, count: 1, want: nil},
		{format: "%+v", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%#x", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%#q", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%d %s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%d%%%s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "100%% done: %s", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%[2]s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%[2]s %s", verbs: stringVerbs, count: 3, want: []int{1, 2}},
		{format: "%[3]s", verbs: stringVerbs, count: 2, want: nil},
		{format: "%-8.4q", verbs: stringVerbs, count: 1, want: []int{0}},
		{format: "%08.3f %s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%*s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%*d %s", verbs: stringVerbs, count: 3, want: []int{2}},
		{format: "%.*s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%*.*s", verbs: stringVerbs, count: 3, want: []int{2}},
		{format: "%6.2[1]f %[2]s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%.[1]s", verbs: stringVerbs, count: 2, want: []int{0}},
		{format: "%.[2]s", verbs: stringVerbs, count: 2, want: []int{1}},
		{format: "%!", verbs: stringVerbs, count: 1, want: nil},
		{format: "%q %x %X %v %s", verbs: stringVerbs, count: 5, want: []int{0, 1, 2, 3, 4}},
		{format: "%w", verbs: stringVerbs, count: 1, want: nil},
		{format: "%w", verbs: stringVerbs + wrapVerb, count: 1, want: []int{0}},
		{format: "%w and %w", verbs: stringVerbs + wrapVerb, count: 2, want: []int{0, 1}},
		{format: "%s: %w", verbs: stringVerbs + wrapVerb, count: 2, want: []int{0, 1}},
	} {
		t.Run(strings.ReplaceAll(test.format, " ", "_")+"/"+test.verbs, func(t *testing.T) {
			var got []int
			for _, op := range stringOperands(test.format, test.count, test.verbs).bound {
				got = append(got, op.at)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("stringOperands(%q, %d, %q) bound %v, want %v", test.format, test.count, test.verbs, got, test.want)
			}
		})
	}
}

// TestStringOperandsReportsHowFarTheVerbsReachedAndWhetherAnIndexAppeared pins
// the two answers the surplus rule reads: the formatting package renders every
// operand past the last one a verb consumed, and stops doing so once the format
// has selected an operand by an explicit index.
func TestStringOperandsReportsHowFarTheVerbsReachedAndWhetherAnIndexAppeared(t *testing.T) {
	for _, test := range []struct {
		format        string
		wantReached   int
		wantReordered bool
	}{
		{format: "", wantReached: 0, wantReordered: false},
		{format: "none", wantReached: 0, wantReordered: false},
		{format: "%d", wantReached: 1, wantReordered: false},
		{format: "%d %s", wantReached: 2, wantReordered: false},
		{format: "%", wantReached: 0, wantReordered: false},
		{format: "%[1]d", wantReached: 1, wantReordered: true},
		{format: "%s %[1]d", wantReached: 1, wantReordered: true},
		{format: "%.[1]s", wantReached: 1, wantReordered: true},
		{format: "%[1]6d", wantReached: 1, wantReordered: true},
		// An index after a width and a precision is no index: the formatting
		// package reads the opening bracket as the verb.
		{format: "%6.2[1]f", wantReached: 1, wantReordered: false},
		{format: "%*d", wantReached: 2, wantReordered: false},
	} {
		t.Run(strings.ReplaceAll(test.format, " ", "_"), func(t *testing.T) {
			scan := stringOperands(test.format, 8, stringVerbs)
			if scan.reached != test.wantReached || scan.reordered != test.wantReordered {
				t.Errorf("stringOperands(%q, 8, %q) reached %d reordered %t, want reached %d reordered %t",
					test.format, stringVerbs, scan.reached, scan.reordered, test.wantReached, test.wantReordered)
			}
		})
	}
}

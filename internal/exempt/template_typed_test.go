package exempt

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// typedClauses is each symbol one class held back by the typed walk of a template
// execution or by the print walk, with the clause it recorded, sorted: the records
// whose clause names a template action or a printing verb.
func typedClauses(t *testing.T, shared *sharedAnalysis, class vocab.Exemption) []string {
	t.Helper()
	symbols := shared.inventory(t)
	refs := make(map[inventory.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}
	var got []string
	for _, e := range retained(t, shared, class) {
		if strings.HasPrefix(e.Detail, "named by {{") || strings.HasPrefix(e.Detail, "formatted by {{") {
			got = append(got, strings.TrimPrefix(refs[e.ID], "go://example.com/typed#")+"\t"+e.Detail)
		}
	}
	slices.Sort(got)
	return slices.Compact(got)
}

func TestATemplateExecutionReadsWhatItsTypedActionsName(t *testing.T) {
	t.Parallel()
	got := typedClauses(t, analysisOf(t, "template-typed.txtar", Options{}), vocab.EncodingReflection)
	want := []string{
		"base.ID\tnamed by {{.ID}}",
		"catalog.ByName\tnamed by {{.ByName.one.Label}}",
		"catalog.Counts\tnamed by {{.Counts}}",
		"computed.Title\tnamed by {{.Title}}",
		"entry.Label\tnamed by {{.ByName.one.Label}}",
		"escaped.Body\tformatted by {{html .}}",
		"filed.Body\tnamed by {{.Body}}",
		"looked.Body\tnamed by {{.Body}}",
		"node.Child\tnamed by {{.Child}}",
		"node.Name\tnamed by {{.Name}}",
		"reassigned.First\tnamed by {{.First}}",
		"reassigned.Second\tnamed by {{.Second}}",
		"tally.Total\tnamed by {{$v.Total}}",
		"wrapped.Note\tnamed by {{.Note}}",
		"wrapped.base\tnamed by {{.ID}}",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the typed walk over template-typed.txtar recorded\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheFormattingWalkReadsWhatFmtPrints(t *testing.T) {
	t.Parallel()
	refs := retainedRefs(t, analysisOf(t, "format-verb-printed.txtar", Options{}), vocab.FormatVerbContract)
	want := qualifiedAll("example.com/printed",
		"formatted.Format", "goSyntax.GoString", "holder.P", "hidden.h", "nested.c", "outer.in",
		"plain.A", "plain.b", "stringy.String", "topPtr.e")
	if !slices.Equal(refs, want) {
		t.Errorf("FormatVerbContractDetector(format-verb-printed.txtar) retained\n%v\nwant\n%v", refs, want)
	}
}

// qualifiedAll spells each type-and-member name as a reference of one fixture
// module, sorted.
func qualifiedAll(module string, names ...string) []string {
	held := make([]string, 0, len(names))
	for _, name := range names {
		held = append(held, "go://"+module+"#"+name)
	}
	slices.Sort(held)
	return held
}

func TestPrintedOperandsReadTheVerbAndTheSharpFlagOfEachOperand(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		format string
		count  int
		want   []operand
	}{
		{"%v %#v", 2, []operand{{at: 0, verb: 'v', under: " under %v"}, {at: 1, verb: 'v', sharpV: true, under: " under %#v"}}},
		{"%+#v %#[1]v %#8v % #v", 1, []operand{
			{at: 0, verb: 'v', sharpV: true, under: " under %#v"},
			{at: 0, verb: 'v', sharpV: true, under: " under %#v"},
		}},
		{"%#x %T %d", 3, []operand{{at: 0, verb: 'x', under: " under %x"}, {at: 2, verb: 'd', under: " under %d"}}},
		{"%s", 2, []operand{{at: 0, verb: 's', under: " under %s"}, {at: 1, verb: 'v', under: " past the format's last verb"}}},
		{"%[2]d", 2, []operand{{at: 1, verb: 'd', under: " under %d"}}},
	} {
		if got := printedOperands(test.format, test.count); !slices.Equal(got, test.want) {
			t.Errorf("printedOperands(%q, %d) = %+v, want %+v", test.format, test.count, got, test.want)
		}
	}
}

package exempt

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// retained runs one detector and returns what it recorded.
func retained(t *testing.T, in *Input, d Detector) []graph.Exemption {
	t.Helper()
	found, err := d(in)
	if err != nil {
		t.Fatalf("detector error: %v", err)
	}
	return found
}

// retainedRefs runs one detector and returns the reference of every symbol it
// held back, once each, sorted.
func retainedRefs(t *testing.T, in *Input, d Detector) []string {
	t.Helper()
	refs := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		refs[in.Symbols[i].ID] = in.Symbols[i].Ref
	}
	var got []string
	for _, e := range retained(t, in, d) {
		ref, held := refs[e.ID]
		if !held {
			t.Errorf("exemption at %s names %s, which the inventory does not hold", e.Site, e.ID)
			continue
		}
		got = append(got, ref)
	}
	slices.Sort(got)
	return slices.Compact(got)
}

// membersOf keeps the references naming a member of one type.
func membersOf(refs []string, typeName string) []string {
	var got []string
	for _, ref := range refs {
		if strings.Contains(ref, "#"+typeName+".") {
			got = append(got, ref)
		}
	}
	return got
}

// qualify spells the references of one type's members in one fixture module.
func qualify(module, typeName string, members ...string) []string {
	want := make([]string, 0, len(members))
	for _, m := range members {
		want = append(want, "go://"+module+"#"+typeName+"."+m)
	}
	slices.Sort(want)
	return want
}

// flowRefs spells the references of one type's members in the flow fixture.
func flowRefs(typeName string, members ...string) []string {
	return qualify("example.com/flow", typeName, members...)
}

func TestEncodingReflectionRetainsTheExportedMethodsAndTheTaggedFieldsOfEveryDestination(t *testing.T) {
	in := inputOf(t, "encoding-reflection-firing.txtar", Options{})
	refs := retainedRefs(t, in, EncodingReflectionDetector)

	for _, test := range []struct {
		destination string
		typeName    string
		want        []string
	}{
		{
			destination: "encoding/json",
			typeName:    "JSONPayload",
			want:        flowRefs("JSONPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "encoding/xml",
			typeName:    "XMLPayload",
			want:        flowRefs("XMLPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "encoding/gob",
			typeName:    "GobPayload",
			want:        flowRefs("GobPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "text/template",
			typeName:    "TextPayload",
			want:        flowRefs("TextPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "html/template",
			typeName:    "HTMLPayload",
			want:        flowRefs("HTMLPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "reflect",
			typeName:    "ReflectPayload",
			want:        flowRefs("ReflectPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "the scan target of one row",
			typeName:    "SQLPayload",
			want:        flowRefs("SQLPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "the scan target of a row set",
			typeName:    "RowsPayload",
			want:        flowRefs("RowsPayload", "Describe", "Name", "secret"),
		},
		{
			destination: "a scan argument that is a value rather than a pointer",
			typeName:    "ValuePayload",
			want:        nil,
		},
		{
			destination: "sort.Interface",
			typeName:    "SortPayload",
			want:        flowRefs("SortPayload", "Describe", "Len", "Less", "Names", "Swap", "secret"),
		},
		{
			destination: "slog.LogValuer",
			typeName:    "LogPayload",
			want:        flowRefs("LogPayload", "Describe", "LogValue", "Name", "secret"),
		},
		{
			destination: "a map of slices of pointers",
			typeName:    "Deep",
			want:        flowRefs("Deep", "Describe", "Inner", "Name", "secret"),
		},
		{
			destination: "the writer of a template execution",
			typeName:    "Sink",
			want:        flowRefs("Sink", "Buffer", "Write"),
		},
		{
			destination: "the type of a field of a type an encoder reaches",
			typeName:    "Inner",
			want:        nil,
		},
	} {
		t.Run(strings.ReplaceAll(test.destination, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-firing.txtar) retained, for %s reaching %s,\ngot  %v\nwant %v",
					test.typeName, test.destination, got, test.want)
			}
		})
	}
}

func TestEncodingReflectionRetainsNothingWhereNoTypeReachesADestination(t *testing.T) {
	in := inputOf(t, "encoding-reflection-quiet.txtar", Options{})
	if got := retainedRefs(t, in, EncodingReflectionDetector); len(got) > 0 {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-quiet.txtar) retained %v, want nothing", got)
	}
}

func TestEncodingReflectionNamesTheClassTheDestinationAndTheSite(t *testing.T) {
	in := inputOf(t, "encoding-reflection-firing.txtar", Options{})
	refs := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		refs[in.Symbols[i].ID] = in.Symbols[i].Ref
	}

	type record struct{ ref, class, site, detail string }
	var got []record
	for _, e := range retained(t, in, EncodingReflectionDetector) {
		if !strings.HasPrefix(refs[e.ID], "go://example.com/flow#JSONPayload.") &&
			!strings.HasPrefix(refs[e.ID], "go://example.com/flow#LogPayload.") {
			continue
		}
		got = append(got, record{ref: refs[e.ID], class: e.Class, site: e.Site.String(), detail: e.Detail})
	}

	want := []record{
		{"go://example.com/flow#JSONPayload.Name", "encoding-reflection", "json.go:18:70", "passed to encoding/json.Marshal"},
		{"go://example.com/flow#JSONPayload.secret", "encoding-reflection", "json.go:18:70", "passed to encoding/json.Marshal"},
		{"go://example.com/flow#JSONPayload.Describe", "encoding-reflection", "json.go:18:70", "passed to encoding/json.Marshal"},
		{"go://example.com/flow#LogPayload.LogValue", "encoding-reflection", "logvalue.go:20:24", "converted to log/slog.LogValuer"},
		{"go://example.com/flow#LogPayload.Describe", "encoding-reflection", "logvalue.go:20:24", "converted to log/slog.LogValuer"},
		{"go://example.com/flow#LogPayload.Name", "encoding-reflection", "logvalue.go:20:24", "converted to log/slog.LogValuer"},
		{"go://example.com/flow#LogPayload.secret", "encoding-reflection", "logvalue.go:20:24", "converted to log/slog.LogValuer"},
	}
	slices.SortFunc(got, func(a, b record) int { return strings.Compare(a.ref+a.site, b.ref+b.site) })
	slices.SortFunc(want, func(a, b record) int { return strings.Compare(a.ref+a.site, b.ref+b.site) })
	if !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-firing.txtar) recorded, for the two types,\ngot  %+v\nwant %+v", got, want)
	}
}

package exempt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec/v2"
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

// What the class retains is per destination: the tagged and exported fields of the
// value for every one of them, and the exported methods only where the destination
// reaches a method. The comparison of two values by reflection reads fields alone,
// while every other entry point of that package hands out a value a method is
// reachable from.
func TestEncodingReflectionRetainsWhatEachDestinationReads(t *testing.T) {
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
			want:        flowRefs("JSONPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "encoding/xml",
			typeName:    "XMLPayload",
			want:        flowRefs("XMLPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "encoding/gob",
			typeName:    "GobPayload",
			want:        flowRefs("GobPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "text/template",
			typeName:    "TextPayload",
			want:        flowRefs("TextPayload", "Describe", "Extra", "Name", "secret"),
		},
		{
			destination: "html/template",
			typeName:    "HTMLPayload",
			want:        flowRefs("HTMLPayload", "Describe", "Extra", "Name", "secret"),
		},
		{
			destination: "reflect",
			typeName:    "ReflectPayload",
			want:        flowRefs("ReflectPayload", "Describe", "Extra", "Name", "secret"),
		},
		{
			destination: "the comparison of two values by reflection",
			typeName:    "ComparedPayload",
			want:        flowRefs("ComparedPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "the scan target of one row",
			typeName:    "SQLPayload",
			want:        flowRefs("SQLPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "the scan target of a row set",
			typeName:    "RowsPayload",
			want:        flowRefs("RowsPayload", "Extra", "Name", "secret"),
		},
		{
			destination: "a scan argument that is a value rather than a pointer",
			typeName:    "ValuePayload",
			want:        nil,
		},
		{
			destination: "sort.Interface",
			typeName:    "SortPayload",
			want:        flowRefs("SortPayload", "Describe", "Extra", "Len", "Less", "Names", "Swap", "secret"),
		},
		{
			destination: "a structured-logging call",
			typeName:    "LogPayload",
			want:        flowRefs("LogPayload", "Describe", "Extra", "LogValue", "Name", "secret"),
		},
		{
			destination: "a map of slices of pointers",
			typeName:    "Deep",
			want:        flowRefs("Deep", "Extra", "Inner", "Name", "secret"),
		},
		{
			destination: "the writer of a template execution",
			typeName:    "Sink",
			want:        flowRefs("Sink", "Buffer", "Extra", "Write"),
		},
		{
			destination: "the field of a type an encoder reaches",
			typeName:    "Inner",
			want:        flowRefs("Inner", "Label"),
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

// methodRefs spells the references of one type's members in the methods fixture.
func methodRefs(typeName string, members ...string) []string {
	return qualify("example.com/methods", typeName, members...)
}

// An encoder resolves a closed set of methods by name on a value it walks, so a
// destination of that family retains those methods beside the fields. What it retains
// is the direction it is: an entry point that encodes resolves no method that decodes,
// one that names neither direction resolves both, and a method of neither set is
// retained by nothing.
func TestEncodingReflectionRetainsTheMethodsAnEncoderResolvesByName(t *testing.T) {
	in := inputOf(t, "encoding-reflection-methods.txtar", Options{})
	refs := retainedRefs(t, in, EncodingReflectionDetector)

	for _, test := range []struct {
		destination string
		typeName    string
		want        []string
	}{
		{
			destination: "the JSON encoder",
			typeName:    "Marshalled",
			want:        methodRefs("Marshalled", "AppendText", "MarshalJSON", "MarshalJSONTo", "MarshalText", "Name"),
		},
		{
			destination: "the JSON decoder",
			typeName:    "Unmarshalled",
			want:        methodRefs("Unmarshalled", "Name", "UnmarshalJSON", "UnmarshalText"),
		},
		{
			destination: "the XML encoder",
			typeName:    "Element",
			want:        methodRefs("Element", "MarshalXML", "MarshalXMLAttr", "Name"),
		},
		{
			destination: "the gob encoder",
			typeName:    "Record",
			want:        methodRefs("Record", "GobEncode", "MarshalBinary", "Name"),
		},
		{
			destination: "a wrapper that forwards its erased parameter to the JSON encoder",
			typeName:    "Wrapped",
			want:        methodRefs("Wrapped", "MarshalJSON", "Name"),
		},
		{
			destination: "an entry point that names neither direction",
			typeName:    "Registered",
			want:        methodRefs("Registered", "GobDecode", "GobEncode", "Name"),
		},
	} {
		t.Run(strings.ReplaceAll(test.destination, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-methods.txtar) retained, for %s reaching %s,\ngot  %v\nwant %v",
					test.typeName, test.destination, got, test.want)
			}
		})
	}
}

// reachRefs spells the references of one type's members in the reach fixture.
func reachRefs(typeName string, members ...string) []string {
	return qualify("example.com/reach", typeName, members...)
}

func TestEncodingReflectionReachesTheTypesTheMembersOfAnArgumentCarry(t *testing.T) {
	in := inputOf(t, "encoding-reflection-reach.txtar", Options{})
	refs := retainedRefs(t, in, EncodingReflectionDetector)

	for _, test := range []struct {
		reached  string
		typeName string
		want     []string
	}{
		{
			reached:  "the argument of the encoder",
			typeName: "Root",
			want:     reachRefs("Root", "Mid", "Opaque", "Printer"),
		},
		{
			reached:  "one struct level below the argument",
			typeName: "Middle",
			want:     reachRefs("Middle", "Count", "Leaf"),
		},
		{
			reached:  "two struct levels below the argument",
			typeName: "Leaf",
			want:     reachRefs("Leaf", "Extra", "Label"),
		},
		{
			reached:  "the value a field typed as the empty interface carries",
			typeName: "Opaque",
			want:     nil,
		},
		{
			reached:  "the value a field typed as an interface carries",
			typeName: "Printed",
			want:     nil,
		},
		{
			reached:  "an argument carrying an embedded field",
			typeName: "Wrapper",
			want:     reachRefs("Wrapper", "Count", "Embedded"),
		},
		{
			reached:  "the type embedded in an argument",
			typeName: "Embedded",
			want:     reachRefs("Embedded", "Extra", "Tag"),
		},
		{
			reached:  "a generic argument",
			typeName: "Box",
			want:     reachRefs("Box", "Count", "V"),
		},
		{
			reached:  "the type a generic argument is instantiated at",
			typeName: "Boxed",
			want:     reachRefs("Boxed", "Extra", "Label"),
		},
		{
			reached:  "a type that holds a value of itself",
			typeName: "Node",
			want:     reachRefs("Node", "Label", "Next"),
		},
	} {
		t.Run(strings.ReplaceAll(test.reached, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-reach.txtar) retained, for %s reaching %s,\ngot  %v\nwant %v",
					test.typeName, test.reached, got, test.want)
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
		{"go://example.com/flow#JSONPayload.Extra", "encoding-reflection", "json.go:18:70", "passed to encoding/json.Marshal"},
		{"go://example.com/flow#JSONPayload.secret", "encoding-reflection", "json.go:18:70", "passed to encoding/json.Marshal"},
		{"go://example.com/flow#LogPayload.LogValue", "encoding-reflection", "logging.go:21:68", "passed to log/slog.Info"},
		{"go://example.com/flow#LogPayload.Describe", "encoding-reflection", "logging.go:21:68", "passed to log/slog.Info"},
		{"go://example.com/flow#LogPayload.Name", "encoding-reflection", "logging.go:21:68", "passed to log/slog.Info"},
		{"go://example.com/flow#LogPayload.Extra", "encoding-reflection", "logging.go:21:68", "passed to log/slog.Info"},
		{"go://example.com/flow#LogPayload.secret", "encoding-reflection", "logging.go:21:68", "passed to log/slog.Info"},
	}
	slices.SortFunc(got, func(a, b record) int { return strings.Compare(a.ref+a.site, b.ref+b.site) })
	slices.SortFunc(want, func(a, b record) int { return strings.Compare(a.ref+a.site, b.ref+b.site) })
	if !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-firing.txtar) recorded, for the two types,\ngot  %+v\nwant %+v", got, want)
	}
}

// opaqueRefs spells the references of one type's members in the opaque fixture.
func opaqueRefs(typeName string, members ...string) []string {
	return qualify("example.com/opaque", typeName, members...)
}

// A value that crosses out of the analysed program through a parameter typed as the
// empty interface flows with the full retained set, because the callee's body is not
// in the program, the parameter keeps nothing of the value's type, and whatever the
// callee does with it reads its fields and may call its exported methods. A function
// of the target that hands such a parameter on is a destination of its own, to a
// fixpoint. A parameter typed as the value's own type is not one at all, and neither
// is one typed as an interface that declares a method: that is a conversion the
// conversion set records, and the methods the interface requires are what
// interface-satisfaction retains.
func TestEncodingReflectionRetainsWhatCrossesOutOfTheProgram(t *testing.T) {
	in := inputOf(t, "encoding-reflection-opaque.txtar", Options{})
	refs := retainedRefs(t, in, EncodingReflectionDetector)

	for _, test := range []struct {
		crossing string
		typeName string
		want     []string
	}{
		{
			crossing: "a parameter typed as an interface that declares a method",
			typeName: "Copied",
			want:     nil,
		},
		{
			crossing: "a method of a package the load did not read",
			typeName: "Crossing",
			want:     opaqueRefs("Crossing", "Describe", "Extra", "Inner", "Name", "secret"),
		},
		{
			crossing: "a type the crossing value's fields reach",
			typeName: "Nested",
			want:     opaqueRefs("Nested", "Label", "Title"),
		},
		{
			crossing: "a function of the target that hands its own erased parameter to such a method",
			typeName: "Wrapped",
			want:     opaqueRefs("Wrapped", "Describe", "Extra", "Name", "secret"),
		},
		{
			crossing: "the same method as a pointer",
			typeName: "Pointed",
			want:     opaqueRefs("Pointed", "Describe", "Extra", "Name", "secret"),
		},
		{
			crossing: "a type parameter of a function of a package the load did not read",
			typeName: "Sought",
			want:     nil,
		},
		{
			crossing: "a function of the target that hands its parameter to such a function",
			typeName: "Chained",
			want:     opaqueRefs("Chained", "Describe", "Extra", "Name", "secret"),
		},
		{
			crossing: "a function of the target that hands its parameter to an encoder",
			typeName: "Encoded",
			want:     opaqueRefs("Encoded", "Extra", "Name", "secret"),
		},
		{
			crossing: "a function of the target typed as the value's own type",
			typeName: "Concrete",
			want:     nil,
		},
		{
			crossing: "the formatting package, whose destinations another class names",
			typeName: "Printed",
			want:     nil,
		},
	} {
		t.Run(strings.ReplaceAll(test.crossing, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-opaque.txtar) retained, for %s crossing into %s,\ngot  %v\nwant %v",
					test.typeName, test.crossing, got, test.want)
			}
		})
	}
}

// The detail names the callee the value was handed to, which is the immediate one:
// a wrapper's caller reads the wrapper's name at its own call, the way the
// format-verb class names the print wrapper it found.
func TestEncodingReflectionNamesTheCalleeAValueCrossedInto(t *testing.T) {
	in := inputOf(t, "encoding-reflection-opaque.txtar", Options{})
	refs := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		refs[in.Symbols[i].ID] = in.Symbols[i].Ref
	}

	got := make(map[string]string)
	for _, e := range retained(t, in, EncodingReflectionDetector) {
		if ref, held := refs[e.ID]; held {
			got[ref] = e.Detail
		}
	}
	for ref, want := range map[string]string{
		"go://example.com/opaque#Crossing.Name": "passed to (*sync.Map).Store",
		"go://example.com/opaque#Nested.Label":  "passed to (*sync.Map).Store",
		"go://example.com/opaque#Wrapped.Name":  "passed to example.com/opaque.respond",
		"go://example.com/opaque#Chained.Name":  "passed to example.com/opaque.relay",
		"go://example.com/opaque#Encoded.Name":  "passed to example.com/opaque.keep",
	} {
		if got[ref] != want {
			t.Errorf("EncodingReflectionDetector(encoding-reflection-opaque.txtar) records %s with detail %q, want %q",
				ref, got[ref], want)
		}
	}
}

// A value the formatting package formats is recorded once, by the class whose
// destination table names that package. The two classes would otherwise spell one
// fact two ways, and two spellings of a detail are two records.
func TestFormatVerbContractAloneRecordsAnOperandOfTheFormattingPackage(t *testing.T) {
	in := inputOf(t, "encoding-reflection-opaque.txtar", Options{})

	formatted := membersOf(retainedRefs(t, in, FormatVerbContractDetector), "Printed")
	if want := opaqueRefs("Printed", "String"); !slices.Equal(formatted, want) {
		t.Errorf("FormatVerbContractDetector(encoding-reflection-opaque.txtar) retained %v for Printed, want %v",
			formatted, want)
	}
	if encoded := membersOf(retainedRefs(t, in, EncodingReflectionDetector), "Printed"); len(encoded) > 0 {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-opaque.txtar) retained %v for Printed, want nothing: the formatting package is a destination the format-verb class names",
			encoded)
	}
}

// The mechanism text of the class, as the Contract states it for this language. The
// class implements this text; a pin bump that moves it must be read against the
// destination table before this literal moves with it.
const encodingReflectionMechanismSHA256 = "d31a6053bf460ab76f08190db5a3d13bdca3af17f7df488add83a3736e83ab1d"

func TestEncodingReflectionImplementsTheContractsMechanismText(t *testing.T) {
	body, err := spec.Contract.ReadFile("contract/exemptions.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exemptions.json from the contract: %v", err)
	}
	var document struct {
		Exemptions []struct {
			Class     string            `json:"class"`
			Mechanism map[string]string `json:"mechanism"`
		} `json:"exemptions"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/exemptions.json: %v", err)
	}
	stated := ""
	for _, one := range document.Exemptions {
		if one.Class == string(EncodingReflection) {
			stated = one.Mechanism["go"]
		}
	}
	if stated == "" {
		t.Fatalf("Setup: contract/exemptions.json states no Go mechanism for %s, so this test pins nothing",
			EncodingReflection)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(stated)))
	if sum != encodingReflectionMechanismSHA256 {
		t.Errorf("the Contract's mechanism text moved; re-read it against encoding_reflection.go's destination table before updating this literal.\nsha256 %s, want %s\ntext:\n%s",
			sum, encodingReflectionMechanismSHA256, stated)
	}
}

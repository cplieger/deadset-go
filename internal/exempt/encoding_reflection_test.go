package exempt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec/v6"
)

// retained is what one class's detector recorded over a shared analysis.
func retained(t *testing.T, shared *sharedAnalysis, class Class) []graph.Exemption {
	t.Helper()
	found, err := shared.detect(t, class)
	if err != nil {
		t.Fatalf("detector error: %v", err)
	}
	return found
}

// retainedRefs is the reference of every symbol one class's detector held back, once
// each, sorted.
func retainedRefs(t *testing.T, shared *sharedAnalysis, class Class) []string {
	t.Helper()
	symbols := shared.inventory(t)
	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}
	var got []string
	for _, e := range retained(t, shared, class) {
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
// reaches a method. Reflection reads fields alone in a program that finds no method
// through it, and a structured-logging call resolves LogValue alone.
func TestEncodingReflectionRetainsWhatEachDestinationReads(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-firing.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

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
			want:        flowRefs("ReflectPayload", "Extra", "Name", "secret"),
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
			want:        flowRefs("LogPayload", "Extra", "LogValue", "Name", "secret"),
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
// destination of that family retains those methods. What it retains is the direction
// it is: an entry point that encodes reads fields and resolves no method that decodes,
// one that decodes fills fields through reflection and so retains none, one that names
// neither direction retains both, and a method of neither set is retained by nothing.
func TestEncodingReflectionRetainsTheMethodsAnEncoderResolvesByName(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-methods.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

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
			want:        methodRefs("Unmarshalled", "UnmarshalJSON", "UnmarshalText"),
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

// A program that makes its JSON decoder refuse a document naming a member the type
// lacks retains, at a JSON decoding entry point, the fields an encoder reads, because
// deleting one then changes which documents decode. A decoder of another package is
// unaffected.
func TestEncodingReflectionRetainsTheFieldsARefusingJSONDecoderReads(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "encoding-reflection-unknown-members.txtar", Options{}), EncodingReflection)

	for typeName, want := range map[string][]string{
		"Strict": qualify("example.com/strict", "Strict", "Extra", "Name"),
		"Loose":  nil,
	} {
		if got := membersOf(refs, typeName); !slices.Equal(got, want) {
			t.Errorf("EncodingReflectionDetector(encoding-reflection-unknown-members.txtar) retained, for %s,\ngot  %v\nwant %v",
				typeName, got, want)
		}
	}
}

// A program that finds a method of a reflected value by name may call any exported
// method of a value it hands to reflection, so each such value keeps them.
func TestEncodingReflectionRetainsTheExportedMethodsWhereReflectionFindsMethods(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "encoding-reflection-method-finder.txtar", Options{}), EncodingReflection)

	want := qualify("example.com/finder", "Found", "Describe", "Extra", "Name")
	if got := membersOf(refs, "Found"); !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-method-finder.txtar) retained, for Found,\ngot  %v\nwant %v", got, want)
	}
}

// An outside package's import closure names an interface only where implementing it
// is decidable: a generic interface no instantiation names, and an interface whose
// type set is not its method set, a type term or comparable among them, only
// constrain a type parameter.
func TestDeclaredInterfacesKeepsTheInterfacesAValueCanSatisfy(t *testing.T) {
	const source = `package p

type Plain interface{ M() }
type Generic[T any] interface{ M() T }
type Terms interface{ ~int; M() }
type Comparable interface{ comparable; M() }
type Empty interface{}
type Alias = Plain
type hidden interface{ N() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", source, 0)
	if err != nil {
		t.Fatalf("Setup: parse: %v", err)
	}
	pkg, err := new(types.Config).Check("example.com/p", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatalf("Setup: check: %v", err)
	}
	for _, tc := range []struct {
		want  []string
		every bool
	}{
		{every: false, want: []string{"interface{M()}"}},
		{every: true, want: []string{"interface{M()}", "interface{N()}"}},
	} {
		var got []string
		for _, iface := range declaredInterfaces(pkg, tc.every) {
			got = append(got, types.TypeString(iface, nil))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("declaredInterfaces(example.com/p, %t) = %v, want %v", tc.every, got, tc.want)
		}
	}
}

// reachRefs spells the references of one type's members in the reach fixture.
func reachRefs(typeName string, members ...string) []string {
	return qualify("example.com/reach", typeName, members...)
}

func TestEncodingReflectionReachesTheTypesTheMembersOfAnArgumentCarry(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-reach.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

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
			reached:  "the value the program stores in a field typed as the empty interface",
			typeName: "Opaque",
			want:     reachRefs("Opaque", "Extra", "Label"),
		},
		{
			reached:  "the value the program stores in a field typed as an interface",
			typeName: "Printed",
			want:     reachRefs("Printed", "Extra", "Label"),
		},
		{
			reached:  "a value appended to a field holding interface elements",
			typeName: "Appended",
			want:     reachRefs("Appended", "Extra", "Label"),
		},
		{
			reached:  "a value stored at an index of a map field holding interface values",
			typeName: "Indexed",
			want:     reachRefs("Indexed", "Extra", "Label"),
		},
		{
			reached:  "an element of a slice literal stored in a field",
			typeName: "Listed",
			want:     reachRefs("Listed", "Extra", "Label"),
		},
		{
			reached:  "an argument a parameter stored in a field stands for, through a second function",
			typeName: "Relayed",
			want:     reachRefs("Relayed", "Extra", "Label"),
		},
		{
			reached:  "a value only a parameter no call passes would store",
			typeName: "Ignored",
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

func TestEncodingReflectionReachesTheTypesStoredInAContainerArgument(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-container.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)
	containerRefs := func(typeName string, members ...string) []string {
		return qualify("example.com/container", typeName, members...)
	}

	for _, test := range []struct {
		reached  string
		typeName string
		want     []string
	}{
		{
			reached:  "a value a setter stores in a map field a renderer passes on",
			typeName: "Pagination",
			want:     containerRefs("Pagination", "HasNext", "LastOffset"),
		},
		{
			reached:  "a value of the map literal stored in that field",
			typeName: "Menu",
			want:     containerRefs("Menu", "Label", "Title"),
		},
		{
			reached:  "a value stored at an index of a local map",
			typeName: "Indexed",
			want:     containerRefs("Indexed", "Name", "Upper"),
		},
		{
			reached:  "an element of a slice literal written at the call",
			typeName: "Listed",
			want:     containerRefs("Listed", "Name", "Upper"),
		},
		{
			reached:  "a value stored in a map no destination receives",
			typeName: "Unrendered",
			want:     nil,
		},
	} {
		t.Run(test.typeName, func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-container.txtar) retained, for %s reaching %s,\ngot  %v\nwant %v",
					test.typeName, test.reached, got, test.want)
			}
		})
	}
}

func TestEncodingReflectionRetainsNothingWhereNoTypeReachesADestination(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-quiet.txtar", Options{})
	if got := retainedRefs(t, shared, EncodingReflection); len(got) > 0 {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-quiet.txtar) retained %v, want nothing", got)
	}
}

func TestEncodingReflectionNamesTheClassTheDestinationAndTheSite(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-firing.txtar", Options{})
	symbols := shared.inventory(t)
	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}

	type record struct{ ref, class, site, detail string }
	var got []record
	for _, e := range retained(t, shared, EncodingReflection) {
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
// empty interface keeps its fields and the methods the callee's package can name: the
// methods by which it satisfies an interface of that package's import closure, and
// every exported method where that closure holds a template engine. A function of the
// target that hands such a parameter on is a destination of its own, to a fixpoint. A
// parameter typed as the value's own type is not one at all, and neither is one typed
// as an interface that declares a method, which interface-satisfaction answers.
func TestEncodingReflectionRetainsWhatCrossesOutOfTheProgram(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-opaque.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

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
			want:     opaqueRefs("Crossing", "Extra", "Inner", "Name", "secret"),
		},
		{
			crossing: "a type the crossing value's fields reach",
			typeName: "Nested",
			want:     opaqueRefs("Nested", "Label"),
		},
		{
			crossing: "a function of the target that hands its own erased parameter to such a method",
			typeName: "Wrapped",
			want:     opaqueRefs("Wrapped", "Extra", "Name", "secret"),
		},
		{
			crossing: "the same method as a pointer",
			typeName: "Pointed",
			want:     opaqueRefs("Pointed", "Extra", "Name", "secret"),
		},
		{
			crossing: "a type parameter of a function of a package the load did not read",
			typeName: "Sought",
			want:     nil,
		},
		{
			crossing: "a function of the target that hands its parameter to such a function",
			typeName: "Chained",
			want:     opaqueRefs("Chained", "Extra", "Name", "secret"),
		},
		{
			crossing: "a function of the target that hands its parameter to an encoder",
			typeName: "Encoded",
			want:     opaqueRefs("Encoded", "Extra", "Name", "secret"),
		},
		{
			crossing: "a method of a package declaring an interface the value satisfies",
			typeName: "Locked",
			want:     opaqueRefs("Locked", "Lock", "Name", "Unlock"),
		},
		{
			crossing: "a function of a package whose import closure holds a template engine",
			typeName: "Served",
			want:     opaqueRefs("Served", "Hello", "Name", "Other"),
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

// keysRefs spells the references of one type's members in the keys fixture.
func keysRefs(typeName string, members ...string) []string {
	return qualify("example.com/keys", typeName, members...)
}

// An encoder skips a field whose tag names the key -, and encodes one whose tag names
// "-," under the name -. An XML decoder reads the XMLName field to match the element
// and only writes the others. A struct crosses out of the program inside a slice, an
// array or a map the way it crosses alone.
func TestEncodingReflectionRetainsWhatAnEncoderReadsByKey(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-keys.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

	for _, test := range []struct {
		desc     string
		typeName string
		want     []string
	}{
		{desc: "a field tagged with the key - is skipped", typeName: "Record", want: keysRefs("Record", "Dash", "ID")},
		{desc: "a decoded XMLName field is read", typeName: "Feed", want: keysRefs("Feed", "XMLName")},
		{desc: "the element of a slice crosses", typeName: "Listed", want: keysRefs("Listed", "Name")},
		{desc: "the value of a map crosses", typeName: "Mapped", want: keysRefs("Mapped", "ID")},
		{desc: "a slice that crosses nothing", typeName: "Kept", want: nil},
	} {
		t.Run(test.typeName, func(t *testing.T) {
			if got := membersOf(refs, test.typeName); !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-keys.txtar) retained, for %s (%s),\ngot  %v\nwant %v",
					test.typeName, test.desc, got, test.want)
			}
		})
	}
}

// anonymousRefs spells the references of one type's members in the anonymous fixture.
func anonymousRefs(typeName string, members ...string) []string {
	return qualify("example.com/anonymous", typeName, members...)
}

// An encoder walks the fields of a struct type with no name as it walks a defined
// struct's, at the value handed to it and in a field of a type it reaches.
func TestEncodingReflectionWalksTheFieldsOfAStructTypeWithNoName(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-anonymous.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

	for _, test := range []struct {
		desc     string
		typeName string
		want     []string
	}{
		{desc: "a field of the value decoded into", typeName: "Keys", want: anonymousRefs("Keys", "UnmarshalJSON")},
		{desc: "a field of a field of the value encoded", typeName: "Stamp", want: anonymousRefs("Stamp", "MarshalText")},
		{desc: "a field of a defined type's field", typeName: "Mark", want: anonymousRefs("Mark", "MarshalText")},
		{desc: "the fields of a defined type's field, and none of a skipped one", typeName: "Envelope", want: anonymousRefs("Envelope", "Inner", "Inner.Count", "Inner.Mark")},
		{desc: "a value no destination reaches", typeName: "Unused", want: nil},
	} {
		t.Run(test.typeName, func(t *testing.T) {
			if got := membersOf(refs, test.typeName); !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-anonymous.txtar) retained, for %s (%s),\ngot  %v\nwant %v",
					test.typeName, test.desc, got, test.want)
			}
		})
	}
}

// A dependency's function is read under the build context the run loads with, its
// tags and cgo disabled, so the body a constraint selects decides whether the
// function only decodes.
func TestEncodingReflectionReadsADependencyUnderTheConfigurationsTags(t *testing.T) {
	for _, test := range []struct {
		desc string
		tags []string
		want []string
	}{
		{desc: "the body that also prints", want: qualify("example.com/tagged", "Settings", "String", "Theme")},
		{desc: "the body that only decodes", tags: []string{"wrapped"}},
	} {
		t.Run(test.desc, func(t *testing.T) {
			in := inputOf(t, "encoding-reflection-tagged.txtar", Options{}, test.tags...)
			found, err := EncodingReflectionDetector(in)
			if err != nil {
				t.Fatalf("EncodingReflectionDetector(encoding-reflection-tagged.txtar, tags %v): %v", test.tags, err)
			}
			refs := make(map[graph.SymbolID]string, len(in.Symbols))
			for i := range in.Symbols {
				refs[in.Symbols[i].ID] = in.Symbols[i].Ref
			}
			var got []string
			for _, e := range found {
				got = append(got, refs[e.ID])
			}
			slices.Sort(got)
			if got = slices.Compact(got); !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-tagged.txtar, tags %v) retained\ngot  %v\nwant %v",
					test.tags, got, test.want)
			}
		})
	}
}

// outsideRefs spells the references of one type's members in the outside fixture.
func outsideRefs(typeName string, members ...string) []string {
	return qualify("example.com/outside", typeName, members...)
}

// A value crossing into a dependency keeps the methods by which it satisfies an
// exported interface of the callee's package or of a package that imports it, and not
// one of an unexported interface. A value returned through a method of an interface
// the dependency declares crosses the same way. A value only decoded into is written,
// so it keeps nothing; one the callee also hands to a destination keeps what that
// destination reads. A function that never uses its parameter, or has no body, is no
// decoder, so a value handed to it crosses.
func TestEncodingReflectionRetainsWhatADependencyCanName(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-outside.txtar", Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

	for _, test := range []struct {
		desc     string
		typeName string
		want     []string
	}{
		{desc: "registered with a function taking the empty interface", typeName: "Record", want: outsideRefs("Record", "Describe", "Label", "Name", "Weight")},
		{desc: "returned through a method of the dependency's interface", typeName: "Service", want: outsideRefs("Service", "Addr", "Label", "Name")},
		{desc: "decoded through a method that only decodes", typeName: "Settings", want: nil},
		{desc: "decoded through a method that also prints", typeName: "Profile", want: outsideRefs("Profile", "Email", "String")},
		{desc: "handed to a function that never uses its parameter", typeName: "Ignored", want: outsideRefs("Ignored", "Label", "Note")},
		{desc: "handed to a function with no body", typeName: "Discarded", want: outsideRefs("Discarded", "Label", "Note")},
		{desc: "handed to a function that only stores it", typeName: "Kept", want: outsideRefs("Kept", "Label", "Note")},
	} {
		t.Run(test.typeName, func(t *testing.T) {
			if got := membersOf(refs, test.typeName); !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-outside.txtar) retained, for %s (%s),\ngot  %v\nwant %v",
					test.typeName, test.desc, got, test.want)
			}
		})
	}
}

// A standard container stores a value and hands it back to the program unchanged,
// so the value keeps no field and only the methods by which it satisfies an
// interface of the container's own package, whether the program stores it directly
// or through a function of its own.
func TestEncodingReflectionRetainsWhatAStandardContainerReads(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "encoding-reflection-standard-containers.txtar", Options{}), EncodingReflection)

	for _, test := range []struct {
		desc     string
		typeName string
		want     []string
	}{
		{desc: "stored in a sync.Map", typeName: "entry", want: qualify("example.com/stored", "entry", "Lock", "Unlock")},
		{desc: "the type of a field of a stored value", typeName: "guard", want: nil},
		{desc: "stored in an atomic.Value", typeName: "role", want: nil},
		{desc: "the value of a context", typeName: "tracer", want: qualify("example.com/stored", "tracer", "String")},
		{desc: "stored through a function of the program", typeName: "boxed", want: qualify("example.com/stored", "boxed", "Lock", "Unlock")},
	} {
		t.Run(test.typeName, func(t *testing.T) {
			if got := membersOf(refs, test.typeName); !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(encoding-reflection-standard-containers.txtar) retained, for %s (%s),\ngot  %v\nwant %v",
					test.typeName, test.desc, got, test.want)
			}
		})
	}
}

// The detail names the callee the value was handed to, which is the immediate one:
// a wrapper's caller reads the wrapper's name at its own call, the way the
// format-verb class names the print wrapper it found.
func TestEncodingReflectionNamesTheCalleeAValueCrossedInto(t *testing.T) {
	shared := analysisOf(t, "encoding-reflection-opaque.txtar", Options{})
	symbols := shared.inventory(t)
	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}

	got := make(map[string]string)
	for _, e := range retained(t, shared, EncodingReflection) {
		if ref, held := refs[e.ID]; held {
			got[ref] = e.Detail
		}
	}
	for ref, want := range map[string]string{
		"go://example.com/opaque#Crossing.Name": "passed to (*sync.Pool).Put",
		"go://example.com/opaque#Nested.Label":  "passed to (*sync.Pool).Put",
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
	shared := analysisOf(t, "encoding-reflection-opaque.txtar", Options{})

	formatted := membersOf(retainedRefs(t, shared, FormatVerbContract), "Printed")
	if want := opaqueRefs("Printed", "String"); !slices.Equal(formatted, want) {
		t.Errorf("FormatVerbContractDetector(encoding-reflection-opaque.txtar) retained %v for Printed, want %v",
			formatted, want)
	}
	if encoded := membersOf(retainedRefs(t, shared, EncodingReflection), "Printed"); len(encoded) > 0 {
		t.Errorf("EncodingReflectionDetector(encoding-reflection-opaque.txtar) retained %v for Printed, want nothing: the formatting package is a destination the format-verb class names",
			encoded)
	}
}

// The mechanism text of the class, as the Contract states it for this language. The
// class implements this text; a pin bump that moves it must be read against the
// destination table before this literal moves with it.
const encodingReflectionMechanismSHA256 = "25902c8bf950966ab032ad8bf9c429f42a9a2b727fdd179e7b45118804f01852"

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

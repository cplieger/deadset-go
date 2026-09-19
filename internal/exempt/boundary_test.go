package exempt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// consumerArchive is the fixture whose program is a target and a module the scope
// declares as a consumer of it.
const consumerArchive = "encoding-reflection-consumer.txtar"

// wireRefs spells the references of one type's members in the consumer fixture's
// target module.
func wireRefs(typeName string, members ...string) []string {
	return qualify("example.com/target", typeName, members...)
}

// A consumer's function that hands its own parameter typed as the empty interface to
// a destination is a wrapper of the program exactly as the target's own is, so a
// target value handed to it is retained: the consumer is inside the program, and what
// the wrapper hands the value to is what reads it. A consumer function that forwards
// the parameter to nothing, and one whose parameter is concrete, are no wrappers, so a
// value reaching only those crosses nothing.
func TestEncodingReflectionRetainsWhatAConsumersWrapperReaches(t *testing.T) {
	in := inputOf(t, consumerArchive, Options{})
	refs := retainedRefs(t, in, EncodingReflectionDetector)

	for _, test := range []struct {
		wrapper  string
		typeName string
		want     []string
	}{
		{
			wrapper:  "a consumer's function that hands the parameter to an encoder it constructs",
			typeName: "Encoded",
			want:     wireRefs("Encoded", "Describe", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to the marshalling function",
			typeName: "Marshalled",
			want:     wireRefs("Marshalled", "Describe", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to another of its own",
			typeName: "Chained",
			want:     wireRefs("Chained", "Describe", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to nothing",
			typeName: "Counted",
			want:     nil,
		},
		{
			wrapper:  "a consumer's function whose parameter is concrete",
			typeName: "Tagged",
			want:     nil,
		},
	} {
		t.Run(strings.ReplaceAll(test.wrapper, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("EncodingReflectionDetector(%s) retained, for %s handed to %s,\ngot  %v\nwant %v",
					consumerArchive, test.typeName, test.wrapper, got, test.want)
			}
		})
	}
}

// One walk over the program's own functions finds the wrappers of both classes, so a
// consumer's print wrapper is found exactly as the target's is: a value the target
// formats through it keeps the method the verb calls, and a value handed to a
// consumer's function that formats nothing keeps nothing.
func TestFormatVerbContractRetainsWhatAConsumersPrintWrapperFormats(t *testing.T) {
	in := inputOf(t, consumerArchive, Options{})
	refs := retainedRefs(t, in, FormatVerbContractDetector)

	for _, test := range []struct {
		consumer string
		typeName string
		want     []string
	}{
		{
			consumer: "a consumer's function that hands its operands to the formatting package",
			typeName: "Printed",
			want:     wireRefs("Printed", "String"),
		},
		{
			consumer: "a consumer's function that counts its operands and formats none",
			typeName: "Counting",
			want:     nil,
		},
	} {
		t.Run(strings.ReplaceAll(test.consumer, " ", "_"), func(t *testing.T) {
			got := membersOf(refs, test.typeName)
			if !slices.Equal(got, test.want) {
				t.Errorf("FormatVerbContractDetector(%s) retained, for %s handed to %s,\ngot  %v\nwant %v",
					consumerArchive, test.typeName, test.consumer, got, test.want)
			}
		})
	}
}

// The detail names the consumer's wrapper the value was handed to, which is the
// immediate callee, so a maintainer reading the retained set is shown the function
// the target itself called rather than the encoder behind it.
func TestEncodingReflectionNamesTheConsumersWrapper(t *testing.T) {
	in := inputOf(t, consumerArchive, Options{})
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
		"go://example.com/target#Encoded.Name":    "passed to example.com/httpwire.WriteJSON",
		"go://example.com/target#Marshalled.Name": "passed to example.com/httpwire.MarshalWire",
		"go://example.com/target#Chained.Name":    "passed to example.com/httpwire.Emit",
	} {
		if got[ref] != want {
			t.Errorf("EncodingReflectionDetector(%s) records %s with detail %q, want %q",
				consumerArchive, ref, got[ref], want)
		}
	}
}

// Every site a class publishes is a file of the target, whatever module the program
// spans: the site is rendered relative to the target root, and a consumer's own file
// has no rendering there. A class that walked a consumer's calls would end the run
// rather than record one.
func TestEveryExemptionOfATwoModuleProgramNamesASiteOfTheTarget(t *testing.T) {
	in := inputOf(t, consumerArchive, Options{})

	found, err := Compute(in, goDetectors())
	if err != nil {
		t.Fatalf("Compute(%s) error: %v", consumerArchive, err)
	}
	if len(found) == 0 {
		t.Fatalf("Compute(%s) retained nothing, so this test pins nothing", consumerArchive)
	}
	for _, e := range found {
		if filepath.IsAbs(e.Site.Filename) || strings.HasPrefix(e.Site.Filename, "..") {
			t.Errorf("Compute(%s) recorded %s at %s, want a path inside the target root",
				consumerArchive, e.ID, e.Site)
		}
	}
}

// Under a production run the evidence of a test file holds nothing, and the mode's
// classification of a loaded consumer's test references does not change that: it says
// which references count as production, while an exemption's site is a file of the
// target and never a consumer's test file. Reading the classification as a second way
// for test-file evidence to hold would retain under a production sweep what a test
// alone marshals.
func TestEvidenceOfATestFileHoldsUnderNeitherProductionClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		mode graph.Mode
		want bool
	}{
		{name: "the plain mode", mode: graph.Mode{}, want: true},
		{
			name: "the plain mode where a consumer's tests count as production",
			mode: graph.Mode{ConsumerTestsProduction: true},
			want: true,
		},
		{name: "a production run", mode: graph.Mode{Production: true}, want: false},
		{
			name: "a production run where a consumer's tests count as production",
			mode: graph.Mode{Production: true, ConsumerTestsProduction: true},
			want: false,
		},
	} {
		t.Run(strings.ReplaceAll(test.name, " ", "_"), func(t *testing.T) {
			site := token.Position{Filename: "app_test.go", Line: 9, Column: 2}
			if got := holdsInMode(site, test.mode); got != test.want {
				t.Errorf("holdsInMode(%s, %+v) = %t, want %t", site, test.mode, got, test.want)
			}
			source := token.Position{Filename: "app.go", Line: 9, Column: 2}
			if got := holdsInMode(source, test.mode); !got {
				t.Errorf("holdsInMode(%s, %+v) = %t, want true: a source file's evidence holds under every mode",
					source, test.mode, got)
			}
		})
	}
}

// The package tests a parameter for the empty interface in one place, because the
// crossing out of the program is one rule: a class that tested a parameter itself
// would be a second crossing test, and two crossing tests are two rules to keep in
// agreement. The test reads the package's own sources, so a copy lands red wherever
// it is written.
func TestOneCrossingTestExistsInThePackage(t *testing.T) {
	const owner = "boundary.go"

	found := make(map[string][]string)
	for _, name := range packageSources(t) {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			if sel, selects := ast.Unparen(call.Fun).(*ast.SelectorExpr); selects && sel.Sel.Name == "Empty" {
				found[name] = append(found[name], "an interface emptiness test")
			}
			return true
		})
	}
	for name, tests := range found {
		if filepath.Base(name) != owner {
			t.Errorf("%s holds %v, want the crossing test in %s alone", name, tests, owner)
		}
	}
	if len(found[filepath.Join(".", owner)]) == 0 {
		t.Errorf("%s holds no interface emptiness test, so this test pins nothing", owner)
	}
}

// packageSources names every source file of the package, test files left out: a test
// writes the shape it measures and is not a second owner of a rule.
func packageSources(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("Setup: read the package directory: %v", err)
	}
	var found []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		found = append(found, filepath.Join(".", name))
	}
	if len(found) == 0 {
		t.Fatal("Setup: the package directory holds no source file")
	}
	return found
}

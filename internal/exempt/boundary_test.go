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
// the wrapper hands the value to is what reads it.
//
// What the wrapper hands the value to also decides how much is retained. A wrapper
// that encodes keeps the fields an encoder reads and no method, one that renders
// through a template keeps the methods a template may select as well, and one that
// does both keeps the union. A consumer function that forwards the parameter to
// nothing, and one whose parameter is concrete, are no wrappers, so a value reaching
// only those crosses nothing.
func TestEncodingReflectionRetainsWhatAConsumersWrapperReaches(t *testing.T) {
	shared := analysisOf(t, consumerArchive, Options{})
	refs := retainedRefs(t, shared, EncodingReflection)

	for _, test := range []struct {
		wrapper  string
		typeName string
		want     []string
	}{
		{
			wrapper:  "a consumer's function that hands the parameter to an encoder it constructs",
			typeName: "Encoded",
			want:     wireRefs("Encoded", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to the marshalling function",
			typeName: "Marshalled",
			want:     wireRefs("Marshalled", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to another of its own",
			typeName: "Chained",
			want:     wireRefs("Chained", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to a template engine",
			typeName: "Rendered",
			want:     wireRefs("Rendered", "Describe", "Extra", "Name", "secret"),
		},
		{
			wrapper:  "a consumer's function that hands the parameter to an encoder and to a template engine",
			typeName: "Unioned",
			want:     wireRefs("Unioned", "Describe", "Extra", "Name", "secret"),
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
	shared := analysisOf(t, consumerArchive, Options{})
	refs := retainedRefs(t, shared, FormatVerbContract)

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
	shared := analysisOf(t, consumerArchive, Options{})
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

// Every site a class publishes is relative to the root of the module whose file holds
// it, the target's or the loaded consumer's the exemption names.
func TestEveryExemptionOfATwoModuleProgramNamesASiteInsideItsModule(t *testing.T) {
	shared := analysisOf(t, consumerArchive, Options{})

	found, err := shared.compute(t, false)
	if err != nil {
		t.Fatalf("Compute(%s) error: %v", consumerArchive, err)
	}
	if len(found) == 0 {
		t.Fatalf("Compute(%s) retained nothing, so this test pins nothing", consumerArchive)
	}
	for _, e := range found {
		if filepath.IsAbs(e.Site.Filename) || strings.HasPrefix(e.Site.Filename, "..") {
			t.Errorf("Compute(%s) recorded %s at %s, want a path inside the root of its module",
				consumerArchive, e.ID, e.Site)
		}
	}
}

// consumerSiteArchive is the fixture whose consumer imports the target and encodes the
// target's values in its own files.
const consumerSiteArchive = "encoding-reflection-consumer-site.txtar"

// Evidence a consumer's file carries is a site of that consumer: the class renders it
// against the consumer's root and names the consumer, rather than ending the run on a
// file outside the target root.
func TestEncodingReflectionRendersAConsumersEvidenceAgainstTheConsumersRoot(t *testing.T) {
	shared := analysisOf(t, consumerSiteArchive, Options{})
	found, err := shared.detect(t, EncodingReflection)
	if err != nil {
		t.Fatalf("EncodingReflectionDetector(%s) = %v, want the consumer's evidence recorded", consumerSiteArchive, err)
	}
	symbols := shared.inventory(t)
	refs := make(map[graph.SymbolID]string, len(symbols))
	for i := range symbols {
		refs[symbols[i].ID] = symbols[i].Ref
	}
	sites := make(map[string]string)
	for _, e := range found {
		sites[refs[e.ID]] = e.Consumer + " " + e.Site.Filename
	}
	for ref, want := range map[string]string{
		"go://example.com/target#Record.Name":  "example.com/httpwire wire.go",
		"go://example.com/target#Record.Count": "example.com/httpwire wire.go",
		"go://example.com/target#Probe.Label":  "example.com/httpwire wire_test.go",
	} {
		if got := sites[ref]; got != want {
			t.Errorf("EncodingReflectionDetector(%s) records %s at %q, want %q", consumerSiteArchive, ref, got, want)
		}
	}

	held, err := shared.compute(t, true)
	if err != nil {
		t.Fatalf("Compute(%s) under a production run = %v", consumerSiteArchive, err)
	}
	for _, e := range held {
		if refs[e.ID] == "go://example.com/target#Probe.Label" {
			t.Errorf("Compute(%s) under a production run holds %s by the consumer's test file %s, want nothing",
				consumerSiteArchive, refs[e.ID], e.Site)
		}
	}
}

// Under a production run the evidence a test file of the target carries holds
// nothing, whatever the mode says of a consumer's tests. A loaded consumer's test file
// is classified as the mode classifies that file's references.
func TestEvidenceOfATestFileHoldsAsTheModeClassifiesThatFile(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         graph.Mode
		targetTest   bool
		consumerTest bool
	}{
		{name: "the_plain_mode", mode: graph.Mode{}, targetTest: true, consumerTest: true},
		{name: "the_plain_mode_consumer_tests_production", mode: graph.Mode{ConsumerTestsProduction: true}, targetTest: true, consumerTest: true},
		{name: "a_production_run", mode: graph.Mode{Production: true}},
		{
			name: "a_production_run_consumer_tests_production",
			mode: graph.Mode{Production: true, ConsumerTestsProduction: true}, consumerTest: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			site := token.Position{Filename: "app_test.go", Line: 9, Column: 2}
			if got := holdsInMode(&graph.Exemption{Site: site}, test.mode); got != test.targetTest {
				t.Errorf("holdsInMode(the target's %s, %+v) = %t, want %t", site, test.mode, got, test.targetTest)
			}
			consumed := &graph.Exemption{Consumer: "example.com/consumer", Site: site}
			if got := holdsInMode(consumed, test.mode); got != test.consumerTest {
				t.Errorf("holdsInMode(the consumer's %s, %+v) = %t, want %t", site, test.mode, got, test.consumerTest)
			}
			for _, consumer := range []string{"", "example.com/consumer"} {
				source := &graph.Exemption{Consumer: consumer, Site: token.Position{Filename: "app.go", Line: 9, Column: 2}}
				if got := holdsInMode(source, test.mode); !got {
					t.Errorf("holdsInMode(%q app.go, %+v) = false, want true: a source file's evidence holds under every mode",
						consumer, test.mode)
				}
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

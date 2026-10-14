package graph

import (
	"slices"
	"testing"
)

// A suite runner's suite has every exported method whose name opens with one of the
// runner's prefixes, or that its list names, rooted as a test, a method an embedded
// suite promotes included, under the rule the runner applies by reflection. A suite
// passed as an interface value, a method the runner skips and one a look-alike runner
// receives are no root.
func TestRootsRootsTheMethodsASuiteRunnerCalls(t *testing.T) {
	d := detect(t, "suite-run.txtar", RootOptions{})
	kinds := kindsByRef(d)

	const pkg = "go://example.com/suites#"
	cases := []struct {
		ref  string
		want []RootKind
	}{
		{ref: pkg + "Runs.TestRuns", want: []RootKind{RootTest}},
		{ref: pkg + "Runs.Testing", want: []RootKind{RootTest}},
		{ref: pkg + "base.TestInherited", want: []RootKind{RootTest}},
		{ref: pkg + "Runs.Helper"},
		{ref: pkg + "Runs.testHidden"},
		{ref: pkg + "Hidden.TestBehindAnInterface"},
		{ref: pkg + "Alike.TestOfALookAlike"},
		{ref: pkg + "Checked.TestChecked", want: []RootKind{RootTest}},
		{ref: pkg + "Checked.BenchmarkChecked", want: []RootKind{RootTest}},
		{ref: pkg + "Checked.SetUpTest", want: []RootKind{RootTest}},
		{ref: pkg + "Checked.Setup"},
	}
	held := make(map[string]bool, len(d.symbols))
	for _, s := range d.symbols {
		held[s.Ref] = true
	}
	for _, test := range cases {
		t.Run(test.ref, func(t *testing.T) {
			if !held[test.ref] {
				t.Fatalf("Symbols(suite-run.txtar) holds no symbol whose reference is %s", test.ref)
			}
			if got := kinds[test.ref]; !slices.Equal(got, test.want) {
				t.Errorf("Roots(suite-run.txtar)[%s] = %v, want %v", test.ref, got, test.want)
			}
		})
	}
}

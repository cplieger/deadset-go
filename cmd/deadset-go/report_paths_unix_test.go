//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/report"
)

// pathClasses is one file name per class of character a reference spells apart:
// a C0 control, a C1 control, a bidirectional control, the line separator, a
// space, % and #. A report path and a baseline row hold each as the file name
// does, and their path form refuses only a C0 control or DEL, so a run that writes
// a baseline naming such a file refuses the baseline it wrote with exit 2.
var pathClasses = []struct {
	label  string
	name   string
	faults bool
}{
	{"c0-control", "w\x1bx.go", true},
	{"c1-control", "w\u009bx.go", false},
	{"bidi-control", "w\u202ex.go", false},
	{"line-separator", "w\u2028x.go", false},
	{"space", "w x.go", false},
	{"percent", "w%x.go", false},
	{"number-sign", "w#x.go", false},
}

// filledPaths is every path location of the report the fixture of
// [TestAFileNameOfEveryClassIsReportedAsTheFileNamesIt] fills, and unfilledPaths
// every other one the report schema holds. A location the schema gains fails the
// test until it is listed in one of them.
var (
	filledPaths = []string{
		"target.root",
		"findings[].position.path",
		"findings[].details.write_positions[].path",
		"findings[].details.implementations[].position.path",
		"findings[].details.name_literal.path",
		"components[].members[].position.path",
		"stale_suppressions[].position.path",
		"stale_suppressions[].entry.path",
	}
	// namingTheFile is each location of filledPaths the fixture fills with the
	// path of the file whose name holds the class's character.
	namingTheFile = []string{
		"findings[].details.write_positions[].path",
		"findings[].details.implementations[].position.path",
		"findings[].details.name_literal.path",
		"components[].members[].position.path",
	}
	unfilledPaths = []string{
		"findings[].details.entry.path",
		"edge_evaluations[].finding.position.path",
		"edge_evaluations[].finding.details.write_positions[].path",
		"edge_evaluations[].finding.details.implementations[].position.path",
		"edge_evaluations[].finding.details.name_literal.path",
		"edge_evaluations[].finding.details.entry.path",
		"configurations[].project",
		"configurations_not_built[].project",
		"excluded_by_cgo[]",
		"type_error_skips[].path",
		"notes[].path",
		"conventions_applied[].manifest",
	}
)

// pathClassFindings is the report's findings over the fixture of
// [TestAFileNameOfEveryClassIsReportedAsTheFileNamesIt], as [findingSummary] writes
// each, with FILE standing for the file whose name holds the class's character.
var pathClassFindings = []string{
	"DS1301 counter main.go:7 writes=[FILE:3]",
	"DS1301 shared main.go:9 writes=[main.go:5 FILE:3]",
	"DS1201 Box main.go:11 implementations=[FILE:5]",
	"DS1002 dead main.go:13",
	"DS1002 lit main.go:15 name_literal=FILE:3",
	"DS1101 Impl FILE:5",
	"DS1002 hidden FILE:9",
}

// A file whose name holds any class of character the reference grammar spells
// apart is analyzed, and the report names it as the file system does: every path
// at every location the report schema holds one names a real file of the target,
// the findings are the same whatever the class, and the run ends as the report's
// path form decides.
func TestAFileNameOfEveryClassIsReportedAsTheFileNamesIt(t *testing.T) {
	t.Parallel()

	locations := schemaPathLocations(t)
	if listed := slices.Concat(filledPaths, unfilledPaths); !sameSet(locations, listed) {
		t.Fatalf("Setup: the report schema holds paths at %q, and the test lists %q", locations, listed)
	}
	for _, class := range pathClasses {
		t.Run(class.label, func(t *testing.T) {
			t.Parallel()

			dir := writeModule(t, map[string]string{
				"go.mod": "module example.com/app\n\ngo 1.27.1\n",
				"main.go": "package main\n\nfunc main() {}\n\nfunc init() { shared = 1 }\n\n" +
					"var counter int\n\nvar shared int\n\ntype Box interface{ M() }\n\n" +
					"func dead() { hidden() }\n\nfunc lit() {}\n",
				class.name: "package main\n\nfunc init() { counter = 1; shared = 2; println(\"lit\") }\n\n" +
					"type Impl struct{}\n\nfunc (Impl) M() {}\n\nfunc hidden() {}\n",
				repositoryDocument:    `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}, "reporters": {"cascade": "full"}}`,
				"deadset-ignore.json": `{"ignore": [{"code": "DS1104", "symbol": "go://example.com/app#forgotten", "path": "main.go", "reason": "Kept for the next release."}]}`,
			})
			baseline := filepath.Join(sandbox.Dir(t), "baseline.json")
			got := runAnalyze(t, dir, "--format=text", "--format=sarif", "--baseline-write="+baseline)
			if class.faults {
				if got.code != 2 || !strings.Contains(got.stderr, "want a path relative to the target root") {
					t.Fatalf("analyze exited %d, want 2 refusing the baseline's path: %s", got.code, got.stderr)
				}
				return
			}
			if got.code != 1 {
				t.Fatalf("analyze exited %d, want 1: %s", got.code, got.stderr)
			}
			assertPathsNameRealFiles(t, dir, got.reportPath, locations, class.name)
			assertBaselineNamesRealFiles(t, dir, baseline)
			envelope := envelopeAt(t, got.reportPath)
			summaries := make([]string, len(envelope.Findings))
			for i := range envelope.Findings {
				summaries[i] = findingSummary(&envelope.Findings[i])
			}
			want := make([]string, len(pathClassFindings))
			for i, line := range pathClassFindings {
				want[i] = strings.ReplaceAll(line, "FILE", class.name)
			}
			if !slices.Equal(summaries, want) {
				t.Errorf("the findings are\n%s\nwant\n%s", strings.Join(summaries, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// assertPathsNameRealFiles fails t for a path the report at reportPath carries, at
// any of locations, that names no file of the target in dir, and for a location of
// namingTheFile that does not name the file name.
func assertPathsNameRealFiles(t *testing.T, dir, reportPath string, locations []string, name string) {
	t.Helper()

	body, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	for _, location := range locations {
		named := valuesAt(document, location)
		for _, path := range named {
			if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
				t.Errorf("%s names %q, which is no file of the target: %v", location, path, err)
			}
		}
		if slices.Contains(namingTheFile, location) && !slices.Contains(named, name) {
			t.Errorf("%s names %q, want %q among them", location, named, name)
		}
	}
}

// findingSummary is f's code, name and position, then the paths and lines of the
// nested positions it carries, which two runs over one tree agree on whatever
// spelling their references take.
func findingSummary(f *report.Finding) string {
	at := func(p report.Position) string { return fmt.Sprintf("%s:%d", p.Path, p.Line) }
	line := fmt.Sprintf("%s %s %s", f.Code, f.Symbol.Name, at(f.Position))
	if len(f.Details.Implementations) > 0 {
		held := make([]string, len(f.Details.Implementations))
		for i := range f.Details.Implementations {
			held[i] = at(f.Details.Implementations[i].Position)
		}
		line += " implementations=[" + strings.Join(held, " ") + "]"
	}
	if f.Details.NameLiteral != nil {
		line += " name_literal=" + at(*f.Details.NameLiteral)
	}
	if len(f.Details.WritePositions) > 0 {
		held := make([]string, len(f.Details.WritePositions))
		for i, p := range f.Details.WritePositions {
			held[i] = at(p)
		}
		line += " writes=[" + strings.Join(held, " ") + "]"
	}
	return line
}

// assertBaselineNamesRealFiles fails t for a row of the baseline at path that
// names no file of the target in dir.
func assertBaselineNamesRealFiles(t *testing.T, dir, path string) {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the baseline: %v", err)
	}
	var rows struct {
		Baseline []struct {
			Path string `json:"path"`
		} `json:"baseline"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode the baseline: %v", err)
	}
	if len(rows.Baseline) == 0 {
		t.Fatalf("the baseline records no row:\n%s", body)
	}
	for _, row := range rows.Baseline {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(row.Path))); err != nil {
			t.Errorf("a baseline row names %q, which is no file of the target: %v", row.Path, err)
		}
	}
}

// schemaPathLocations is every location of a report that the report schema holds a
// path at: a value of finding.schema.json's relative_path, or one whose pattern
// refuses the control characters a path refuses. A location is the member names
// leading to it, joined by dots, with [] after an array.
func schemaPathLocations(t *testing.T) []string {
	t.Helper()

	documents := map[string]map[string]any{}
	for _, name := range []string{"report.schema.json", "finding.schema.json"} {
		body, err := spec.Contract.ReadFile("contract/" + name)
		if err != nil {
			t.Fatalf("Setup: read contract/%s: %v", name, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(body, &schema); err != nil {
			t.Fatalf("Setup: decode contract/%s: %v", name, err)
		}
		documents[name] = schema
	}
	found := map[string]bool{}
	var walk func(file string, node any, at string, open map[string]bool)
	walk = func(file string, node any, at string, open map[string]bool) {
		object, isObject := node.(map[string]any)
		if !isObject {
			return
		}
		if ref, refers := object["$ref"].(string); refers {
			target, pointer, _ := strings.Cut(ref, "#")
			if target == "" {
				target = file
			}
			if pointer == "/$defs/relative_path" {
				found[at] = true
				return
			}
			key := target + "#" + pointer
			if open[key] {
				return
			}
			open[key] = true
			walk(target, resolvePointer(t, documents[target], pointer), at, open)
			delete(open, key)
		}
		if pattern, ok := object["pattern"].(string); ok && strings.Contains(pattern, `\x00-\x1f\x7f`) {
			found[at] = true
		}
		if properties, ok := object["properties"].(map[string]any); ok {
			for name, property := range properties {
				walk(file, property, strings.TrimPrefix(at+"."+name, "."), open)
			}
		}
		walk(file, object["items"], at+"[]", open)
		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			branches, _ := object[keyword].([]any)
			for _, branch := range branches {
				walk(file, branch, at, open)
			}
		}
		walk(file, object["then"], at, open)
		walk(file, object["else"], at, open)
	}
	walk("report.schema.json", documents["report.schema.json"], "", map[string]bool{})
	var locations []string
	for location := range found {
		locations = append(locations, location)
	}
	slices.Sort(locations)
	return locations
}

// resolvePointer is the node a JSON pointer of the $defs form names in schema.
func resolvePointer(t *testing.T, schema map[string]any, pointer string) any {
	t.Helper()

	var node any = schema
	if pointer == "" {
		return node
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("Setup: %s names no schema node", pointer)
		}
		node = object[part]
	}
	return node
}

// valuesAt is every string document holds at location, in the form
// [schemaPathLocations] spells one.
func valuesAt(document any, location string) []string {
	nodes := []any{document}
	for part := range strings.SplitSeq(location, ".") {
		name, arrays := strings.CutSuffix(part, "[]")
		var next []any
		for _, node := range nodes {
			object, _ := node.(map[string]any)
			value, held := object[name]
			if !held {
				continue
			}
			if arrays {
				items, _ := value.([]any)
				next = append(next, items...)
			} else {
				next = append(next, value)
			}
		}
		nodes = next
	}
	var values []string
	for _, node := range nodes {
		if text, ok := node.(string); ok {
			values = append(values, text)
		}
	}
	return values
}

// sameSet reports whether a and b hold the same strings.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

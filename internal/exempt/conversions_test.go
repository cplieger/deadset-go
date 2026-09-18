package exempt

import (
	"errors"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/txtar"
)

// inputForDir assembles the input of one target directory already on disk, for a
// fixture the package's own testdata does not hold.
func inputForDir(t *testing.T, dir, subject string) *Input {
	t.Helper()
	result, root := loadDir(t, dir)
	symbols, err := graph.Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: graph.Symbols(%s): %v", subject, err)
	}
	resolve, err := graph.NewResolver(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: graph.NewResolver(%s): %v", subject, err)
	}
	return &Input{Result: result, Symbols: symbols, Resolve: resolve, Root: root, Read: os.ReadFile}
}

// renderConversions prints one conversion per line, tab-separated, for a golden
// comparison. Each position is rendered against the target root, so the temporary
// directory the archive was written to does not reach the golden.
func renderConversions(t *testing.T, in *Input, sites []conversion) string {
	t.Helper()
	var b strings.Builder
	for i := range sites {
		c := &sites[i]
		at, err := in.Resolve.Render(c.Site)
		if err != nil {
			t.Fatalf("Resolve.Render(%v) error: %v", c.Site, err)
		}
		fmt.Fprintf(&b, "%s:%d:%d\t%s\t%s\timplements=%t\n",
			at.Filename, at.Line, at.Column, c.From, c.name, types.Implements(c.From, c.To))
	}
	return b.String()
}

// compareGolden compares one rendering against the committed fixture of that
// name, and rewrites the fixture when the run asks for it.
func compareGolden(t *testing.T, name, subject, got string) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	regenerate := "UPDATE_GOLDEN=1 go test ./internal/exempt/"

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", golden, err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("Setup: read %s (run %s): %v", golden, regenerate, err)
	}
	if got != string(want) {
		t.Errorf("%s golden mismatch (run %s)\n--- want\n%s\n+++ got\n%s", subject, regenerate, want, got)
	}
}

func TestConversionsGoldenTableOverEverySyntacticShape(t *testing.T) {
	in := inputOf(t, "conversion-shapes.txtar", Options{})
	sites, err := conversionSites(in)
	if err != nil {
		t.Fatalf("Conversions(conversion-shapes.txtar) error: %v", err)
	}
	compareGolden(t, "conversion-shapes.golden",
		"Conversions(conversion-shapes.txtar)", renderConversions(t, in, sites))
}

func TestConversionsRecordsNeitherAnAssertionNorATypeSwitchNorADeclaration(t *testing.T) {
	in := inputOf(t, "conversion-shapes.txtar", Options{})
	sites, err := conversionSites(in)
	if err != nil {
		t.Fatalf("Conversions(conversion-shapes.txtar) error: %v", err)
	}

	// NotConversions holds one type assertion, one two-case type switch, one short
	// variable declaration and one comparison whose operand is written as an
	// explicit conversion. Only that operand puts a value into an interface, so
	// exactly one site inside the function is recorded, on the line that spells the
	// conversion.
	rendered := renderConversions(t, in, sites)
	got := linesInFunction(t, rendered, "NotConversions")
	if len(got) != 1 {
		t.Errorf("Conversions(conversion-shapes.txtar) recorded %d sites in NotConversions:\n%s\nwant 1, the explicit conversion",
			len(got), strings.Join(got, "\n"))
	}
}

func TestConversionsIsIndependentOfTheOrderItIsCalledIn(t *testing.T) {
	in := inputOf(t, "conversion-shapes.txtar", Options{})
	first, err := conversionSites(in)
	if err != nil {
		t.Fatalf("Conversions(conversion-shapes.txtar) first call error: %v", err)
	}
	second, err := conversionSites(in)
	if err != nil {
		t.Fatalf("Conversions(conversion-shapes.txtar) second call error: %v", err)
	}
	want, got := renderConversions(t, in, first), renderConversions(t, in, second)
	if got != want {
		t.Errorf("Conversions(conversion-shapes.txtar) twice differs\n--- first\n%s\n+++ second\n%s", want, got)
	}
}

func TestConversionsRefusesAnInputCarryingNoFileSet(t *testing.T) {
	for _, test := range []struct {
		in   *Input
		name string
	}{
		{name: "no input", in: nil},
		{name: "no load result", in: &Input{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sites, err := Conversions(test.in)
			if err == nil {
				t.Fatalf("Conversions(%s) = %v, nil, want an error", test.name, sites)
			}
			if !errors.Is(err, graph.ErrIncompleteLoad) {
				t.Errorf("Conversions(%s) error = %v, want one matching graph.ErrIncompleteLoad", test.name, err)
			}
		})
	}
}

// linesInFunction returns the rendered conversion lines whose line number falls
// inside the named function of the fixture's one source file.
func linesInFunction(t *testing.T, rendered, name string) []string {
	t.Helper()
	first, last := functionSpan(t, name)
	var inside []string
	for line := range strings.SplitSeq(strings.TrimRight(rendered, "\n"), "\n") {
		at := lineNumberOf(t, line)
		if at >= first && at <= last {
			inside = append(inside, line)
		}
	}
	return inside
}

// functionSpan returns the first and last source line of one function of the
// fixture, read from the archive rather than from the loaded program, so the span
// comes from the file the golden's positions name.
func functionSpan(t *testing.T, name string) (first, last int) {
	t.Helper()
	source := fixtureSource(t, "conversion-shapes.txtar", "shapes.go")
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		switch {
		case first == 0 && strings.HasPrefix(line, "func "+name+"("):
			first = i + 1
		case first != 0 && line == "}":
			return first, i + 1
		}
	}
	t.Fatalf("Setup: shapes.go declares no function %s", name)
	return 0, 0
}

// lineNumberOf reads the line number out of one rendered conversion.
func lineNumberOf(t *testing.T, rendered string) int {
	t.Helper()
	fields := strings.Split(strings.SplitN(rendered, "\t", 2)[0], ":")
	if len(fields) != 3 {
		t.Fatalf("Setup: %q is not a rendered position", rendered)
	}
	var at int
	if _, err := fmt.Sscanf(fields[1], "%d", &at); err != nil {
		t.Fatalf("Setup: %q carries no line number: %v", rendered, err)
	}
	return at
}

// writeUnder lays one archive's files out under a temporary directory, keeping
// only the files under prefix and stripping it from each name, which is how one
// module of a multi-module rendering is loaded on its own.
func writeUnder(t *testing.T, files []txtar.File, prefix string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		name, inside := relativeTo(f.Name, prefix)
		if !inside {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", path, err)
		}
	}
	return dir
}

// relativeTo reports whether name is under prefix, and returns name without it.
func relativeTo(name, prefix string) (string, bool) {
	if prefix == "" {
		return name, true
	}
	rest, inside := strings.CutPrefix(name, prefix)
	return rest, inside && rest != ""
}

// fixtureSource returns one section of one archive under testdata.
func fixtureSource(t *testing.T, archive, section string) string {
	t.Helper()
	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	for _, f := range parsed.Files {
		if f.Name == section {
			return string(f.Data)
		}
	}
	t.Fatalf("Setup: testdata/%s carries no section %s", archive, section)
	return ""
}

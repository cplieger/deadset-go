//go:build unix

package main

import (
	"strings"
	"testing"
)

// A file base name holding a colon and a space, which only a Unix file system can
// carry, is found by the name its finding spells it as, a source-path segment
// (symbol-ref.md, Path segments), and by its own, and explain spells it as the
// report does.
func TestExplainFindsAFileByTheNameItsFindingSpellsItAs(t *testing.T) {
	t.Parallel()
	const ref = "go://example.com/app/tool#tool%3A%20gen.go:file"
	dir := writeModule(t, map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.27.1\n",
		"main.go":           "package main\n\nfunc main() {}\n",
		"tool/tool: gen.go": "package tool\n\n// Gen is referenced by nothing at all.\nfunc Gen() int { return 1 }\n",
		repositoryDocument:  `{"target": {"kind": "application"}}`,
	})
	for _, named := range []string{"tool:%20gen.go", "tool: gen.go"} {
		got := runExplain(t, dir, named)
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Errorf("explain %q = %d, want %d\nstderr: %s", named, got.code, want, got.stderr)
			continue
		}
		for _, want := range []string{
			"symbol: " + ref,
			"declaration: file tool:%20gen.go\n",
			"answer: reported",
			"code: DS1602 file-never-imported",
			"message: no import reaches the package file tool:%20gen.go declares",
		} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("explain %q stdout =\n%s\nwant it to contain %q", named, got.stdout, want)
			}
		}
	}

	got := runExplain(t, dir, "tool:%20gen")
	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("explain %q = %d, want %d", "tool:%20gen", got.code, want)
	}
	if !strings.Contains(got.stderr, ref+"\ttool/tool: gen.go:1:1") {
		t.Errorf("explain %q stderr =\n%s\nwant the file among its partial matches", "tool:%20gen", got.stderr)
	}
}

// Two files of one package whose names differ only by the space after a colon
// keep two spellings, and explain finds each by its own.
func TestTwoFilesSpelledApartAreEachExplainedByItsOwnName(t *testing.T) {
	t.Parallel()
	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		"tool/a:b.go":      "package tool\n\n// Gen is referenced by nothing at all.\nfunc Gen() int { return 1 }\n",
		"tool/a: b.go":     "package tool\n\n// Other is referenced by nothing at all.\nfunc Other() int { return 2 }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	for _, file := range []struct{ named, ref, path string }{
		{"a:b.go", "go://example.com/app/tool#a%3Ab.go:file", "tool/a:b.go"},
		{"a:%20b.go", "go://example.com/app/tool#a%3A%20b.go:file", "tool/a: b.go"},
	} {
		got := runExplain(t, dir, file.named)
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Errorf("explain %q = %d, want %d\nstderr: %s", file.named, got.code, want, got.stderr)
			continue
		}
		for _, want := range []string{
			"symbol: " + file.ref + "\n",
			"declaration: file " + file.named + "\n",
			"position: " + file.path + ":1:1\n",
		} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("explain %q stdout =\n%s\nwant it to contain %q", file.named, got.stdout, want)
			}
		}
	}
}

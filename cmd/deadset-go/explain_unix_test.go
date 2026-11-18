//go:build unix

package main

import (
	"strings"
	"testing"
)

// A file base name holding a colon and a space, which only a Unix file system can
// carry, is found by the name its finding spells it as (finding.schema.json,
// symbol.name) and by its own, and explain spells it as the report does.
func TestExplainFindsAFileByTheNameItsFindingSpellsItAs(t *testing.T) {
	t.Parallel()
	const ref = "go://example.com/app/tool#tool: gen.go:file"
	dir := writeModule(t, map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.27.1\n",
		"main.go":           "package main\n\nfunc main() {}\n",
		"tool/tool: gen.go": "package tool\n\n// Gen is referenced by nothing at all.\nfunc Gen() int { return 1 }\n",
		repositoryDocument:  `{"target": {"kind": "application"}}`,
	})
	for _, named := range []string{"tool:gen.go", "tool: gen.go"} {
		got := runExplain(t, dir, named)
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Errorf("explain %q = %d, want %d\nstderr: %s", named, got.code, want, got.stderr)
			continue
		}
		for _, want := range []string{
			"symbol: " + ref,
			"declaration: file tool:gen.go\n",
			"answer: reported",
			"code: DS1602 file-never-imported",
			"message: no import reaches the package file tool:gen.go declares",
		} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("explain %q stdout =\n%s\nwant it to contain %q", named, got.stdout, want)
			}
		}
	}

	got := runExplain(t, dir, "tool:gen")
	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("explain %q = %d, want %d", "tool:gen", got.code, want)
	}
	if !strings.Contains(got.stderr, ref+"\ttool/tool: gen.go:1:1") {
		t.Errorf("explain %q stderr =\n%s\nwant the file among its partial matches", "tool:gen", got.stderr)
	}
}

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// readerWorkspace is a workspace listing an application and, beside it, a module
// whose only call reaches the application's exported lib.Used. The reader sits
// outside the target, so a report run from the target cannot name it.
func readerWorkspace(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.work":                   "go 1.27.1\n\nuse (\n\t./app\n\t./reader\n)\n",
		"app/go.mod":                "module example.com/app\n\ngo 1.27.1\n",
		"app/main.go":               "package main\n\nfunc main() {}\n",
		"app/lib/lib.go":            "package lib\n\n// Used is what the reader calls.\nfunc Used() {}\n",
		"app/" + repositoryDocument: `{"target": {"kind": "application"}}`,
		"reader/go.mod":             "module example.com/reader\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n",
		"reader/main.go":            "package main\n\nimport \"example.com/app/lib\"\n\nfunc main() { lib.Used() }\n",
	})
}

// A module the workspace listing the target lists outside it counts its references
// whatever names the scope: a scope document the invocation names, the one deadset
// always passes, included.
func TestAnalyzeCountsAWorkspaceReaderWhateverNamesTheScope(t *testing.T) {
	base := readerWorkspace(t)
	app := filepath.Join(base, "app")
	scopeDocument := filepath.Join(base, "scope.json")
	writeDocument(t, base, "scope.json", `{"target": {"path": "`+app+`"}}`)

	for _, invocation := range []struct {
		name string
		args []string
	}{
		{name: "no scope document"},
		{name: "a scope document naming the target alone", args: []string{"--scope=" + scopeDocument}},
	} {
		got := runAnalyze(t, app, invocation.args...)
		if got.code != vocab.ExitClean && got.code != vocab.ExitFindings {
			t.Fatalf("analyze with %s = %d, want a report; stderr:\n%s", invocation.name, got.code, got.stderr)
		}
		for _, f := range envelopeAt(t, got.reportPath).Findings {
			t.Errorf("analyze with %s reported %s %s, want nothing: the workspace reader calls lib.Used", invocation.name, f.Code, f.Symbol.Ref)
		}
	}
}

// A module the workspace lists whose go.mod the toolchain cannot read fails every
// load through that workspace, the target's own included, so the run ends with the
// toolchain's refusal naming that file and writes no report.
func TestAnalyzeEndsOnAWorkspaceMemberWhoseModuleFileTheToolchainCannotRead(t *testing.T) {
	base := readerWorkspace(t)
	writeDocument(t, base, "reader/go.mod", "module example.com/reader\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nunknowndirective x\n")

	got := runAnalyze(t, filepath.Join(base, "app"))
	if got.code != vocab.ExitFailure || !strings.Contains(got.stderr, "reader/go.mod") {
		t.Errorf("analyze with an unreadable workspace member = %d, stderr %q, want %d naming reader/go.mod", got.code, got.stderr, vocab.ExitFailure)
	}
	if _, err := os.Stat(got.reportPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("analyze with an unreadable workspace member left the report %s (%v), want none", got.reportPath, err)
	}
}

// A target a go.mod above it governs is a directory of that module: the run
// analyzes it and judges no directive of a module file outside the target.
func TestAnalyzeReadsATargetAModuleFileAboveGoverns(t *testing.T) {
	base := writeModule(t, map[string]string{
		"go.mod":                    "module example.com/app\n\ngo 1.27.1\n",
		"lib/lib.go":                "package lib\n\n// Unused is what nothing calls.\nfunc Unused() {}\n",
		"lib/" + repositoryDocument: `{"target": {"kind": "application"}}`,
	})

	got := runAnalyze(t, filepath.Join(base, "lib"))
	if got.code != vocab.ExitFindings {
		t.Fatalf("analyze of a directory of a module = %d, want %d; stderr:\n%s", got.code, vocab.ExitFindings, got.stderr)
	}
	var reported []string
	for _, f := range envelopeAt(t, got.reportPath).Findings {
		reported = append(reported, string(f.Code)+" "+f.Symbol.Ref)
	}
	if want := []string{"DS1602 go://example.com/app/lib#lib.go:file", "DS1001 go://example.com/app/lib#Unused"}; !slices.Equal(reported, want) {
		t.Errorf("analyze of a directory of a module reported %v, want %v", reported, want)
	}
}

// A package below an underscore directory of a module the search finds below a
// target root that holds no go.mod is a consumer of that module, as one of a
// module rooted at the target is, so a declaration only it references is live.
func TestAnalyzeCountsTheUnderscoredPackagesOfAWalkedModule(t *testing.T) {
	base := writeModule(t, map[string]string{
		repositoryDocument:             `{"target": {"kind": "application"}}`,
		"app/go.mod":                   "module example.com/app\n\ngo 1.27.1\n",
		"app/main.go":                  "package main\n\nfunc main() {}\n",
		"app/lib/lib.go":               "package lib\n\n// Throttle is what the example calls.\nfunc Throttle() {}\n\n// Spare is what nothing calls.\nfunc Spare() {}\n",
		"app/_examples/limits/main.go": "package main\n\nimport \"example.com/app/lib\"\n\nfunc main() { lib.Throttle() }\n",
	})

	got := runAnalyze(t, base)
	if got.code != vocab.ExitFindings {
		t.Fatalf("analyze of a walked module with an example = %d, want %d; stderr:\n%s", got.code, vocab.ExitFindings, got.stderr)
	}
	var reported []string
	for _, f := range envelopeAt(t, got.reportPath).Findings {
		reported = append(reported, string(f.Code)+" "+f.Symbol.Ref)
	}
	if want := []string{"DS1001 go://example.com/app/lib#Spare"}; !slices.Equal(reported, want) {
		t.Errorf("analyze of a walked module with an example reported %v, want %v", reported, want)
	}
}

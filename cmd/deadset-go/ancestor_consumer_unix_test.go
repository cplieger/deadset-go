//go:build unix

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// ancestorFiles is a module requiring the target in its directory t through a
// replace and calling Used, below prefix: the target's Used returns a literal
// spelling Forgotten, which lowers Forgotten's finding to possible.
func ancestorFiles(prefix string) map[string]string {
	return map[string]string{
		prefix + "go.mod": "module example.com/parent\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ./t\n",
		prefix + "main.go": "// Command parent calls the target.\npackage main\n\n" +
			"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n",
		prefix + "t/go.mod": "module example.com/app\n\ngo 1.27.1\n",
		prefix + "t/lib.go": "// Package app exports one declaration the parent calls and one nothing calls.\npackage app\n\n" +
			"// Used is what the parent calls.\nfunc Used() string { return \"Forgotten\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		prefix + "t/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
	}
}

// resolvedModule writes files as [writeModule] does and returns the directory where
// it resolves.
func resolvedModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(writeModule(t, files))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return dir
}

// A declared consumer whose module is a directory above the target holds none of
// the target's files, however either is spelled: with the consumer's directory
// spelled longer than the target's, through a link or as macOS spells /private/var
// beside a target below /var, the target's evidence still renders against the
// target root and names no consumer, so the literal in its lib.go lowers Forgotten.
// Each run starts in the consumer's directory, which holds both.
func TestAnalyzeKeepsTheTargetsFilesFromAConsumerAboveIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tree func(t *testing.T) (run, target, consumer string)
	}{
		{
			name: "the consumer spelled through a link longer than the target's directory",
			tree: func(t *testing.T) (string, string, string) {
				parent := resolvedModule(t, ancestorFiles(""))
				link := filepath.Join(sandbox.Dir(t), strings.Repeat("l", 96))
				symlink(t, parent, link)
				return parent, "t", link
			},
		},
		{
			name: "the target spelled through a link shorter than the consumer's directory",
			tree: func(t *testing.T) (string, string, string) {
				deep := strings.Repeat("d", 96)
				parent := filepath.Join(resolvedModule(t, ancestorFiles(deep+"/")), deep)
				link := filepath.Join(sandbox.Dir(t), "s")
				symlink(t, parent, link)
				return parent, filepath.Join(link, "t"), parent
			},
		},
		{
			name: "the target below the temporary directory as spelled, the consumer where it resolves",
			tree: func(t *testing.T) (string, string, string) {
				spelled := writeModule(t, ancestorFiles(""))
				parent, err := filepath.EvalSymlinks(spelled)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				return spelled, "t", parent
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run, target, consumer := tc.tree(t)
			writeDocument(t, run, "ancestor-scope.json", `{"target": {"id": "example.com/app", "role": "target", "path": "`+target+`"},`+
				` "consumers": [{"id": "example.com/parent", "role": "consumer", "path": "`+consumer+`"}]}`)

			envelope := envelopeAt(t, analyzedFrom(t, run, "--scope=ancestor-scope.json", "--config=t/"+repositoryDocument))
			if loaded := envelope.Consumers.Loaded; len(loaded) != 1 {
				t.Errorf("the report names the loaded consumers %+v, want the parent", loaded)
			}
			if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
				t.Fatalf("the report reports %v under DS1001, want %v: the parent's call holds Used live", got, want)
			}
			found := envelope.Findings[slices.IndexFunc(envelope.Findings, func(f report.Finding) bool { return f.Code == "DS1001" })]
			literal := found.Details.NameLiteral
			if found.Position.Path != "lib.go" || found.Confidence != vocab.Possible || literal == nil || literal.Path != "lib.go" {
				t.Errorf("Forgotten is reported at %s %s with the name literal %+v, want lib.go possible with the literal at lib.go:"+
					" a consumer above the target holds no target file", found.Position.Path, found.Confidence, literal)
			}
		})
	}
}

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// exportedFindings is the symbol of every DS1001 finding of a report.
func exportedFindings(envelope *report.Report) []string {
	var exported []string
	for i := range envelope.Findings {
		if envelope.Findings[i].Code == "DS1001" {
			exported = append(exported, envelope.Findings[i].Symbol.Ref)
		}
	}
	return exported
}

// A declared consumer that reaches the target by a case variant of its directory,
// on a volume that finds an entry by such a name, holds the target only where the
// names, resolved, say so: the run either counts its references, so the export only
// it uses is not reported, or refuses it naming the two directories, and never
// loads it with references that match no declaration. A volume that keeps the case
// of a name offers no such variant.
func TestAnalyzeNeverMiscountsAConsumerReachingTheTargetByACaseVariant(t *testing.T) {
	t.Parallel()
	dir := writeModule(t, map[string]string{
		"target/go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"target/lib.go": "// Package app exports one declaration a consumer references and one\n" +
			"// nothing references.\npackage app\n\n" +
			"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		"target/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}}`,
		"consumer/go.mod":              "module example.com/consumer\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../TARGET\n",
		"consumer/main.go": "// Command consumer references one exported declaration of the target.\npackage main\n\n" +
			"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n",
		"scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "target"},` +
			` "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "consumer"}]}`,
	})
	if _, err := os.Stat(filepath.Join(dir, "TARGET", "go.mod")); err != nil {
		t.Skipf("the volume of %s keeps the case of a name, so no case variant names the target: %v", dir, err)
	}

	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	ran := invokedIn(t, dir, "analyze", "--report="+reportPath, "--scope=scope.json", "--config=target/"+repositoryDocument)
	if ran.code != vocab.ExitClean && ran.code != vocab.ExitFindings {
		if !strings.Contains(ran.stderr, "rather than at") {
			t.Errorf("analyze over a consumer reaching the target as TARGET = %d writing %q, want a verdict or a refusal naming both directories", ran.code, ran.stderr)
		}
		return
	}
	if got, want := exportedFindings(envelopeAt(t, reportPath)), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: a consumer the run loads counts its reference to Used", got, want)
	}
}

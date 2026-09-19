package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pendingBase writes the target one pending assertion is measured over, under a
// directory of its own inside the returned base: a module with one declaration nothing
// references, and an edge document pairing that declaration with the TypeScript
// generated from it.
//
// An analyzer reads only its own side of an edge and never resolves the pair, so the
// run publishes the evaluation of the Go side with the finding it would otherwise have
// reported, and the report is pending until a merge reads it.
func pendingBase(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dir, err)
	}
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "// Command app is the target of a pending assertion.\npackage main\n\n" +
			"func main() { kept() }\n\n" +
			"// kept is what the entry point calls.\nfunc kept() {}\n\n" +
			"// Generated is paired with the TypeScript generated from it and referenced by\n" +
			"// nothing in this module.\nfunc Generated() {}\n",
		"deadset-edges.json": `{
  "description": "One edge pairing a declaration of this module with the TypeScript generated from it.",
  "edges": [
    {
      "id": "wire/Generated",
      "because": "generated",
      "provides": "go://example.com/app#Generated",
      "used_by": "ts://@example/app/src/wire.ts#Generated"
    }
  ]
}
`,
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	if err := writeFiles(dir, files); err != nil {
		t.Fatalf("Setup: write the target module: %v", err)
	}
	return base
}

// analyzerBinary builds the command and answers the path of the binary, so an
// assertion about an exit code reads the status of the real process rather than the
// return value of a function inside the test binary.
//
// The build carries the flags a published build carries, so what the binary reports
// about itself is what a published one reports.
func analyzerBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "deadset-go")
	build := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("Setup: build the command: %v\n%s", err, out)
	}
	return binary
}

// TestAnalyzeAloneOverADeclaredEdgeExitsPending is the single-analyzer pending
// assertion: the analyzer running by itself over a target holding a declared
// cross-language edge exits with the pending code rather than with the clean one, and
// names how many pending findings its report holds.
//
// The run is the command itself, invoked as a process with no orchestrator, so what is
// asserted is the status the operating system reports and the diagnostic a maintainer
// reads, not a code returned inside this test binary. A report holding an evaluation
// the merge has not resolved is not a clean tree, and reading one as clean is the
// failure this exit code exists to prevent.
func TestAnalyzeAloneOverADeclaredEdgeExitsPending(t *testing.T) {
	base := pendingBase(t)
	binary := analyzerBinary(t)

	command := exec.CommandContext(t.Context(), binary,
		"analyze", "--target=./app", "--report=report.json")
	command.Dir = base
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("%s analyze over a target holding a declared edge = %v, want a non-zero exit\nstderr: %s",
			binary, err, stderr.String())
	}
	if got, want := exited.ExitCode(), contractExitCodes(t)["pending"]; got != want {
		t.Errorf("%s analyze over a target holding a declared edge exited %d, want %d\nstderr: %s",
			binary, got, want, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("analyze wrote %q to stdout, want nothing: the report is a file and the verdict is the exit code", stdout.String())
	}
	for _, want := range []string{"1 pending finding", "no merge has resolved it"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("analyze stderr = %q, want it to name %q", stderr.String(), want)
		}
	}

	// The count on stderr is the count in the document, and the evaluation carries
	// the finding the run did not report, which is what a merge reads.
	body, err := os.ReadFile(filepath.Join(base, "report.json"))
	if err != nil {
		t.Fatalf("read the report the run wrote: %v", err)
	}
	var document struct {
		Totals struct {
			Pending  int `json:"pending"`
			Findings int `json:"findings"`
		} `json:"totals"`
		EdgeEvaluations []struct {
			Edge    string `json:"edge"`
			Side    string `json:"side"`
			State   string `json:"state"`
			Finding *struct {
				Code string `json:"code"`
			} `json:"finding"`
		} `json:"edge_evaluations"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the report the run wrote: %v", err)
	}
	if document.Totals.Pending != 1 {
		t.Errorf("the report names %d pending findings, want 1", document.Totals.Pending)
	}
	if len(document.EdgeEvaluations) != 1 {
		t.Fatalf("the report publishes %d edge evaluations, want one per own-language side of the declared edge",
			len(document.EdgeEvaluations))
	}
	evaluation := &document.EdgeEvaluations[0]
	if evaluation.Edge != "wire/Generated" || evaluation.Side != "provides" || evaluation.State != "dead" {
		t.Errorf("the report evaluates %+v, want the provides side of wire/Generated dead", evaluation)
	}
	if evaluation.Finding == nil || evaluation.Finding.Code == "" {
		t.Errorf("the dead evaluation carries the finding %+v, want the finding the run would otherwise have reported",
			evaluation.Finding)
	}
}

package deadsetgo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/host"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/corpus"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The corpus run is a package of its own so that its test binary runs beside the
// command's, which a shared package would run one after the other.

// goldenVariable is the gate that rewrites the committed results document, and
// goldenCommand the invocation a failure names.
const (
	goldenVariable = "UPDATE_GOLDEN"
	goldenCommand  = "UPDATE_GOLDEN=1 go test . -run TestConformanceCorpus"
	resultsFile    = "conformance-results.json"
	product        = "deadset-go"
)

// corpusJobs is how many fixtures the corpus run answers at once. A fixture's
// analyses mostly wait on the go command each starts, whose start-up and reads of
// the toolchain's files cost several times more on Windows than elsewhere, so four
// per processor keeps the host busy.
var corpusJobs = 4 * runtime.GOMAXPROCS(0)

// processVariable set to 1 has the corpus run answer through the built command,
// one process per invocation, rather than through its host, against the same
// committed document, so the binary a release ships answers the corpus too. The
// weekly properties job sets it.
const processVariable = "DEADSET_CORPUS_PROCESS"

// binary is the command, built once by TestMain with no version control state
// stamped, so the version it states is the development version at every commit,
// and analyzer its host form, which answers the corpus in one process.
var (
	binary   string
	analyzer *host.Host
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run is TestMain's body, written so that the removal of the binary runs before
// the exit.
func run(m *testing.M) int {
	dir, err := sandbox.MkdirTemp("", "deadset-go-conformance")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create the binary directory: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary = filepath.Join(dir, product)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", binary, "./cmd/deadset-go")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build the command: %v\n%s", err, out)
		return 1
	}
	if os.Getenv(processVariable) == "1" {
		return m.Run()
	}
	hostBinary := filepath.Join(dir, product+"-host"+filepath.Ext(binary))
	if err := host.Build(context.Background(), filepath.Join("cmd", "deadset-go"), hostBinary); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	hostDir, err := sandbox.MkdirTemp(dir, "host")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create the host's directory: %v\n", err)
		return 1
	}
	if analyzer, err = host.Start(hostBinary, hostDir, corpus.HostEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	if err := analyzer.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return code
}

// corpusAnalyzer is what the corpus run answers through: the command's host, or
// the built command where processVariable is 1.
func corpusAnalyzer() corpus.Analyzer {
	if os.Getenv(processVariable) == "1" {
		return corpus.Analyzer{Command: []string{binary}, Language: string(vocab.Go)}
	}
	return corpus.Analyzer{InProcess: analyzer.Invoke, Language: string(vocab.Go)}
}

// TestConformanceCorpus answers the pinned corpus through [corpusAnalyzer], and
// holds the run to the committed results document.
func TestConformanceCorpus(t *testing.T) {
	t.Parallel()
	var gaps corpus.Gaps
	if err := corpus.DecodeStrict(Gaps, &gaps); err != nil {
		t.Fatalf("Setup: decode conformance.json: %v", err)
	}
	results, err := corpus.Run(t.Context(), spec.Corpus,
		corpusAnalyzer(),
		corpus.Options{Gaps: gaps.Gaps, Jobs: corpusJobs, Scratch: sandbox.Dir(t)})
	if err != nil {
		t.Fatalf("corpus.Run = %v, want the results document", err)
	}
	got, err := corpus.Encode(results)
	if err != nil {
		t.Fatalf("corpus.Encode: %v", err)
	}
	if os.Getenv(goldenVariable) == "1" {
		if err := sandbox.WriteGolden(resultsFile, got, 0o600); err != nil {
			t.Fatalf("write %s: %v", resultsFile, err)
		}
	}

	for _, row := range results.Fixtures {
		if row.Result == corpus.Pending {
			continue
		}
		if row.Result == corpus.Fail && row.Message != "" {
			t.Errorf("%s: %s", row.Fixture, row.Message)
		}
		for _, held := range row.Expectations {
			if held.Result == corpus.Fail || held.Message == corpus.StaleGap {
				t.Errorf("%s %s %s: %s", row.Fixture, held.Symbol, held.Result, held.Message)
			}
		}
		for _, one := range row.Unexpected {
			t.Errorf("%s: %s at %s:%d, which no expectation names", row.Fixture, one.Report, one.File, one.Line)
		}
	}
	if results.Result != corpus.Pass {
		t.Errorf("the corpus result is %s, want pass", results.Result)
	}
	if err := corpus.Validate(corpus.ResultsSchemaPath, got); err != nil {
		t.Errorf("the results document does not meet %s: %v", corpus.ResultsSchemaPath, err)
	}
	fixtures, err := corpus.Load(spec.Corpus)
	if err != nil {
		t.Fatalf("Setup: corpus.Load: %v", err)
	}
	for _, err := range corpus.Unanswered(fixtures, results, gaps.Gaps) {
		t.Error(err)
	}
	if !bytes.Equal(Results, got) {
		t.Errorf("%s differs from this run (regenerate with %s)", resultsFile, goldenCommand)
	}
}

// TestTheCorpusRunAnswersThroughTheBuiltCommandWhereProcessIsSet pins what
// processVariable selects, and that the weekly properties job sets it, so one CI
// leg answers the corpus through the binary a release ships.
func TestTheCorpusRunAnswersThroughTheBuiltCommandWhereProcessIsSet(t *testing.T) {
	t.Setenv(processVariable, "1")
	if got := corpusAnalyzer(); !slices.Equal(got.Command, []string{binary}) || got.InProcess != nil {
		t.Errorf("corpusAnalyzer() under %s=1 = %v (in process: %t), want the built command %s alone",
			processVariable, got.Command, got.InProcess != nil, binary)
	}
	t.Setenv(processVariable, "")
	if got := corpusAnalyzer(); got.Command != nil || got.InProcess == nil {
		t.Errorf("corpusAnalyzer() with %s unset = %v (in process: %t), want the host alone", processVariable, got.Command, got.InProcess != nil)
	}
	const workflow = ".github/workflows/weekly-properties.yaml"
	body, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", workflow, err)
	}
	if !bytes.Contains(body, []byte(processVariable+`: "1"`)) {
		t.Errorf("%s does not set %s: \"1\", so no CI leg answers the corpus through the built command", workflow, processVariable)
	}
}

// TestCommandLineVectors runs every command-line vector naming this analyzer
// through the built command; a pending case reports what it awaits.
func TestCommandLineVectors(t *testing.T) {
	t.Parallel()
	results, err := corpus.RunCases(t.Context(), spec.Vectors, product, corpus.Analyzer{Command: []string{binary}})
	if err != nil {
		t.Fatalf("corpus.RunCases = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no command-line vector names this analyzer")
	}
	for _, result := range results {
		if result.Result == corpus.Fail {
			t.Errorf("%s: %s", result.Case, result.Message)
		}
	}
}

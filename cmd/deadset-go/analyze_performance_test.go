package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// moduleSize is what one synthesized module holds: the packages the run loads, the
// files they are written across, and the lines of Go source in them. Every field is
// a fact about the generated tree rather than a target, so the record names what the
// generator produced and a test reads the two against each other.
type moduleSize struct {
	Packages int `json:"packages"`
	Files    int `json:"files"`
	Lines    int `json:"lines"`
}

// synthesizeModule writes a module of packages packages, each of filesPerPackage
// files declaring declarations functions, under dir, and answers the size of what it
// wrote.
//
// The generation is deterministic: the packages, the files and the declarations are
// numbered, every body is written from those numbers, and nothing is drawn. Two runs
// of the generator therefore produce byte-identical trees, which is what lets a
// committed record name the tree's size and a later run check it.
//
// Each file's declarations form a chain, every one calling the next, and the first
// declaration of each file is called by the file before it, so one declaration per
// package is referenced by nothing and the analysis has a dead component to report
// in every package. No file imports anything and no file is a test file, which keeps
// the cost of the load the cost of type-checking the generated source itself.
func synthesizeModule(sink failureSink, dir string, packages, files, declarations int) moduleSize {
	sink.Helper()

	written := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	lines := countLines(written["go.mod"]) + countLines(written[repositoryDocument])
	for p := range packages {
		name := "p" + strconv.Itoa(p)
		for f := range files {
			body := synthesizedFile(name, f, declarations)
			written[filepath.Join(name, "f"+strconv.Itoa(f)+".go")] = body
			lines += countLines(body)
		}
	}
	if err := writeFiles(dir, written); err != nil {
		sink.Fatalf("Setup: write the synthesized module: %v", err)
	}
	return moduleSize{Packages: packages, Files: packages * files, Lines: lines}
}

// synthesizedFile is one generated file: the package clause, then one chain of
// declarations calling each other.
func synthesizedFile(pkg string, file, declarations int) string {
	var body strings.Builder
	fmt.Fprintf(&body, "// Package %s is a generated package.\npackage %s\n", pkg, pkg)
	for d := range declarations {
		name := fmt.Sprintf("f%dd%d", file, d)
		call := ""
		if d+1 < declarations {
			call = fmt.Sprintf("f%dd%d()", file, d+1)
		}
		fmt.Fprintf(&body, "\n// %s is a generated declaration.\nfunc %s() { %s }\n", name, name, call)
	}
	return body.String()
}

// countLines is the lines of one generated file, which is the newlines in it because
// every generated body ends with one.
func countLines(body string) int {
	return strings.Count(body, "\n")
}

// benchmarkRecord is cmd/deadset-go/testdata/benchmark.json: the budget the analysis
// is held to, the machine one measurement was taken on, and what that measurement
// answered for each of the two module shapes.
type benchmarkRecord struct {
	Description string           `json:"description"`
	Budget      benchmarkBudget  `json:"budget"`
	Machine     benchmarkMachine `json:"machine"`
	Runs        []benchmarkRun   `json:"runs"`
}

// benchmarkBudget is the published baseline and the budget stated against it: the
// size and the time of the reference measurement, and the size and the time the
// analysis is required to stay inside on a runner of the stated cores.
type benchmarkBudget struct {
	ReferenceLines   int     `json:"reference_lines"`
	ReferenceSeconds float64 `json:"reference_seconds"`
	BudgetLines      int     `json:"budget_lines"`
	BudgetSeconds    float64 `json:"budget_seconds"`
	BudgetCores      int     `json:"budget_cores"`
}

// benchmarkMachine is the machine the recorded times were measured on, which is what
// makes a later measurement comparable or explains why it is not.
type benchmarkMachine struct {
	Cores     int    `json:"cores"`
	Goarch    string `json:"goarch"`
	GoVersion string `json:"go_version"`
	Race      bool   `json:"race_detector"`
}

// benchmarkRun is one measurement: the module shape it was taken over, the
// configurations the matrix held, the findings the run reported and the wall time it
// took.
type benchmarkRun struct {
	Name           string     `json:"name"`
	Generator      generator  `json:"generator"`
	Size           moduleSize `json:"size"`
	Configurations int        `json:"configurations"`
	Findings       int        `json:"findings"`
	Seconds        float64    `json:"seconds"`
}

// generator is the three numbers the generator takes, so the record names how its
// module is produced rather than only how big it came out.
type generator struct {
	Packages            int `json:"packages"`
	FilesPerPackage     int `json:"files_per_package"`
	DeclarationsPerFile int `json:"declarations_per_file"`
}

// recordPath is the committed record, beside the tests that read it.
const recordPath = "testdata/benchmark.json"

// readBenchmarkRecord decodes the committed record, refusing any member it does not
// declare so a field added to the document without a reader fails here.
func readBenchmarkRecord(sink failureSink) benchmarkRecord {
	sink.Helper()

	body, err := os.ReadFile(recordPath)
	if err != nil {
		sink.Fatalf("Setup: read the benchmark record %s: %v", recordPath, err)
	}
	var record benchmarkRecord
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		sink.Fatalf("Setup: decode %s: %v", recordPath, err)
	}
	return record
}

// analyzeSynthesized runs analyze over one synthesized module from the directory the
// module was written under, and answers the wall time of the run and the findings it
// reported. The report goes to a sibling of the module, because a report names the
// target relative to the directory the run was invoked from.
func analyzeSynthesized(ctx context.Context, sink failureSink, base, name string) (time.Duration, int) {
	sink.Helper()

	reportPath := filepath.Join(base, name+".report.json")
	args := []string{"analyze", "--target=./" + name, "--report=" + reportPath, "--exit-code=" + exitCodeOff}

	var stderr strings.Builder
	started := time.Now()
	if code := run(ctx, args, io.Discard, &stderr); code != exitClean {
		sink.Fatalf("analyze over the synthesized module %s = %d, want %d with the exit code configured off\nstderr: %s",
			name, code, exitClean, stderr.String())
	}
	elapsed := time.Since(started)

	body, err := os.ReadFile(reportPath)
	if err != nil {
		sink.Fatalf("read the report of the synthesized module: %v", err)
	}
	var document struct {
		Totals struct {
			Findings int `json:"findings"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		sink.Fatalf("decode the report of the synthesized module: %v", err)
	}
	return elapsed, document.Totals.Findings
}

// writeSynthesized generates one recorded run's module under a directory of its own
// and returns the base the run is invoked from and the module's size.
func writeSynthesized(t *testing.T, recorded *benchmarkRun) (base string, size moduleSize) {
	t.Helper()

	base = t.TempDir()
	dir := filepath.Join(base, recorded.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dir, err)
	}
	return base, synthesizeModule(t, dir,
		recorded.Generator.Packages, recorded.Generator.FilesPerPackage, recorded.Generator.DeclarationsPerFile)
}

// TestTheBenchmarkRecordNamesWhatTheGeneratorProduces reads the committed record
// against the generator it names: every recorded run's module is generated again and
// its size compared, so a record whose numbers no longer describe the tree fails
// here rather than being carried into a release.
//
// Generating a module writes files and reads none, so this test costs no analysis and
// the recorded times are not re-measured here. The re-measurement is the benchmark
// beside it, which a release runs: a wall time asserted in the unit suite would
// measure a shared runner's load rather than the analysis.
func TestTheBenchmarkRecordNamesWhatTheGeneratorProduces(t *testing.T) {
	record := readBenchmarkRecord(t)

	if len(record.Runs) == 0 {
		t.Fatal("Setup: the benchmark record names no run, so this test pins nothing")
	}
	for i := range record.Runs {
		recorded := &record.Runs[i]
		t.Run(recorded.Name, func(t *testing.T) {
			_, size := writeSynthesized(t, recorded)
			if size != recorded.Size {
				t.Errorf("the generator of the %s run produced %+v, want the recorded %+v (regenerate the record with %s)",
					recorded.Name, size, recorded.Size, recordCommand)
			}
		})
	}
}

// recordCommand is the invocation that measures the record's times again, which a
// failure names so a reader does not go looking for it.
const recordCommand = "go test -run='^$' -bench=BenchmarkAnalyzeTheRecordedModules ./cmd/deadset-go/"

// TestTheBenchmarkRecordStatesTheBudgetTheDesignSets pins the record's budget
// against the figures the analysis is required to meet, so a record edited to make a
// measurement look inside the budget fails here.
func TestTheBenchmarkRecordStatesTheBudgetTheDesignSets(t *testing.T) {
	record := readBenchmarkRecord(t)

	// The required budget: a module of about 250,000 lines analyzed within 60
	// seconds on a four-core runner, measured against the reference of 0.9 seconds
	// for a 21,441-line module.
	want := benchmarkBudget{
		ReferenceLines:   21441,
		ReferenceSeconds: 0.9,
		BudgetLines:      250000,
		BudgetSeconds:    60,
		BudgetCores:      4,
	}
	if record.Budget != want {
		t.Errorf("the benchmark record states the budget %+v, want %+v", record.Budget, want)
	}
	if record.Machine.Cores == 0 || record.Machine.Goarch == "" || record.Machine.GoVersion == "" {
		t.Errorf("the benchmark record names the machine %+v, want the cores, the architecture and the toolchain of the measurement",
			record.Machine)
	}

	// Every recorded time is inside the budget stated for its own size, which is
	// the claim the record exists to carry: the reference run at or under the
	// published reference, and the budget run inside the minute. The recorded
	// machine has more cores than the budget's runner, so a recorded time close to
	// either figure is a finding rather than a pass.
	for i := range record.Runs {
		recorded := &record.Runs[i]
		against := record.Budget.BudgetSeconds
		if recorded.Size.Lines < record.Budget.BudgetLines/2 {
			against = record.Budget.ReferenceSeconds
		}
		if recorded.Seconds > against {
			t.Errorf("the %s run of %d lines is recorded at %.2f s, want at most the %.2f s the budget states for that size",
				recorded.Name, recorded.Size.Lines, recorded.Seconds, against)
		}
	}
}

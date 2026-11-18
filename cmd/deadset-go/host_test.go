package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/hostwire"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/host"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// hostBuilt is the command's host form, which [runTests] builds under the fixture
// directory before any test runs.
var hostBuilt struct {
	path string
	err  error
}

// buildHost builds the host form for [hostBinary].
func buildHost() {
	hostBuilt.path = filepath.Join(fixtureBase, "bin", "deadset-go-host")
	if runtime.GOOS == "windows" {
		hostBuilt.path += ".exe"
	}
	hostBuilt.err = host.Build(context.Background(), packageDir, hostBuilt.path, "deadset_names")
}

// hostBinary is the built host form; like [commandBinary], no test's deadline
// counts its build.
func hostBinary(t *testing.T) string {
	t.Helper()
	if hostBuilt.err != nil || hostBuilt.path == "" {
		t.Fatalf("Setup: build the command's host: %v", hostBuilt.err)
	}
	return hostBuilt.path
}

// hosted is the host every test of the package that runs an invocation through it
// shares, started from an empty directory under the fixture directory; runTests
// closes it.
var hosted struct {
	once sync.Once
	host *host.Host
	err  error
}

// commandHost is the package's host, started on first use.
func commandHost(t *testing.T) *host.Host {
	t.Helper()
	binary := hostBinary(t)
	hosted.once.Do(func() {
		dir, err := sandbox.MkdirTemp(fixtureBase, "host")
		if err != nil {
			hosted.err = err
			return
		}
		hosted.host, hosted.err = host.Start(binary, dir, nil)
	})
	if hosted.err != nil {
		t.Fatalf("Setup: start the command's host: %v", hosted.err)
	}
	return hosted.host
}

// closeHost ends the package's host where a test started it.
func closeHost() error {
	if hosted.host == nil {
		return nil
	}
	return hosted.host.Close()
}

// TestTheHostAnswersEachInvocationAsAProcessRunFromItsDirectory holds the host to
// the process: two invocations at once, each from its own module's directory and
// naming its paths relative to it, print, exit with and write what the built
// command run from that directory does.
func TestTheHostAnswersEachInvocationAsAProcessRunFromItsDirectory(t *testing.T) {
	t.Parallel()

	modules := []string{
		writeModule(t, map[string]string{
			"go.mod": "module example.com/a\n\ngo 1.27.1\n",
			"a.go":   "// Package a exports one declaration nothing uses.\npackage a\n\n// Unused is used by nothing.\nfunc Unused() {}\n",
		}),
		writeModule(t, map[string]string{
			"go.mod":           "module example.com/b\n\ngo 1.27.1\n",
			"main.go":          "// Command b calls one of its functions.\npackage main\n\nfunc main() { called() }\n\nfunc called() {}\n\nfunc forgotten() {}\n",
			repositoryDocument: `{"target": {"kind": "application"}}`,
		}),
	}
	args := [][]string{
		{"analyze", "--target=.", "--report=report.json"},
		{"print-roots", "--target=."},
	}
	h := commandHost(t)
	for _, invocation := range args {
		processes := make([]invoked, len(modules))
		processWrote := make([]string, len(modules))
		for i, dir := range modules {
			processes[i] = processIn(t, dir, nil, invocation...)
			processWrote[i] = takeReport(t, dir)
		}
		answers := make([]invoked, len(modules))
		var wg sync.WaitGroup
		for i, dir := range modules {
			wg.Go(func() {
				stdout, stderr, code, err := h.Run(t.Context(), dir, invocation)
				if err != nil {
					t.Errorf("Run(%s, %q) = %v", dir, invocation, err)
				}
				answers[i] = invoked{stdout: string(stdout), stderr: stderr, code: code}
			})
		}
		wg.Wait()
		for i, dir := range modules {
			if answers[i] != processes[i] {
				t.Errorf("the host's %q from %s = %+v, want the process's %+v", invocation, dir, answers[i], processes[i])
			}
			if wrote := takeReport(t, dir); wrote != processWrote[i] {
				t.Errorf("the host's %q from %s wrote\n%s\nwant the process's\n%s", invocation, dir, wrote, processWrote[i])
			}
		}
	}
}

// The host reads its input while its answers wait on their reader: a client that
// writes every request before it reads an answer, more of both than a pipe holds,
// is answered in full within a minute.
func TestTheHostTakesEveryRequestWhileItsAnswersWaitOnTheirReader(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	process := exec.CommandContext(ctx, hostBinary(t))
	process.Dir = sandbox.Dir(t)
	input, err := process.StdinPipe()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := process.Start(); err != nil {
		t.Fatalf("Setup: start the host: %v", err)
	}
	const requests = 64
	arg := strings.Repeat("x", 8<<10)
	written := make(chan error, 1)
	go func() {
		encoder := json.NewEncoder(input)
		for id := range uint64(requests) {
			if err := encoder.Encode(hostwire.Request{ID: id + 1, Args: []string{"version", arg}}); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write the requests: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("the host took fewer than %d requests of 8 KiB within a minute while no answer was read: it stopped reading its input while an answer waited", requests)
	}
	answers := json.NewDecoder(output)
	for range requests {
		var answer hostwire.Response
		if err := answers.Decode(&answer); err != nil {
			t.Fatalf("read an answer: %v", err)
		}
		if answer.Code != vocab.ExitUsage {
			t.Errorf("request %d answered %d, want %d for version with an argument", answer.ID, answer.Code, vocab.ExitUsage)
		}
	}
	if err := errors.Join(input.Close(), process.Wait()); err != nil {
		t.Errorf("the host, its input closed with every request answered, exited with %v, want 0", err)
	}
}

// takeReport is the report.json an invocation wrote in dir, empty where it wrote
// none, removed once read.
func takeReport(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "report.json")
	body, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Setup: remove %s: %v", path, err)
	}
	return string(body)
}

// TestTheHostEndsACancelledRequestAsAnInterruptedRun holds the host to the
// interrupt a process gets: a request whose context ends is answered with the
// interrupted-run class and the failure code.
func TestTheHostEndsACancelledRequestAsAnInterruptedRun(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, map[string]string{
		"go.mod":           "module example.com/a\n\ngo 1.27.1\n",
		"a.go":             "// Package a exports one declaration nothing uses.\npackage a\n\n// Unused is used by nothing.\nfunc Unused() {}\n",
		repositoryDocument: `{"target": {"kind": "library"}}`,
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, stderr, code, err := commandHost(t).Run(ctx, dir, []string{"analyze", "--target=.", "--report=" + filepath.Join(sandbox.Dir(t), "report.json")})
	if err != nil || code != vocab.ExitFailure || !strings.HasPrefix(stderr, vocab.RunFailurePrefix+vocab.RunInterruptedRun+":") {
		t.Errorf("Run(a cancelled analyze) = %d writing %q (%v), want %d and the %s class", code, stderr, err, vocab.ExitFailure, vocab.RunInterruptedRun)
	}
}

// runners is how each test file of the package runs the command outside the test
// binary: through the host (invokedIn), or as a process (processIn, or the binary
// commandBinary builds) where the process itself is the subject: its signals,
// environment, deadline or working directory as it reads them. A file that changes
// how it runs the command names it here, so a test whose subject is the process
// does not move onto the host unnoticed.
var runners = map[string][]string{
	"analyze_determinism_test.go":    {"processIn"},
	"analyze_entries_unix_test.go":   {"commandBinary"},
	"analyze_pending_test.go":        {"commandBinary"},
	"analyze_prop_test.go":           {"invokedIn"},
	"analyze_test.go":                {"invokedIn", "processIn"},
	"budget_test.go":                 {"commandBinary"},
	"case_variant_test.go":           {"invokedIn"},
	"conformance_test.go":            {"commandBinary"},
	"explain_test.go":                {"invokedIn"},
	"facts_schema_reads_test.go":     {"invokedIn"},
	"findings_ledger_test.go":        {"invokedIn"},
	"helpers_test.go":                {"commandBinary"},
	"hidden_prop_unix_test.go":       {"commandBinary"},
	"host_test.go":                   {"processIn"},
	"linked_members_unix_test.go":    {"commandBinary"},
	"main_matrix_test.go":            {"invokedIn"},
	"signal_unix_test.go":            {"commandBinary"},
	"symlinked_paths_test.go":        {"processIn"},
	"unplaced_packages_unix_test.go": {"commandBinary"},
	"workdir_test.go":                {"processIn"},
}

// TestNoTestBuildsTheCommandInsideItsOwnRun holds every build of the command to
// runTests, before any test runs: a build inside a test counts against the
// deadline the test sets, and a coverage or toolexec build outlasts a minute.
func TestNoTestBuildsTheCommandInsideItsOwnRun(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("Setup: list the test files: %v", err)
	}
	fset := token.NewFileSet()
	var got []string
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, held := decl.(*ast.FuncDecl)
			if !held || fn.Body == nil {
				continue
			}
			// A builder named anywhere in a body, called or handed on (builds.Go),
			// is that body building.
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.Ident:
					if (n.Name == "buildCommand" || n.Name == "buildHost") && !slices.Contains(got, fn.Name.Name+" calls "+n.Name) {
						got = append(got, fn.Name.Name+" calls "+n.Name)
					}
				case *ast.SelectorExpr:
					if id, held := n.X.(*ast.Ident); held && id.Name == "host" && n.Sel.Name == "Build" {
						got = append(got, fn.Name.Name+" calls host.Build")
					}
				}
				return true
			})
		}
	}
	slices.Sort(got)
	want := []string{"buildHost calls host.Build", "runTests calls buildCommand", "runTests calls buildHost"}
	if !slices.Equal(got, want) {
		t.Errorf("the test files build the command at %q, want %q: only runTests builds, before any test runs", got, want)
	}
}

func TestEveryTestFileRunsTheCommandAsItsRunnersEntryNames(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("Setup: list the test files: %v", err)
	}
	fset := token.NewFileSet()
	got := map[string][]string{}
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, held := node.(*ast.CallExpr)
			if !held {
				return true
			}
			if fn, held := call.Fun.(*ast.Ident); held && slices.Contains([]string{"commandBinary", "invokedIn", "processIn"}, fn.Name) && !slices.Contains(got[name], fn.Name) {
				got[name] = append(got[name], fn.Name)
			}
			return true
		})
	}
	for name := range got {
		slices.Sort(got[name])
	}
	if !maps.EqualFunc(got, runners, slices.Equal) {
		t.Errorf("the test files run the command through\n%v\nwant runners\n%v", got, runners)
	}
}

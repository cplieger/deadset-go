package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-go/v2/internal/testsupport"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// failureSink is what a fixture helper needs of the test that calls it: mark itself a
// helper, and end that test with a diagnostic. Both *testing.T and *rapid.T satisfy
// it, which is what lets the unit suite and the properties share one set of fixtures.
// Neither a temporary directory nor a context is in that method set, so the context
// arrives as a parameter and the directory comes from the cache.
type failureSink interface {
	Helper()
	Fatalf(format string, args ...any)
}

// fixtureBase is the directory the shared fixture modules are written under. A shared
// module is declared by more than one test, so its directory outlives whichever one
// declared it first and no t.TempDir can own it; TestMain creates this directory and
// removes it when the package's tests have run.
var fixtureBase string

// fixtures holds one entry per distinct archive, keyed by the archive's digest, so
// that a module several tests declare is written once and analyzed once however many
// of them declare it. One analysis of a fixture module carrying a test file costs
// about a second under the race detector, which is the whole reason the cache exists.
var fixtures sync.Map

// fixture is one archive as the cache holds it: the files it declares, the directory
// they were written to, the findings of one analysis of that directory, and whichever
// of the two steps failed.
type fixture struct {
	files map[string]string

	written  sync.Once
	dir      string
	writeErr error

	analyzed   sync.Once
	set        *run.Findings
	analyzeErr error

	reported  sync.Once
	envelope  *report.Report
	reportErr error
}

// archiveKey names one archive by its contents: every path and every body, in path
// order. Two tests declaring the same files therefore name one directory and one
// analysis, and a test that changes a byte of its fixture names another.
func archiveKey(files map[string]string) string {
	digest := sha256.New()
	for _, path := range slices.Sorted(maps.Keys(files)) {
		digest.Write([]byte(path))
		digest.Write([]byte{0})
		digest.Write([]byte(files[path]))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// fixtureFor is the cache entry one archive is held under.
func fixtureFor(files map[string]string) *fixture {
	entry, _ := fixtures.LoadOrStore(archiveKey(files), &fixture{files: files})
	held, _ := entry.(*fixture)
	return held
}

// directory writes the archive under the package's fixture directory on the first
// declaration of it, and answers the same directory to every later one.
func (f *fixture) directory() (string, error) {
	f.written.Do(func() {
		dir, err := os.MkdirTemp(fixtureBase, "module")
		if err != nil {
			f.writeErr = err
			return
		}
		f.dir, f.writeErr = dir, writeFiles(dir, f.files)
	})
	return f.dir, f.writeErr
}

// findings analyzes the archive's directory on the first request and answers the same
// findings to every later one.
func (f *fixture) findings(ctx context.Context) (*run.Findings, error) {
	dir, err := f.directory()
	if err != nil {
		return nil, err
	}
	f.analyzed.Do(func() {
		var refused strings.Builder
		req, code := resolve(ctx, "print-roots", printRootsUsage, []string{"--target=" + dir}, &refused)
		if code != vocab.ExitClean {
			f.analyzeErr = fmt.Errorf("resolve the configuration of %s = %d: %s", dir, code, refused.String())
			return
		}
		f.set, f.analyzeErr = findingsOf(ctx, req)
	})
	return f.set, f.analyzeErr
}

// report assembles the archive's report on the first request and answers the same
// envelope to every later one.
//
// A report names the target relative to the directory the run was invoked from, so the
// caller has already made the archive's own directory the working directory; every
// caller of one archive therefore names the same directory and reads an envelope built
// under it.
func (f *fixture) report(ctx context.Context) (*report.Report, error) {
	dir, err := f.directory()
	if err != nil {
		return nil, err
	}
	f.reported.Do(func() {
		var refused strings.Builder
		req, code := resolve(ctx, "print-roots", printRootsUsage, []string{"--target=" + dir}, &refused)
		if code != vocab.ExitClean {
			f.reportErr = fmt.Errorf("resolve the configuration of %s = %d: %s", dir, code, refused.String())
			return
		}
		f.envelope, f.reportErr = reportOf(ctx, req, corpusAnswered())
	})
	return f.envelope, f.reportErr
}

// reportOf is the report of one run.
func reportOf(ctx context.Context, req *run.Request, self *run.Self) (*report.Report, error) {
	found, err := findingsOf(ctx, req)
	if err != nil {
		return nil, err
	}
	return found.Report(self)
}

// sharedModule writes one archive and returns its directory, which is the same
// directory for every test declaring the same files.
//
// The directory belongs to the cache and not to the caller: a test that needs a
// document inside its target declares that document as part of the archive, and a test
// that writes into its target at all uses writeModule instead, which gives it a
// directory of its own.
func sharedModule(sink failureSink, files map[string]string) string {
	sink.Helper()

	dir, err := fixtureFor(files).directory()
	if err != nil {
		sink.Fatalf("Setup: write the fixture module: %v", err)
	}
	return dir
}

// cachedFindings is the findings of one run over the archive's module, computed once
// per archive however many tests ask for it. Each caller gets its own copy of the
// findings and evaluations; the stages before them are shared and read only.
func cachedFindings(ctx context.Context, sink failureSink, files map[string]string) *run.Findings {
	sink.Helper()

	set, err := fixtureFor(files).findings(ctx)
	if err != nil {
		sink.Fatalf("the findings of the fixture module: %v", err)
	}
	held := *set
	held.Result = testsupport.Detached(sink, set.Result)
	return &held
}

// cachedEnvelope is the report of one run over the archive's module, assembled once per
// archive however many tests ask for it. Each caller gets its own copy of the report.
func cachedEnvelope(ctx context.Context, sink failureSink, files map[string]string) *report.Report {
	sink.Helper()

	envelope, err := fixtureFor(files).report(ctx)
	if err != nil {
		sink.Fatalf("the report of the fixture module: %v", err)
	}
	return testsupport.Detached(sink, envelope)
}

// mustDecode is the report document data holds, ending the test, naming what it
// read, where the document is no report.
func mustDecode(t failureSink, what string, data []byte) *report.Report {
	t.Helper()

	read, err := report.Decode(data)
	if err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
	return read
}

// built is the command built once for every test of the package that runs it as a
// process, under the fixture directory, which outlives each of them.
var built struct {
	once sync.Once
	path string
	out  []byte
	err  error
}

// packageDir is the directory of this package, read before any test runs: a test that
// changes the working directory may be the first to ask for the command.
var packageDir string

// commandBinary builds the command on first use and answers the path of the binary.
// It stamps no version control state, so the version it states is the development
// version at every commit.
func commandBinary(t *testing.T) string {
	t.Helper()
	if packageDir == "" {
		t.Fatal("Setup: packageDir is unset, so the build would compile the working directory's package")
	}
	built.once.Do(func() {
		name := "deadset-go"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		built.path = filepath.Join(fixtureBase, "bin", name)
		build := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", built.path, ".")
		build.Dir = packageDir
		built.out, built.err = build.CombinedOutput()
	})
	if built.err != nil {
		t.Fatalf("Setup: build the command: %v\n%s", built.err, built.out)
	}
	return built.path
}

// invoked is what one invocation of the command wrote and the code it exited with.
type invoked struct {
	stdout, stderr string
	code           int
}

// invokedIn runs one invocation of the built command as a process from dir, so the
// directory the run is invoked from is that process's and the calling test can run
// in parallel with others. The command is built without the race detector, which
// multiplies an analysis's CPU time several times over: the in-process tests run the
// same analysis under it, and the signal test re-executes this binary ([reexecuted]).
func invokedIn(t *testing.T, dir string, args ...string) invoked {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), commandBinary(t), args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if _, exited := errors.AsType[*exec.ExitError](err); !exited {
			t.Fatalf("Setup: run %q from %s: %v", args, dir, err)
		}
	}
	return invoked{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}
}

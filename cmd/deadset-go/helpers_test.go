package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/internal/report"
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
	set        findingSet
	analyzeErr error

	reported  sync.Once
	envelope  report.Envelope
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
func (f *fixture) findings(ctx context.Context) (findingSet, error) {
	dir, err := f.directory()
	if err != nil {
		return findingSet{}, err
	}
	f.analyzed.Do(func() {
		var refused strings.Builder
		resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &refused)
		if code != exitClean {
			f.analyzeErr = fmt.Errorf("resolve the configuration of %s = %d: %s", dir, code, refused.String())
			return
		}
		options, optionsErr := exemptOptions(&resolved.config)
		if optionsErr != nil {
			f.analyzeErr = fmt.Errorf("exemptOptions(): %w", optionsErr)
			return
		}
		f.set, f.analyzeErr = findingsOf(ctx, &resolved, &options)
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
func (f *fixture) report(ctx context.Context) (report.Envelope, error) {
	dir, err := f.directory()
	if err != nil {
		return report.Envelope{}, err
	}
	f.reported.Do(func() {
		var refused strings.Builder
		resolved, code := resolve("print-roots", printRootsUsage, []string{"--target=" + dir}, &refused)
		if code != exitClean {
			f.reportErr = fmt.Errorf("resolve the configuration of %s = %d: %s", dir, code, refused.String())
			return
		}
		options, optionsErr := exemptOptions(&resolved.config)
		if optionsErr != nil {
			f.reportErr = fmt.Errorf("exemptOptions(): %w", optionsErr)
			return
		}
		f.envelope, f.reportErr = reportOf(ctx, &resolved, &options, corpusAnswered())
	})
	return f.envelope, f.reportErr
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

// cachedFindings is the findings of one run over the archive's module: the resolution a
// verb would build, the exemption options it would convert, and the pass itself,
// computed once per archive however many tests ask for it.
//
// Every caller reads one value, so no caller writes to what it returns.
func cachedFindings(ctx context.Context, sink failureSink, files map[string]string) findingSet {
	sink.Helper()

	set, err := fixtureFor(files).findings(ctx)
	if err != nil {
		sink.Fatalf("the findings of the fixture module: %v", err)
	}
	return set
}

// cachedEnvelope is the report of one run over the archive's module, assembled once per
// archive however many tests ask for it.
//
// Every caller reads one value, so no caller writes to what it returns.
func cachedEnvelope(ctx context.Context, sink failureSink, files map[string]string) report.Envelope {
	sink.Helper()

	envelope, err := fixtureFor(files).report(ctx)
	if err != nil {
		sink.Fatalf("the report of the fixture module: %v", err)
	}
	return envelope
}

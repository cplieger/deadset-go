package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// determinismModule writes the target every determinism assertion is measured over,
// under a directory of its own inside base: a module whose entry point reaches one of
// its declarations and not the others, so the run has findings of several kinds to
// render, and one ignore document the run reads, so a document inside the target is
// part of what a write would be likeliest to land on.
//
// No file imports anything and no file is a test file, which is what keeps one
// analysis of it cheap: the toolchain synthesizes no test main, so the load
// type-checks the target's own source and nothing else.
func determinismModule(t *testing.T, base string) {
	t.Helper()

	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "// Command app is the target of a determinism assertion.\npackage main\n\n" +
			"func main() { kept() }\n\n" +
			"// kept is what the entry point calls.\nfunc kept() {}\n\n" +
			"// forgotten is what nothing calls.\nfunc forgotten() {}\n\n" +
			"// Widened is exported and referenced by nothing outside this package.\n" +
			"func Widened() { kept() }\n",
		filepath.Join("lib", "lib.go"): "// Package lib is a package of the target.\npackage lib\n\n" +
			"// Unreached is exported and reached by nothing.\nfunc Unreached() {}\n",
		"deadset-ignore.json": `{
  "description": "One entry about a declaration the run reports.",
  "ignore": [
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#forgotten",
      "path": "app.go",
      "reason": "the deletion of this declaration is scheduled"
    }
  ]
}
`,
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	dir := filepath.Join(base, "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", dir, err)
	}
	if err := writeFiles(dir, files); err != nil {
		t.Fatalf("Setup: write the target module: %v", err)
	}
}

// templateName is the file the template rendering reads, and templateDocument its
// body. The template is written beside the target rather than inside it, because the
// run reads it and the target is what the report-only guarantee is about.
const (
	templateName     = "one.tmpl"
	templateDocument = "{{ range .Findings }}{{ .Code }} {{ .Symbol.Ref }}\n{{ end }}"
)

// determinismBase writes the target and the template, makes their directory the
// working directory and answers it. A report names the target relative to the
// directory the run was invoked from, so every document of every run below lands
// inside this one.
func determinismBase(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	determinismModule(t, base)
	if err := os.WriteFile(filepath.Join(base, templateName), []byte(templateDocument), 0o600); err != nil {
		t.Fatalf("Setup: write the template: %v", err)
	}
	t.Chdir(base)
	return base
}

// documentSuffixes is every document one analyze invocation of these tests writes,
// named by what the invocation appends to the report path: the report itself, the
// baseline, and one rendering per format this analyzer renders.
func documentSuffixes() []string {
	suffixes := []string{".json", ".json.baseline"}
	for _, format := range slices.Sorted(maps.Keys(renderings)) {
		suffixes = append(suffixes, ".json"+renderings[format].suffix)
	}
	return suffixes
}

// documentsOf runs analyze over the target of the working directory, writing its
// report at name.json, and answers the bytes of every document the run wrote, keyed by
// the suffix the invocation named it with. Two runs under different names are
// therefore compared key by key, and a report path that reached a document is a
// difference the comparison reads.
func documentsOf(t *testing.T, name string) map[string][]byte {
	t.Helper()

	reportPath := name + ".json"
	args := []string{
		"analyze", "--target=./app", "--report=" + reportPath,
		"--template=" + templateName, "--baseline-write=" + reportPath + ".baseline",
		"--exit-code=" + exitCodeOff,
	}
	for _, format := range slices.Sorted(maps.Keys(renderings)) {
		args = append(args, "--format="+string(format))
	}

	var stderr strings.Builder
	if code := run(t.Context(), args, io.Discard, &stderr); code != exitClean {
		t.Fatalf("run(%q) = %d, want %d with the exit code configured off\nstderr: %s",
			args, code, exitClean, stderr.String())
	}

	written := make(map[string][]byte, len(documentSuffixes()))
	for _, suffix := range documentSuffixes() {
		body, err := os.ReadFile(name + suffix)
		if err != nil {
			t.Fatalf("read the document %s the run wrote: %v", name+suffix, err)
		}
		written[suffix] = body
	}
	return written
}

// compareDocuments reports every document of two runs that differs, by the suffix that
// names it, so a failure says which rendering moved rather than that something did.
func compareDocuments(t *testing.T, first, second map[string][]byte, firstName, secondName string) {
	t.Helper()

	if !slices.Equal(slices.Sorted(maps.Keys(first)), slices.Sorted(maps.Keys(second))) {
		t.Fatalf("the %s run wrote %v and the %s run wrote %v, want the same set of documents",
			firstName, slices.Sorted(maps.Keys(first)), secondName, slices.Sorted(maps.Keys(second)))
	}
	for _, suffix := range slices.Sorted(maps.Keys(first)) {
		if !bytes.Equal(first[suffix], second[suffix]) {
			t.Errorf("the document a run writes at <report>%s differs between the %s run and the %s run:\n%s",
				suffix, firstName, secondName, documentDifference(first[suffix], second[suffix]))
		}
	}
}

// documentDifference is the first line at which two documents differ, with their
// lengths, for a failure that has to say what moved.
func documentDifference(first, second []byte) string {
	firstLines := strings.Split(string(first), "\n")
	secondLines := strings.Split(string(second), "\n")
	for i := range max(len(firstLines), len(secondLines)) {
		at, to := "", ""
		if i < len(firstLines) {
			at = firstLines[i]
		}
		if i < len(secondLines) {
			to = secondLines[i]
		}
		if at != to {
			return fmt.Sprintf("%d bytes against %d, first differing at line %d:\n--- %q\n+++ %q",
				len(first), len(second), i+1, at, to)
		}
	}
	return fmt.Sprintf("%d bytes against %d, and every line is equal", len(first), len(second))
}

// TestAnalyzeIsByteIdenticalToItsOwnRepetition asserts the first half of the
// determinism guarantee: two consecutive runs over one unchanged target write the same
// bytes, in the report, in the baseline and in every rendering.
//
// Nothing in the analysis is timed, sharded or bounded by a clock, and this is the
// assertion that says so: a time, a duration, a worker count or a path of the
// invocation reaching any document would differ between the two runs.
func TestAnalyzeIsByteIdenticalToItsOwnRepetition(t *testing.T) {
	determinismBase(t)

	first := documentsOf(t, "first")
	second := documentsOf(t, "second")

	if len(first) != len(renderings)+2 {
		t.Fatalf("one run wrote %d documents, want the report, the baseline and one per rendering, which is %d",
			len(first), len(renderings)+2)
	}
	compareDocuments(t, first, second, "first", "second")
}

// TestAnalyzeIsIndependentOfTheBuildCache asserts the second half: a run with an empty
// cache directory writes the same bytes as a run with a populated one.
//
// The analyzer writes no cache of its own, so the only cache a run reads is the
// toolchain's, which the load spawns its `go list` with. The first run is given a
// cache directory that does not exist yet and populates it; the second is given that
// directory, now populated. The directory is read between the two runs, because a
// comparison of two runs over two empty caches would pass while asserting nothing.
func TestAnalyzeIsIndependentOfTheBuildCache(t *testing.T) {
	base := determinismBase(t)
	cache := filepath.Join(base, "cache")

	t.Setenv("GOCACHE", cache)
	empty := documentsOf(t, "empty-cache")

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("read the cache directory the first run was given: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the cache directory is empty after the first run, so the second run reads no populated cache and this test asserts nothing")
	}

	populated := documentsOf(t, "populated-cache")
	compareDocuments(t, empty, populated, "empty-cache", "populated-cache")
}

// TestAnalyzeWritesOnlyTheDocumentsTheInvocationNames asserts the third guarantee: no
// code path of the analysis writes state to disk.
//
// Every file under the directory the run is invoked from is hashed before and after,
// which is the target tree, the template and the documents together, and the
// difference must be exactly the documents the invocation named. The temporary
// directory this process and the toolchain it spawns write through is pointed at a
// directory of its own and must be empty afterwards, which is what a file created and
// not removed would be. The toolchain's build cache lives outside both and is the one
// thing a run populates, which the assertion above is about.
func TestAnalyzeWritesOnlyTheDocumentsTheInvocationNames(t *testing.T) {
	temporary := t.TempDir()
	base := determinismBase(t)
	t.Setenv("TMPDIR", temporary)

	before := hashDirectory(t, base)
	written := documentsOf(t, "documents")
	after := hashDirectory(t, base)

	for suffix := range written {
		path := "documents" + suffix
		if _, present := after[path]; !present {
			t.Errorf("the run named the document %s and no file of the run directory is it", path)
		}
		delete(after, path)
	}
	if !maps.Equal(before, after) {
		t.Errorf("the run changed the directory it was invoked from, beyond the documents it named:\n%s",
			directoryDifference(before, after))
	}

	left, err := os.ReadDir(temporary)
	if err != nil {
		t.Fatalf("read the temporary directory of the run: %v", err)
	}
	if len(left) != 0 {
		names := make([]string, len(left))
		for i, entry := range left {
			names[i] = entry.Name()
		}
		t.Errorf("the run left %v in its temporary directory, want nothing: no analysis state outlives a run", names)
	}
}

// hashDirectory is the SHA-256 of every file under dir, keyed by the path relative to
// it, which is the file set and the contents in one value.
func hashDirectory(t *testing.T, dir string) map[string]string {
	t.Helper()

	hashes := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		digest := sha256.Sum256(body)
		hashes[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash the directory %s: %v", dir, err)
	}
	return hashes
}

// directoryDifference names every file whose presence or contents moved, for a failure
// that has to say what the run touched.
func directoryDifference(before, after map[string]string) string {
	var held strings.Builder
	for _, path := range slices.Sorted(maps.Keys(before)) {
		switch at, present := after[path]; {
		case !present:
			held.WriteString("removed: " + path + "\n")
		case at != before[path]:
			held.WriteString("rewritten: " + path + "\n")
		}
	}
	for _, path := range slices.Sorted(maps.Keys(after)) {
		if _, present := before[path]; !present {
			held.WriteString("added: " + path + "\n")
		}
	}
	return held.String()
}

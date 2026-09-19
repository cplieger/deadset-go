package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

// TestAnalysisLeavesTheTreeByteIdentical is property dead-code-suite/P23: a run of
// the analysis changes no file of the target.
//
// Every file of the tree is hashed before the run and after it, and the file set is
// compared as well as the contents, so a file added, removed or rewritten fails the
// property. The run is asked for every document the verb can write, because a write
// is the only thing that could touch the tree, and the report and the baseline are
// written outside the target so that the documents the run produces are not the
// change being measured.
//
// No deletion candidate is deleted and no visibility is changed by construction: each
// would be a byte the hash reads.
//
// One iteration writes a module, loads it and renders five documents, which costs
// about half a second, so the property is about a minute of the package's deadline.
func TestAnalysisLeavesTheTreeByteIdentical(t *testing.T) {
	// A report names the target relative to the directory the run was invoked from
	// and can name no path outside it, so the run is invoked from the directory the
	// generated trees are written under. The documents go in a sibling of those
	// trees, which is inside the run directory and outside every target.
	base := t.TempDir()
	documents := filepath.Join(base, "documents")
	if err := os.MkdirAll(documents, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", documents, err)
	}
	t.Chdir(base)

	rapid.Check(t, func(t *rapid.T) {
		drawn := drawModule(t, `{"target": {"kind": "application"}}`, false)
		dir, err := os.MkdirTemp(base, "module")
		if err != nil {
			t.Fatalf("create a directory for the generated module: %v", err)
		}
		if err := writeFiles(dir, drawn.files); err != nil {
			t.Fatalf("write the generated module: %v", err)
		}

		before := hashTree(t, dir)
		reportPath := filepath.Join(documents, filepath.Base(dir)+".json")
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{
			"analyze", "--target=" + dir, "--report=" + reportPath,
			"--format=text", "--format=json", "--format=github", "--format=sarif",
			"--baseline-write=" + reportPath + ".baseline",
		}, &stdout, &stderr)
		if code != exitClean && code != exitFindings {
			t.Fatalf("analyze over the generated module = %d, want a verdict about its findings\nstderr: %s",
				code, stderr.String())
		}

		after := hashTree(t, dir)
		if !maps.Equal(before, after) {
			t.Fatalf("the analysis changed the target tree:\n%s", treeDifference(before, after))
		}
	})
}

// hashTree is the SHA-256 of every file under dir, keyed by the path relative to it,
// which is both the file set and the contents in one value.
func hashTree(t *rapid.T, dir string) map[string]string {
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
		t.Fatalf("hash the tree at %s: %v", dir, err)
	}
	return hashes
}

// treeDifference names every file whose presence or contents moved, for a failure
// that has to say what the run touched.
func treeDifference(before, after map[string]string) string {
	held := ""
	for _, path := range slices.Sorted(maps.Keys(before)) {
		switch at, present := after[path]; {
		case !present:
			held += "removed: " + path + "\n"
		case at != before[path]:
			held += "rewritten: " + path + "\n"
		}
	}
	for _, path := range slices.Sorted(maps.Keys(after)) {
		if _, present := before[path]; !present {
			held += "added: " + path + "\n"
		}
	}
	return held
}

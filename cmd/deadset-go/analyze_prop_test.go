package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// drawTree draws one target tree: a main package whose entry point references some
// of its declarations and not others, an optional second package in a directory of
// its own, an optional file that is not Go source at all, and the repository
// configuration. Every path of the draw is a file of the tree the property hashes,
// and the declarations nothing references are what give the run findings to render.
//
// No file of the draw imports anything and no file of it is a _test.go file, which
// is what makes the draw cheap to analyze: measured under the race detector, one
// analysis of a module with neither costs 0.06 s, one of a module importing io and
// strings costs 0.69 s, and one of a module carrying any test file at all costs
// 1.07 s, because the toolchain's synthesized test main pulls in the testing
// package's whole dependency graph whatever the test file itself imports. What this
// property is about is the documents a run writes and where, so the type-level shape
// of the target is not its subject and the cheap draw is what lets an iteration draw
// a tree of its own.
func drawTree(t *rapid.T) map[string]string {
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
	}

	count := rapid.IntRange(1, 4).Draw(t, "the number of declarations")
	var body strings.Builder
	body.WriteString("// Command app is a generated target.\npackage main\n\nfunc main() {\n")
	var declared []string
	for i := range count {
		name := "d" + strconv.Itoa(i)
		declared = append(declared, name)
		if rapid.Bool().Draw(t, name+" is referenced by the entry point") {
			fmt.Fprintf(&body, "\t%s()\n", name)
		}
	}
	body.WriteString("}\n")
	for _, name := range declared {
		fmt.Fprintf(&body, "\n// %s is a generated declaration.\nfunc %s() {}\n", name, name)
	}
	files["app.go"] = body.String()

	if rapid.Bool().Draw(t, "a second package in a directory of its own") {
		files[filepath.Join("lib", "lib.go")] = "// Package lib is a generated package.\npackage lib\n\n" +
			"// Exported is what nothing references.\nfunc Exported() {}\n"
	}
	if rapid.Bool().Draw(t, "a file that is not Go source") {
		files[filepath.Join("docs", "notes.md")] = "# Notes\n\nA file of the tree that is no input of the analysis.\n"
	}
	// An ignore document is a file of the tree the run READS, which is the file a
	// write would be likeliest to land on. The entry binds where the draw left the
	// first declaration unreferenced and is stale where it did not, and the run
	// leaves the document alone either way.
	if rapid.Bool().Draw(t, "an ignore document the run reads") {
		files["deadset-ignore.json"] = `{
  "description": "One entry about the first generated declaration.",
  "ignore": [
    {
      "code": "DS1002",
      "symbol": "go://example.com/app#d0",
      "path": "app.go",
      "reason": "the deletion of the first generated declaration is scheduled"
    }
  ]
}
`
	}

	kind := rapid.SampledFrom([]string{"application", "library"}).Draw(t, "the declared target kind")
	files[repositoryDocument] = `{"target": {"kind": "` + kind + `"}}`
	return files
}

// drawInvocation draws the parts of an analyze invocation that decide which
// documents the run writes and where: the renderings asked for, whether a baseline
// is written, whether the exit code carries the verdict, and the severity that fails
// the run. --report and --baseline-write name paths under documents, which is a
// sibling of the target and inside the run directory, so no document the run writes
// is a file of the tree being hashed.
func drawInvocation(t *rapid.T, documents, name string) []string {
	formats := rapid.SliceOfNDistinct(
		rapid.SampledFrom([]string{"text", "json", "github", "sarif"}),
		1, 4, func(s string) string { return s },
	).Draw(t, "the renderings the invocation asks for")

	reportPath := filepath.Join(documents, name+".json")
	args := []string{"analyze", "--target=./" + name, "--report=" + reportPath}
	for _, one := range formats {
		args = append(args, "--format="+one)
	}
	if rapid.Bool().Draw(t, "a baseline is written") {
		args = append(args, "--baseline-write="+reportPath+".baseline")
	}
	if rapid.Bool().Draw(t, "the exit code carries the verdict") {
		args = append(args, "--exit-code="+exitCodeOff)
	}
	args = append(args, "--fail-on="+rapid.SampledFrom([]string{"allow", "warn", "deny"}).
		Draw(t, "the severity that fails the run"))
	return args
}

// TestAnalysisLeavesTheTreeByteIdentical is property dead-code-suite/P23: a run of
// the analysis changes no file of the target.
//
// Every file of the tree is hashed before the run and after it, and the file set is
// compared as well as the contents, so a file added, removed or rewritten fails the
// property. The documents the run writes go outside the target, so what the run
// produces is not the change being measured, and the invocation is drawn over every
// document the verb can write, because a write is the only thing that could touch the
// tree.
//
// No deletion candidate is deleted and no visibility is changed by construction: each
// would be a byte the hash reads.
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
		files := drawTree(t)
		dir, err := os.MkdirTemp(base, "module")
		if err != nil {
			t.Fatalf("create a directory for the generated module: %v", err)
		}
		if err := writeFiles(dir, files); err != nil {
			t.Fatalf("write the generated module: %v", err)
		}

		before := hashTree(t, dir)
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), drawInvocation(t, documents, filepath.Base(dir)), &stdout, &stderr)
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

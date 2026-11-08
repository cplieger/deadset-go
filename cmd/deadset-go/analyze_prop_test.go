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
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// treeShape is one target tree of the byte-identity property: a main package whose
// entry point references some of its declarations and not others, and the files a
// run reads beside it or must pass over.
type treeShape struct {
	declared, referenced int
	lib, docs, ignore    bool
	kind                 string
}

// treeShapes is every combination of the optional parts of a tree, a second package,
// a file that is not Go source, an ignore document and either target kind, with a
// count of declarations and of referenced ones that varies between them.
func treeShapes() []treeShape {
	var shapes []treeShape
	for i := range 16 {
		declared := 1 + i%4
		shapes = append(shapes, treeShape{
			declared: declared, referenced: i % (declared + 1),
			lib: i&1 != 0, docs: i&2 != 0, ignore: i&4 != 0,
			kind: []string{"application", "library"}[i>>3],
		})
	}
	return shapes
}

// files writes the shape's tree, every path a file the property hashes; the
// declarations nothing references give the run findings to render. No file imports
// anything and none is a _test.go file, whose synthesized test main would pull in the
// testing package's whole dependency graph: the property is about the documents a run
// writes, not the target's type-level shape.
func (s treeShape) files() map[string]string {
	files := map[string]string{"go.mod": "module example.com/app\n\ngo 1.27.1\n"}
	var body strings.Builder
	body.WriteString("// Command app is a generated target.\npackage main\n\nfunc main() {\n")
	for i := range s.referenced {
		fmt.Fprintf(&body, "\td%d()\n", i)
	}
	body.WriteString("}\n")
	for i := range s.declared {
		fmt.Fprintf(&body, "\n// d%d is a generated declaration.\nfunc d%d() {}\n", i, i)
	}
	files["app.go"] = body.String()
	if s.lib {
		files[filepath.Join("lib", "lib.go")] = "// Package lib is a generated package.\npackage lib\n\n" +
			"// Exported is what nothing references.\nfunc Exported() {}\n"
	}
	if s.docs {
		files[filepath.Join("docs", "notes.md")] = "# Notes\n\nA file of the tree that is no input of the analysis.\n"
	}
	// An ignore document is a file of the tree the run READS, which is the file a
	// write would be likeliest to land on. The entry binds where the first declaration
	// is unreferenced and is stale where it is referenced, and the run leaves the
	// document alone either way.
	if s.ignore {
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
	files[repositoryDocument] = `{"target": {"kind": "` + s.kind + `"}}`
	return files
}

// everyDocument is the analyze invocation of row i over the tree named name: every
// rendering and the baseline, so every document the verb writes is written, with
// --report and --baseline-write under documents, a sibling of the target inside the
// run directory, so no document the run writes is a file of the tree being hashed.
// Whether the exit code carries the verdict and the severity that fails the run vary
// between rows.
func everyDocument(i int, documents, name string) []string {
	reportPath := filepath.Join(documents, name+".json")
	args := []string{"analyze", "--target=./" + name, "--report=" + reportPath, "--baseline-write=" + reportPath + ".baseline"}
	for _, one := range []string{"text", "json", "github", "sarif"} {
		args = append(args, "--format="+one)
	}
	if i%2 == 1 {
		args = append(args, "--exit-code="+exitCodeOff)
	}
	return append(args, "--fail-on="+[]string{"allow", "warn", "deny"}[i%3])
}

// TestAnalysisLeavesTheTreeByteIdentical is property dead-code-suite/P23: a run of
// the analysis changes no file of the target. The tree's file set and contents are
// hashed before and after the run, so a file added, removed or rewritten fails it.
// The documents the run writes go outside the target, and each run writes every
// document the verb can write, over every combination of the parts a tree can hold,
// because a write is the only thing that could touch the tree; a deleted candidate or
// a changed visibility would be a byte the hash reads.
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

	for i, shape := range treeShapes() {
		t.Run(fmt.Sprintf("%+v", shape), func(t *testing.T) {
			dir, err := os.MkdirTemp(base, "module")
			if err != nil {
				t.Fatalf("create a directory for the module: %v", err)
			}
			if err := writeFiles(dir, shape.files()); err != nil {
				t.Fatalf("write the module: %v", err)
			}

			before := hashTree(t, dir)
			var stdout, stderr bytes.Buffer
			code := invoke(t.Context(), everyDocument(i, documents, filepath.Base(dir)), &stdout, &stderr)
			if code != vocab.ExitClean && code != vocab.ExitFindings {
				t.Fatalf("analyze over the module = %d, want a verdict about its findings\nstderr: %s", code, stderr.String())
			}

			after := hashTree(t, dir)
			if !maps.Equal(before, after) {
				t.Fatalf("the analysis changed the target tree:\n%s", treeDifference(before, after))
			}
		})
	}
}

// hashTree is the SHA-256 of every file under dir, keyed by the path relative to it,
// which is both the file set and the contents in one value.
func hashTree(t *testing.T, dir string) map[string]string {
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

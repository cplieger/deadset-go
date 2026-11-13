//go:build unix

package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// linkedTree is a tree of the in-tree link rows: its files, the links inside it to
// directories of it, and the files a tree spelling the same directories with no
// link replaces, so plain is the tree as written without any link.
type linkedTree struct {
	files map[string]string
	links map[string]string
	plain map[string]string
}

// written is the directory of the tree, through its links where linked and spelled
// without them otherwise.
func (tree linkedTree) written(t *testing.T, linked bool) string {
	t.Helper()
	files := maps.Clone(tree.files)
	if !linked {
		maps.Copy(files, tree.plain)
	}
	dir := writeModule(t, files)
	if linked {
		for _, link := range slices.Sorted(maps.Keys(tree.links)) {
			symlink(t, tree.links[link], filepath.Join(dir, filepath.FromSlash(link)))
		}
	}
	return dir
}

// linkedRun is what one analysis of a link row wrote: its exit code, its standard
// error, its findings as the report writes them, and each finding's code, path and
// symbol name.
type linkedRun struct {
	stderr, findings string
	found            []string
	code             int
}

// analyzedOver is binary's analysis of the tree at dir with args, within the
// per-row deadline of two minutes.
func analyzedOver(t *testing.T, binary, dir string, args ...string) linkedRun {
	t.Helper()
	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	got, stalled := invokedBinaryWithin(t, binary, 2*time.Minute, dir, append([]string{"analyze", "--target=.", "--report=" + reportPath}, args...)...)
	if stalled {
		t.Fatalf("%s analyze did not return within two minutes", binary)
	}
	run := linkedRun{code: got.code, stderr: got.stderr}
	text, err := os.ReadFile(reportPath)
	if err != nil {
		return run
	}
	var written struct {
		Findings json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(text, &written); err != nil {
		t.Fatalf("Setup: read the report of %s: %v", binary, err)
	}
	run.findings = string(written.Findings)
	for _, f := range envelopeAt(t, reportPath).Findings {
		run.found = append(run.found, string(f.Code)+" "+f.Position.Path+" "+f.Symbol.Name)
	}
	return run
}

// memberFiles is the target of the workspace rows: an application at the root
// whose go.work lists the module example.com/u at x/u through the link l to x, and
// whose main package calls Run, which calls add with a parameter it never reads.
// u holds the files of u's package.
func memberFiles(u map[string]string) map[string]string {
	files := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
		"go.work":          "go 1.27.1\n\nuse (\n\t.\n\t./l/u\n)\n",
		"x/u/go.mod":       "module example.com/u\n\ngo 1.27.1\n",
		"main.go":          "package main\n\nimport \"example.com/u\"\n\nfunc main() { u.Run() }\n",
	}
	for name, body := range u {
		files["x/u/"+name] = body
	}
	return files
}

// memberPlain is the workspace rows' go.work spelled with no link.
var memberPlain = map[string]string{"go.work": "go 1.27.1\n\nuse (\n\t.\n\t./x/u\n)\n"}

// runAndAdd is u's package: Run, which main calls, calling add, whose second
// parameter nothing reads.
const runAndAdd = "// Package u runs.\npackage u\n\n// Run runs.\nfunc Run() { println(add(1, 2)) }\n\nfunc add(a, unused int) int { return a }\n"

// A module, a replace target, a workspace member, a consumer or a generator's
// input spelled through a link inside the target is the directory the
// link leads to: the run reports over the tree what it reports over the tree
// spelled with no link, byte for byte, exit code and standard error included, and
// every position names the file where it resolves. Where [referenceBinary] names a
// binary of v2.0.0-dev.50, the tree spelled with no link reports as it reports.
func TestAnalyzeReportsATreeSpelledThroughALinkInsideItAsSpelledWithout(t *testing.T) {
	t.Parallel()
	library := libraryFiles("")
	for _, tc := range []struct {
		tree       linkedTree
		args       []string
		want       []string
		name, exit string
	}{
		{
			name: "a workspace member listed through a link",
			tree: linkedTree{files: memberFiles(map[string]string{"u.go": runAndAdd}), links: map[string]string{"l": "x"}, plain: memberPlain},
			want: []string{"DS1401 x/u/u.go unused"},
			exit: "clean",
		},
		{
			name: "a workspace member listed through a link, its package in two configurations' files",
			tree: linkedTree{files: memberFiles(map[string]string{
				"u_unix.go":    "//go:build unix\n\n" + runAndAdd,
				"u_windows.go": "//go:build windows\n\npackage u\n\n// Run runs.\nfunc Run() {}\n",
			}), links: map[string]string{"l": "x"}, plain: memberPlain},
			want: []string{"DS1401 x/u/u_unix.go unused"},
			exit: "clean",
		},
		{
			name: "a program of the target calling a workspace member listed through a link",
			tree: func() linkedTree {
				files := memberFiles(map[string]string{"u.go": runAndAdd + "\n// Gen is what the generator calls.\nfunc Gen() {}\n"})
				files["gen.go"] = "//go:build ignore\n\npackage main\n\nimport \"example.com/u\"\n\nfunc main() { u.Gen() }\n"
				return linkedTree{files: files, links: map[string]string{"l": "x"}, plain: memberPlain}
			}(),
			want: []string{"DS1401 x/u/u.go unused"},
			exit: "clean",
		},
		{
			name: "a replace target through a link",
			tree: func() linkedTree {
				files := memberFiles(map[string]string{"u.go": runAndAdd})
				delete(files, "go.work")
				files["go.mod"] = "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/u v0.0.0\n\nreplace example.com/u => ./l/u\n"
				return linkedTree{files: files, links: map[string]string{"l": "x"}, plain: map[string]string{
					"go.mod": "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/u v0.0.0\n\nreplace example.com/u => ./x/u\n",
				}}
			}(),
			want: []string{},
			exit: "clean",
		},
		{
			name: "a nested module replacing the target, a workspace member listed through a link",
			tree: func() linkedTree {
				files := maps.Clone(library)
				files["x/tools/go.mod"] = "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../\n"
				files["x/tools/main.go"] = "// Command tools references one exported declaration of the target.\npackage main\n\n" +
					"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n"
				files["go.work"] = "go 1.27.1\n\nuse (\n\t.\n\t./l/tools\n)\n"
				return linkedTree{files: files, links: map[string]string{"l": "x"}, plain: map[string]string{"go.work": "go 1.27.1\n\nuse (\n\t.\n\t./x/tools\n)\n"}}
			}(),
			exit: "findings",
		},
		{
			name: "a nested module replacing the target through a link to the target",
			tree: func() linkedTree {
				files := maps.Clone(library)
				files["x/tools/go.mod"] = "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../k\n"
				files["x/tools/main.go"] = "// Command tools references one exported declaration of the target.\npackage main\n\n" +
					"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n"
				return linkedTree{files: files, links: map[string]string{"k": "."}, plain: map[string]string{
					"x/tools/go.mod": "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../\n",
				}}
			}(),
			want: []string{"DS1001 lib.go Forgotten"},
			exit: "findings",
		},
		{
			name: "a generator's package argument through a link",
			tree: func() linkedTree {
				files := maps.Clone(library)
				files["gen.go"] = "package app\n\n//go:generate go run entgo.io/ent/cmd/ent generate ./l\n"
				files["schema/user.go"] = "// Package schema is what the generator reads.\npackage schema\n\n// User is a schema the generator reads.\ntype User struct{}\n"
				return linkedTree{files: files, links: map[string]string{"l": "schema"}, plain: map[string]string{
					"gen.go": "package app\n\n//go:generate go run entgo.io/ent/cmd/ent generate ./schema\n",
				}}
			}(),
			want: []string{"DS1001 lib.go Used", "DS1001 lib.go Forgotten"},
			exit: "findings",
		},
		{
			name: "a mockgen source named through a link",
			tree: func() linkedTree {
				files := maps.Clone(library)
				files["gen.go"] = "package app\n\n//go:generate mockgen -source=l/store.go -destination=mock.go\n"
				files["store.go"] = "package app\n\n// Store is what the generator mocks.\ntype Store interface {\n\t// Get gets.\n\tGet() string\n}\n"
				return linkedTree{files: files, links: map[string]string{"l": "."}, plain: map[string]string{
					"gen.go": "package app\n\n//go:generate mockgen -source=store.go -destination=mock.go\n",
				}}
			}(),
			want: []string{"DS1001 lib.go Used", "DS1001 lib.go Forgotten"},
			exit: "findings",
		},
		{
			name: "a declared consumer spelled through a link",
			tree: func() linkedTree {
				files := maps.Clone(library)
				files["x/c/go.mod"] = "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../\n"
				files["x/c/main.go"] = "// Command c references one exported declaration of the target.\npackage main\n\n" +
					"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n"
				files["scope.json"] = `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
					` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "l/c"}]}`
				return linkedTree{files: files, links: map[string]string{"l": "x"}, plain: map[string]string{
					"scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
						` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "x/c"}]}`,
				}}
			}(),
			args: []string{"--scope=scope.json", "--config=" + repositoryDocument},
			want: []string{"DS1001 lib.go Forgotten"},
			exit: "findings",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := analyzedOver(t, commandBinary(t), tc.tree.written(t, true), tc.args...)
			plain := analyzedOver(t, commandBinary(t), tc.tree.written(t, false), tc.args...)
			if got.code != plain.code || got.stderr != plain.stderr || got.findings != plain.findings {
				t.Errorf("analyze over the tree through the link = %d writing %q and the findings %s, want what it reports over the tree with no link, %d writing %q and %s",
					got.code, got.stderr, got.findings, plain.code, plain.stderr, plain.findings)
			}
			if code := contractExitCodes(t)[tc.exit]; got.code != code {
				t.Errorf("analyze over the tree through the link = %d, want %d\nstderr: %s", got.code, code, got.stderr)
			}
			if tc.want != nil && !slices.Equal(got.found, tc.want) {
				t.Errorf("analyze over the tree through the link reported %q, want %q", got.found, tc.want)
			}
			if reference := os.Getenv(referenceBinary); reference != "" {
				want := analyzedOver(t, reference, tc.tree.written(t, false), tc.args...)
				if plain.code != want.code || plain.stderr != want.stderr || plain.findings != want.findings {
					t.Errorf("analyze over the tree with no link = %d writing %q and the findings %s, want the reference's %d writing %q and %s",
						plain.code, plain.stderr, plain.findings, want.code, want.stderr, want.findings)
				}
			}
		})
	}
}

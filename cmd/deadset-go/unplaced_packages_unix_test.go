//go:build unix

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/report"
)

// unplacedApplication is the target of the left-out package rows: an application
// at app whose main package calls Used of the package l, which calls Helper of
// app's package core, which also declares Lonely, which nothing calls.
func unplacedApplication() map[string]string {
	return map[string]string{
		"app/go.mod":                "module example.com/app\n\ngo 1.27.1\n",
		"app/" + repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
		"app/main.go":               "package main\n\nimport \"example.com/app/l\"\n\nfunc main() { l.Used() }\n",
		"app/core/core.go":          coreFile("core"),
	}
}

// coreFile is the package name declaring Helper, which only l calls, and Lonely,
// which nothing calls.
func coreFile(name string) string {
	return "// Package " + name + " helps.\npackage " + name + "\n\n" +
		"// Helper is called only from package l.\nfunc Helper() int { return 1 }\n\n" +
		"// Lonely is called by nothing.\nfunc Lonely() int { return 2 }\n"
}

// packageL is the package l importing the package at core and calling its Helper.
func packageL(core string) string {
	return "// Package l runs.\npackage l\n\nimport \"" + core + "\"\n\n// Used runs.\nfunc Used() { println(" +
		filepath.Base(core) + ".Helper()) }\n"
}

// linkedProgram is the target of the linked-program rows with files added: an
// application at app whose main package calls Lonely of its package core.
func linkedProgram(files map[string]string) map[string]string {
	held := unplacedApplication()
	held["app/main.go"] = "package main\n\nimport \"example.com/app/core\"\n\nfunc main() { println(core.Lonely()) }\n"
	maps.Copy(held, files)
	return held
}

// toolCalling is a main package calling Helper of the package at core.
func toolCalling(core string) string {
	return "// Command tool runs.\npackage main\n\nimport \"" + core + "\"\n\nfunc main() { println(" + filepath.Base(core) + ".Helper()) }\n"
}

// unplacedRun is what one analysis of a left-out package row reported: its exit
// code and standard error, each finding's code, path, name and reachability class,
// and each note's kind, path and message.
type unplacedRun struct {
	stderr string
	found  []string
	notes  []report.Note
	code   int
}

// analyzedUnplaced is the command's analysis of the target at dir, run from there
// with args, within the per-row deadline of two minutes.
func analyzedUnplaced(t *testing.T, dir string, args ...string) unplacedRun {
	t.Helper()
	return analyzedUnplacedEnv(t, dir, nil, args...)
}

// analyzedUnplacedEnv is [analyzedUnplaced] with env added to the environment.
func analyzedUnplacedEnv(t *testing.T, dir string, env []string, args ...string) unplacedRun {
	t.Helper()
	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	got, stalled := invokedBinaryWithinEnv(t, commandBinary(t), 2*time.Minute, dir, env, append([]string{"analyze", "--target=.", "--report=" + reportPath}, args...)...)
	if stalled {
		t.Fatal("analyze did not return within two minutes")
	}
	run := unplacedRun{code: got.code, stderr: got.stderr}
	if _, err := os.Stat(reportPath); err != nil {
		return run
	}
	written := envelopeAt(t, reportPath)
	for _, f := range written.Findings {
		run.found = append(run.found, string(f.Code)+" "+f.Position.Path+" "+f.Symbol.Name+" "+string(f.ReachabilityClass))
	}
	slices.Sort(run.found)
	run.notes = written.Notes
	return run
}

// unplacedTree writes files below a directory whose name is longer than the link
// the row reaches it through, makes each of links there, and returns the target
// app spelled through that link, as macOS spells a temporary directory below /var.
func unplacedTree(t *testing.T, files, links map[string]string) string {
	t.Helper()
	base := sandbox.Dir(t)
	real := filepath.Join(base, "a-directory-longer-than-its-link")
	if err := sandbox.MkdirAll(real, 0o750); err != nil {
		t.Fatalf("Setup: create %s: %v", real, err)
	}
	if err := writeFiles(real, files); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, link := range slices.Sorted(maps.Keys(links)) {
		symlink(t, links[link], filepath.Join(real, filepath.FromSlash(link)))
	}
	spelled := filepath.Join(base, "t")
	symlink(t, real, spelled)
	if resolved, err := filepath.EvalSymlinks(spelled); err != nil || resolved == spelled {
		t.Fatalf("Setup: %s resolves to %q (%v), want the directory it links to", spelled, resolved, err)
	}
	return filepath.Join(spelled, "app")
}

// A package of a module the run loads that a build compiles and that is none of
// the packages the run reads for its module, one below a link the go command's
// ./... pattern does not follow, imported or not, or one in a directory the run
// leaves out, is left out, and so is a module whose build resolves the target that
// the run does not load: the report names its directory in an unread-entry note, a
// package or a module it imports is imported, every export it may use is possible
// and no result it may read is unused. Each row reaches its target through a link,
// as macOS spells a temporary directory.
func TestAnalyzeLeavesOutAPackageABuildCompilesOutsideItsModulesPackages(t *testing.T) {
	t.Parallel()
	possibleCore := []string{"DS1001 core/core.go Helper possible", "DS1001 core/core.go Lonely possible"}
	for _, tc := range []struct {
		files, links map[string]string
		name, at     string // the row's name and the path of the note naming the left-out package
		named        string // the package or directory the note's message names
		args         []string
		want         []string
	}{
		{
			name: "a link out of the root",
			files: func() map[string]string {
				files := unplacedApplication()
				files["shared/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			links: map[string]string{"app/l": "../shared"},
			at:    "l", named: "example.com/app/l",
			want: possibleCore,
		},
		{
			name: "a link into a directory the module file ignores",
			files: func() map[string]string {
				files := unplacedApplication()
				files["app/go.mod"] = "module example.com/app\n\ngo 1.27.1\n\nignore ./x\n"
				files["app/x/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			links: map[string]string{"app/l": "x"},
			at:    "l", named: "example.com/app/l",
			want: possibleCore,
		},
		{
			name: "a link out of the root to a package importing an internal package",
			files: func() map[string]string {
				files := unplacedApplication()
				delete(files, "app/core/core.go")
				files["app/internal/core/core.go"] = coreFile("core")
				files["app/sub/internal/z/z.go"] = "// Package z is one l may not import.\npackage z\n\n// Zed is called by nothing.\nfunc Zed() int { return 3 }\n"
				files["shared/s.go"] = packageL("example.com/app/internal/core")
				return files
			}(),
			links: map[string]string{"app/l": "../shared"},
			at:    "l", named: "example.com/app/l",
			want: []string{
				"DS1101 internal/core/core.go Helper possible", "DS1101 internal/core/core.go Lonely possible",
				"DS1101 sub/internal/z/z.go Zed certain", "DS1602 sub/internal/z/z.go z.go certain",
			},
		},
		{
			name: "a program of the target importing a package through a link out of the root",
			files: func() map[string]string {
				files := unplacedApplication()
				files["app/main.go"] = "package main\n\nfunc main() {}\n"
				files["app/gen.go"] = "//go:build ignore\n\npackage main\n\nimport \"example.com/app/l\"\n\nfunc main() { l.Used() }\n"
				files["shared/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			links: map[string]string{"app/l": "../shared"},
			at:    "l", named: "example.com/app/l",
			want: possibleCore,
		},
		{
			name: "a module below the target importing a package of the target through a link",
			files: func() map[string]string {
				files := unplacedApplication()
				files["app/main.go"] = "package main\n\nfunc main() {}\n"
				files["app/tools/go.mod"] = "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n"
				files["app/tools/main.go"] = "// Command tools runs l.\npackage main\n\nimport \"example.com/app/l\"\n\nfunc main() { l.Used() }\n"
				files["shared/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			links: map[string]string{"app/l": "../shared"},
			at:    "l", named: "example.com/app/l",
			want: possibleCore,
		},
		{
			name: "a module below the target importing its own package in a testdata directory",
			files: func() map[string]string {
				files := unplacedApplication()
				files["app/main.go"] = "package main\n\nfunc main() {}\n"
				files["app/tools/go.mod"] = "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n"
				files["app/tools/main.go"] = "// Command tools runs l.\npackage main\n\nimport \"example.com/tools/testdata/l\"\n\nfunc main() { l.Used() }\n"
				files["app/tools/testdata/l/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			at: "tools/testdata/l", named: "example.com/tools/testdata/l",
			want: possibleCore,
		},
		{
			name: "a declared consumer importing its own package in a testdata directory",
			files: map[string]string{
				"app/go.mod": "module example.com/app\n\ngo 1.27.1\n",
				"app/lib.go": "// Package app exports one declaration only the consumer's left-out package\n// calls and one nothing calls.\npackage app\n\n" +
					"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
					"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
				"app/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
				"app/scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
					` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "c"}]}`,
				"app/c/go.mod":          "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
				"app/c/main.go":         "// Command c runs h.\npackage main\n\nimport \"example.com/c/testdata/h\"\n\nfunc main() { h.Run() }\n",
				"app/c/testdata/h/h.go": "// Package h calls the target.\npackage h\n\nimport \"example.com/app\"\n\n// Run runs.\nfunc Run() { println(app.Used()) }\n",
			},
			args: []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:   "c/testdata/h", named: "example.com/c/testdata/h",
			want: []string{"DS1001 lib.go Forgotten possible", "DS1001 lib.go Used possible"},
		},
		{
			name:  "a program nothing imports through a link out of the root",
			files: linkedProgram(map[string]string{"shared/main.go": toolCalling("example.com/app/core")}),
			links: map[string]string{"app/tool": "../shared"},
			at:    "tool", named: "example.com/app/tool",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a program nothing imports through a link, of a package nothing read imports",
			files: func() map[string]string {
				files := linkedProgram(map[string]string{"shared/main.go": toolCalling("example.com/app/core")})
				files["app/main.go"] = "package main\n\nfunc main() {}\n"
				return files
			}(),
			links: map[string]string{"app/tool": "../shared"},
			at:    "tool", named: "example.com/app/tool",
			want: possibleCore,
		},
		{
			name: "a program nothing imports through a link into a directory the module file ignores",
			files: linkedProgram(map[string]string{
				"app/go.mod":    "module example.com/app\n\ngo 1.27.1\n\nignore ./x\n",
				"app/x/main.go": toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/tool": "x"},
			at:    "tool", named: "example.com/app/tool",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a package below a link below a link",
			files: linkedProgram(map[string]string{
				"shared/main.go": "// Command tool runs.\npackage main\n\nfunc main() {}\n",
				"inner/inner.go": packageL("example.com/app/core"),
			}),
			links: map[string]string{"app/tool": "../shared", "shared/inner": "../inner"},
			at:    ".", named: "example.com/app/tool/inner in ../shared/inner",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program through a link to the parent of the module",
			files: linkedProgram(map[string]string{"shared/main.go": toolCalling("example.com/app/core")}),
			links: map[string]string{"app/up": ".."},
			at:    "up", named: "below up are compiled by a build that names them and the run does not walk the link",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a program through a link to the parent of a directory a link already reaches",
			files: linkedProgram(map[string]string{
				"x/y/y.go":    "// Package y helps nothing.\npackage y\n",
				"x/z/main.go": toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/a": "../x/y", "app/b": "../x"},
			at:    ".", named: "example.com/app/b/z in ../x/z",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program through an underscore link out of the root",
			files: linkedProgram(map[string]string{"tool/main.go": toolCalling("example.com/app/core")}),
			links: map[string]string{"app/_tool": "../tool"},
			at:    "_tool", named: "example.com/app/_tool",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program in a testdata directory below a link out of the root",
			files: linkedProgram(map[string]string{"shared/testdata/p/main.go": toolCalling("example.com/app/core")}),
			links: map[string]string{"app/tool": "../shared"},
			at:    ".", named: "example.com/app/tool/testdata/p in ../shared/testdata/p",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a link whose packages do not load",
			files: func() map[string]string {
				files := linkedProgram(map[string]string{"shared/main.go": "// Command tool imports a package no module provides.\npackage main\n\nimport \"example.com/missing\"\n\nfunc main() { missing.Run() }\n"})
				files["app/main.go"] = "package main\n\nfunc main() {}\n"
				return files
			}(),
			links: map[string]string{"app/tool": "../shared"},
			at:    "tool", named: "below tool are compiled by a build that names them and the run does not count the Go files there that no load of the run compiles",
			want: possibleCore,
		},
		{
			name: "a result only a package through a link reads",
			files: func() map[string]string {
				files := unplacedApplication()
				files["app/main.go"] = "package main\n\nimport (\n\t\"example.com/app/core\"\n\t\"example.com/app/l\"\n)\n\nfunc main() {\n\tcore.Helper()\n\tl.Used()\n}\n"
				files["shared/s.go"] = packageL("example.com/app/core")
				return files
			}(),
			links: map[string]string{"app/l": "../shared"},
			at:    "l", named: "example.com/app/l",
			want: []string{"DS1001 core/core.go Lonely possible"},
		},
		{
			name: "a requirement only a program through a link imports",
			files: linkedProgram(map[string]string{
				"app/go.mod":     "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n",
				"dep/go.mod":     "module example.com/dep\n\ngo 1.27.1\n",
				"dep/dep.go":     "// Package dep is a dependency.\npackage dep\n\n// Value is one.\nfunc Value() int { return 1 }\n",
				"shared/main.go": "// Command tool prints a dependency's value.\npackage main\n\nimport \"example.com/dep\"\n\nfunc main() { println(dep.Value()) }\n",
			}),
			links: map[string]string{"app/tool": "../shared"},
			at:    "tool", named: "example.com/app/tool",
			want: []string{"DS1001 core/core.go Helper certain"},
		},
		{
			name:  "a program through a link out of the root in an underscore directory",
			files: linkedProgram(map[string]string{"tool/main.go": toolCalling("example.com/app/core"), "app/_ex/keep.txt": "kept\n"}),
			links: map[string]string{"app/_ex/u": "../../tool"},
			at:    "_ex/u", named: "example.com/app/_ex/u",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a program in an ignored directory below a link to a package directory",
			files: linkedProgram(map[string]string{
				"app/go.mod":      "module example.com/app\n\ngo 1.27.1\n\nignore ./y/x\n",
				"app/y/x/main.go": toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/m": "y"},
			at:    "y/x", named: "example.com/app/m/x in y/x",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program in a testdata directory below a link to a package directory",
			files: linkedProgram(map[string]string{"app/y/testdata/p/main.go": toolCalling("example.com/app/core")}),
			links: map[string]string{"app/m": "y"},
			at:    "y/testdata/p", named: "example.com/app/m/testdata/p in y/testdata/p",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a program through a link into a directory another module roots",
			files: linkedProgram(map[string]string{
				"app/sub/go.mod":      "module example.com/sub\n\ngo 1.27.1\n",
				"app/sub/pkg/main.go": toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/l": "sub/pkg"},
			at:    "l", named: "example.com/app/l",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program through a link into a module root below a directory a link reaches",
			files: linkedProgram(moduleBelowALink()),
			links: map[string]string{"app/b": "../x", "app/z": "../x/mod/pkg"},
			at:    "z", named: "example.com/app/z",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program through a link into a module root below a directory a link reaches, the links' names swapped",
			files: linkedProgram(moduleBelowALink()),
			links: map[string]string{"app/b": "../x/mod/pkg", "app/z": "../x"},
			at:    "b", named: "example.com/app/b",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a module through a link building the target under another module path",
			files: linkedProgram(map[string]string{
				"other/go.mod":  "module example.com/other\n\ngo 1.27.1\n\nrequire example.com/renamed v0.0.0\n\nreplace example.com/renamed => ../app\n",
				"other/main.go": toolCalling("example.com/renamed/core"),
			}),
			links: map[string]string{"app/other": "../other"},
			at:    "other", named: "the module at other builds the target under another module path",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a module through a link building the target through its workspace",
			files: linkedProgram(map[string]string{
				"other/go.mod":  "module example.com/other\n\ngo 1.27.1\n",
				"other/go.work": "go 1.27.1\n\nuse (\n\t.\n\t../app\n)\n",
				"other/main.go": toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/other": "../other"},
			at:    "other", named: "the module at other builds against the target",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a link of a module below the target to a program outside the root",
			files: linkedProgram(map[string]string{
				"app/tool/go.mod":  "module example.com/tool\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
				"app/tool/main.go": "// Command tool runs.\npackage main\n\nfunc main() {}\n",
				"out/main.go":      toolCalling("example.com/app/core"),
			}),
			links: map[string]string{"app/tool/l": "../../out"},
			at:    "tool/l", named: "example.com/tool/l",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "an underscore program of a module below the target",
			files: linkedProgram(map[string]string{
				"app/tool/go.mod":     "module example.com/tool\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
				"app/tool/main.go":    "// Command tool runs.\npackage main\n\nfunc main() {}\n",
				"app/tool/_x/main.go": toolCalling("example.com/app/core"),
			}),
			at: "tool/_x", named: "example.com/tool/_x",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program of the target in its own testdata directory",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": toolCalling("example.com/app/core")}),
			at:    "testdata/p", named: "example.com/app/testdata/p",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a program of the target in a directory its module file ignores",
			files: linkedProgram(map[string]string{
				"app/go.mod":       "module example.com/app\n\ngo 1.27.1\n\nignore ./skip\n",
				"app/skip/main.go": toolCalling("example.com/app/core"),
			}),
			at: "skip", named: "example.com/app/skip",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program of the target in a dot directory",
			files: linkedProgram(map[string]string{"app/.hid/main.go": toolCalling("example.com/app/core")}),
			at:    ".hid", named: "example.com/app/.hid",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a program of the target in a vendor directory below its root",
			files: linkedProgram(map[string]string{"app/x/vendor/q/main.go": toolCalling("example.com/app/core")}),
			at:    "x/vendor/q", named: "example.com/app/x/vendor/q",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name:  "a link of a declared consumer to a program outside the root",
			files: consumerOf(map[string]string{"out/main.go": libraryCalling("example.com/app")}),
			links: map[string]string{"app/c/l": "../../out"},
			args:  []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:    "c/l", named: "example.com/c/l",
			want: possibleLibrary,
		},
		{
			name:  "a link of a declared consumer to a package outside the root",
			files: consumerOf(map[string]string{"tool/t.go": "// Package tool calls the target.\npackage tool\n\nimport \"example.com/app\"\n\n// Run runs.\nfunc Run() { println(app.Used()) }\n"}),
			links: map[string]string{"app/c/tool": "../../tool"},
			args:  []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:    "c/tool", named: "example.com/c/tool",
			want: possibleLibrary,
		},
		{
			name:  "a program of a declared consumer in its testdata directory",
			files: consumerOf(map[string]string{"app/c/testdata/p/main.go": libraryCalling("example.com/app")}),
			args:  []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:    "c/testdata/p", named: "example.com/c/testdata/p",
			want: possibleLibrary,
		},
		{
			name: "a program of a declared consumer in a directory its module file ignores",
			files: consumerOf(map[string]string{
				"app/c/go.mod":       "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n\nignore ./skip\n",
				"app/c/skip/main.go": libraryCalling("example.com/app"),
			}),
			args: []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:   "c/skip", named: "example.com/c/skip",
			want: possibleLibrary,
		},
		{
			name: "a module below the target building the target under another module path",
			files: linkedProgram(map[string]string{
				"app/other/go.mod":  "module example.com/other\n\ngo 1.27.1\n\nrequire example.com/renamed v0.0.0\n\nreplace example.com/renamed => ../\n",
				"app/other/main.go": toolCalling("example.com/renamed/core"),
			}),
			at: "other", named: "the module at other builds the target under another module path",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := analyzedUnplaced(t, unplacedTree(t, tc.files, tc.links), tc.args...)
			if got.code != contractExitCodes(t)["findings"] {
				t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
			}
			if !slices.Equal(got.found, tc.want) {
				t.Errorf("analyze reported %q, want %q", got.found, tc.want)
			}
			noted := slices.IndexFunc(got.notes, func(n report.Note) bool {
				return n.Kind == report.NoteKindUnreadEntry && n.Path == tc.at && strings.Contains(n.Message, tc.named)
			})
			if noted < 0 || len(got.notes) != 1 {
				t.Errorf("analyze noted %+v, want one unread-entry note at %s naming %s", got.notes, tc.at, tc.named)
			}
		})
	}
}

// possibleLibrary is what the library of [consumerOf] reports where code the run
// does not read may call it.
var possibleLibrary = []string{"DS1001 lib.go Forgotten possible", "DS1001 lib.go Used possible"}

// consumerOf is a library at app exporting Used and Forgotten, which nothing
// read calls, and its declared consumer at app/c, whose main package calls
// nothing, with files added.
func consumerOf(files map[string]string) map[string]string {
	held := map[string]string{
		"app/go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app/lib.go": "// Package app exports two declarations nothing it reads calls.\npackage app\n\n" +
			"// Used is what a left-out package calls.\nfunc Used() string { return \"used\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		"app/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
		"app/scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
			` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "c"}]}`,
		"app/c/go.mod":  "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
		"app/c/main.go": "// Command c runs.\npackage main\n\nfunc main() {}\n",
	}
	maps.Copy(held, files)
	return held
}

// libraryCalling is a main package calling Used of the library at path.
func libraryCalling(path string) string {
	return "// Command p runs.\npackage main\n\nimport \"" + path + "\"\n\nfunc main() { println(app.Used()) }\n"
}

// moduleBelowALink is the module x/mod outside the target, whose package pkg is
// a program calling Helper of the target's core, beside x's own package.
func moduleBelowALink() map[string]string {
	return map[string]string{
		"x/x.go":            "// Package x helps nothing.\npackage x\n",
		"x/mod/go.mod":      "module example.com/mod\n\ngo 1.27.1\n",
		"x/mod/pkg/main.go": toolCalling("example.com/app/core"),
	}
}

// A module whose build resolves the target's directory through a workspace, one
// below the target with its own go.work or one the workspace listing the target
// lists beside it, imports the target without requiring it, and the run loads it
// as a consumer: what it calls is used, and nothing is noted.
func TestAnalyzeCountsAModuleThatBuildsTheTargetThroughAWorkspace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files map[string]string
		name  string
	}{
		{
			name: "a module below the target with a workspace of its own",
			files: map[string]string{
				"app/other/go.mod":  "module example.com/other\n\ngo 1.27.1\n",
				"app/other/go.work": "go 1.27.1\n\nuse (\n\t.\n\t..\n)\n",
				"app/other/main.go": toolCalling("example.com/app/core"),
			},
		},
		{
			name: "a module the workspace listing the target lists",
			files: map[string]string{
				"go.work":       "go 1.27.1\n\nuse (\n\t./app\n\t./other\n)\n",
				"other/go.mod":  "module example.com/other\n\ngo 1.27.1\n",
				"other/main.go": toolCalling("example.com/app/core"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := linkedProgram(tc.files)
			files["app/main.go"] = "package main\n\nimport _ \"example.com/app/core\"\n\nfunc main() {}\n"
			got := analyzedUnplaced(t, unplacedTree(t, files, nil))
			if got.code != contractExitCodes(t)["findings"] {
				t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
			}
			if want := []string{"DS1001 core/core.go Lonely certain"}; !slices.Equal(got.found, want) {
				t.Errorf("analyze reported %q, want %q", got.found, want)
			}
			if len(got.notes) != 0 {
				t.Errorf("analyze noted %+v, want no note", got.notes)
			}
		})
	}
}

// The go command builds no package in the vendor directory of a module's root,
// in any -mod mode, so a program there is none a build compiles: what only it
// calls stays certain, and nothing is noted.
func TestAnalyzeLeavesNothingOutOfTheModuleRootsVendorDirectory(t *testing.T) {
	t.Parallel()
	files := linkedProgram(map[string]string{"app/vendor/p/main.go": toolCalling("example.com/app/core")})
	got := analyzedUnplaced(t, unplacedTree(t, files, nil))
	if got.code != contractExitCodes(t)["findings"] {
		t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
	}
	if want := []string{"DS1001 core/core.go Helper certain"}; !slices.Equal(got.found, want) {
		t.Errorf("analyze reported %q, want %q", got.found, want)
	}
	if len(got.notes) != 0 {
		t.Errorf("analyze noted %+v, want no note", got.notes)
	}
}

// A consumer whose module file replaces the target through a link to the target's
// directory lists the target's packages under that link: those packages are the
// ones the run reads, never left out, so the report holds no note and a declaration
// the consumer leaves uncalled stays dead at certain.
func TestAnalyzeReadsTheTargetAConsumerReplacesThroughALinkAsTheTarget(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"app/go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app/lib.go": "// Package app exports one declaration the consumer calls and one nothing calls.\npackage app\n\n" +
			"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		"app/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
		"app/scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
			` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "c"}]}`,
		"app/c/go.mod":  "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../linked\n",
		"app/c/main.go": "// Command c calls the target.\npackage main\n\nimport \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n",
	}
	got := analyzedUnplaced(t, unplacedTree(t, files, map[string]string{"linked": "app"}), "--scope=scope.json", "--config="+repositoryDocument)
	if got.code != contractExitCodes(t)["findings"] {
		t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
	}
	if want := []string{"DS1001 lib.go Forgotten certain"}; !slices.Equal(got.found, want) {
		t.Errorf("analyze reported %q, want %q", got.found, want)
	}
	if len(got.notes) != 0 {
		t.Errorf("analyze noted %+v, want no note", got.notes)
	}
}

// A module whose build the run cannot count references of, because the go command
// cannot list its module graph or because it builds a unit under another module
// path, makes every export it may use possible, whichever load of the run it is:
// the target itself, a walked unit, a module below the target, a workspace reader,
// a declared consumer or a module root a link reaches. The report names it in an
// unread-entry note, and no declaration it may use is certain. Each row reaches its
// target through a link, as macOS spells a temporary directory.
func TestAnalyzeFailsSafeOnEveryModuleWhoseReferencesTheRunCannotCount(t *testing.T) {
	t.Parallel()
	const (
		newer   = "go 1.99\n" // a go directive above every toolchain, which the offline run cannot fetch
		helpers = "package core\n\n// Helper is called only by the module the run cannot count.\nfunc Helper() int { return 1 }\n"
		calling = "package main\n\nimport \"example.com/%s/core\"\n\nfunc main() { _ = core.Helper() }\n"
		blank   = "package main\n\nimport _ \"example.com/%s/core\"\n\nfunc main() {}\n"
	)
	module := func(path, directive, require string) string {
		held := "module " + path + "\n\n" + directive
		if require != "" {
			held += "\nrequire " + require + " v0.0.0\n"
		}
		return held
	}
	app := map[string]string{
		"app/go.mod":                module("example.com/app", "go 1.27.1\n", ""),
		"app/" + repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
		"app/main.go":               fmt.Sprintf(blank, "app"),
		"app/core/core.go":          helpers,
	}
	walked := map[string]string{
		"app/" + repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
		"app/a/go.mod":              module("example.com/a", "go 1.27.1\n", ""),
		"app/a/main.go":             fmt.Sprintf(blank, "a"),
		"app/a/core/core.go":        helpers,
	}
	with := func(base, files map[string]string) map[string]string {
		held := maps.Clone(base)
		maps.Copy(held, files)
		return held
	}
	for _, tc := range []struct {
		files, links map[string]string
		name, at     string // the row's name and the path of the note naming the module
		named        string // what that note's message says of it
		args, want   []string
	}{
		{
			name: "the target building itself under another module path",
			files: with(app, map[string]string{
				"app/go.mod":  module("example.com/app", "go 1.27.1\n", "example.com/renamed") + "\nreplace example.com/renamed => ./\n",
				"app/main.go": fmt.Sprintf(calling, "renamed"),
			}),
			at: ".", named: "the module at . builds the target under another module path",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a walked unit building another under another module path",
			files: with(walked, map[string]string{
				"app/b/go.mod":  module("example.com/b", "go 1.27.1\n", "example.com/renamed") + "\nreplace example.com/renamed => ../a\n",
				"app/b/main.go": fmt.Sprintf(calling, "renamed"),
			}),
			at: "b", named: "the module at b builds the target under another module path",
			want: []string{"DS1001 a/core/core.go Helper possible"},
		},
		{
			name: "a walked unit whose module graph the go command cannot list",
			files: with(walked, map[string]string{
				"app/b/go.mod":  module("example.com/b", newer, "example.com/a") + "\nreplace example.com/a => ../a\n",
				"app/b/main.go": fmt.Sprintf(calling, "a"),
			}),
			at: "b", named: "the module at b may build against the target",
			want: []string{"DS1001 a/core/core.go Helper possible"},
		},
		{
			name: "a module below the target whose module graph the go command cannot list",
			files: with(app, map[string]string{
				"app/tool/go.mod":  module("example.com/tool", newer, "example.com/app") + "\nreplace example.com/app => ../\n",
				"app/tool/main.go": fmt.Sprintf(calling, "app"),
			}),
			at: "tool", named: "the module at tool may build against the target",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a module root a link reaches whose module graph the go command cannot list",
			files: with(app, map[string]string{
				"tool/go.mod":  module("example.com/tool", newer, "example.com/app") + "\nreplace example.com/app => ../app\n",
				"tool/main.go": fmt.Sprintf(calling, "app"),
			}),
			links: map[string]string{"app/tool": "../tool"},
			at:    "tool", named: "the module at tool may build against the target",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a workspace reader building the target under another module path",
			files: with(app, map[string]string{
				"go.work":       "go 1.27.1\n\nuse (\n\t./app\n\t./other\n)\n",
				"other/go.mod":  module("example.com/other", "go 1.27.1\n", "example.com/renamed") + "\nreplace example.com/renamed => ../app\n",
				"other/main.go": fmt.Sprintf(calling, "renamed"),
			}),
			at: ".", named: "the module at ../other builds the target under another module path",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "a declared consumer building the target under another module path",
			files: with(app, map[string]string{
				"app/" + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
				"app/scope.json": `{"target": {"id": "example.com/app", "role": "target", "path": "."},` +
					` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "c"}]}`,
				"app/c/go.mod":  module("example.com/c", "go 1.27.1\n", "example.com/renamed") + "\nreplace example.com/renamed => ../\n",
				"app/c/main.go": fmt.Sprintf(calling, "renamed"),
			}),
			args: []string{"--scope=scope.json", "--config=" + repositoryDocument},
			at:   "c", named: "the module at c builds the target under another module path",
			want: []string{"DS1001 core/core.go Helper possible"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := analyzedUnplaced(t, unplacedTree(t, tc.files, tc.links), tc.args...)
			if got.code != contractExitCodes(t)["findings"] {
				t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
			}
			if !slices.Equal(got.found, tc.want) {
				t.Errorf("analyze reported %q, want %q", got.found, tc.want)
			}
			if !slices.ContainsFunc(got.notes, func(n report.Note) bool {
				return n.Kind == report.NoteKindUnreadEntry && n.Path == tc.at && strings.Contains(n.Message, tc.named)
			}) {
				t.Errorf("analyze noted %+v, want an unread-entry note at %s saying %s", got.notes, tc.at, tc.named)
			}
		})
	}
}

// Every directory the walk of a loaded module meets has one outcome: a directory
// holding a Go-named entry that is no regular file is listed where the
// configuration's build never opens that entry by its name, and left out unloaded
// where it does; a module root in a testdata or a dot directory is decided by
// what its build resolves. Each row is a program of the target's testdata or dot
// directory calling Helper, which nothing read calls, so Helper is possible, with
// one unread-entry note there. Each row reaches its target through a link.
func TestAnalyzeDecidesEveryDirectoryTheWalkOfALoadedModuleMeets(t *testing.T) {
	t.Parallel()
	program := toolCalling("example.com/app/core")
	nest := "module example.com/nest\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../\n"
	for _, tc := range []struct {
		files, links map[string]string
		fifos        []string // the paths below the fixture made named pipes
		name, at     string   // the row's name and the path of the note
		named        string   // what the note's message names
	}{
		{
			name:  "a pipe whose name only another platform's build opens",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": program}),
			fifos: []string{"app/testdata/p/x_windows.go"},
			at:    "testdata/p", named: "example.com/app/testdata/p",
		},
		{
			name:  "a link to nothing whose name only another platform's build opens",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": program}),
			links: map[string]string{"app/testdata/p/x_windows.go": "/nonexistent"},
			at:    "testdata/p", named: "example.com/app/testdata/p",
		},
		{
			name:  "a link to a device whose name only another platform's build opens",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": program}),
			links: map[string]string{"app/testdata/p/x_windows.go": "/dev/null"},
			at:    "testdata/p", named: "example.com/app/testdata/p",
		},
		{
			name:  "a link to a device and a link to nothing whose names only another platform's build opens",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": program}),
			links: map[string]string{"app/testdata/p/p_windows.go": "/dev/null", "app/testdata/p/stale_windows.go": "missing.go"},
			at:    "testdata/p", named: "example.com/app/testdata/p",
		},
		{
			name:  "a pipe whose name the build opens",
			files: linkedProgram(map[string]string{"app/testdata/p/main.go": program}),
			fifos: []string{"app/testdata/p/x.go"},
			at:    "testdata/p", named: "the run does not count the Go files there",
		},
		{
			name:  "a module in a testdata directory building the target",
			files: linkedProgram(map[string]string{"app/testdata/nest/go.mod": nest, "app/testdata/nest/main.go": program}),
			at:    "testdata/nest", named: "builds against the target and the run does not load it",
		},
		{
			name:  "a module in a dot directory building the target",
			files: linkedProgram(map[string]string{"app/.hid/nest/go.mod": nest, "app/.hid/nest/main.go": program}),
			at:    ".hid/nest", named: "builds against the target and the run does not load it",
		},
		{
			name:  "the vendor directory of the module root",
			files: linkedProgram(map[string]string{"app/vendor/main.go": program}),
			at:    "vendor", named: "example.com/app/vendor",
		},
		{
			name:  "the vendor directory of the module root beside an empty vendor list",
			files: linkedProgram(map[string]string{"app/vendor/main.go": program, "app/vendor/modules.txt": ""}),
			at:    "vendor", named: "example.com/app/vendor",
		},
		{
			name:  "a link named vendor out of the root",
			files: linkedProgram(map[string]string{"out/main.go": program}),
			links: map[string]string{"app/vendor": "../out"},
			at:    "vendor", named: "example.com/app/vendor",
		},
		{
			name:  "a link out of the root of another name",
			files: linkedProgram(map[string]string{"out/main.go": program}),
			links: map[string]string{"app/ext": "../out"},
			at:    "ext", named: "example.com/app/ext",
		},
		{
			name:  "a link below the vendor directory to a testdata program",
			files: linkedProgram(map[string]string{"app/w/testdata/p/main.go": program, "app/vendor/README": ""}),
			links: map[string]string{"app/vendor/l": "../w/testdata/p"},
			at:    "w/testdata/p", named: "example.com/app/w/testdata/p",
		},
		{
			name:  "a testdata program beside an empty vendor directory",
			files: linkedProgram(map[string]string{"app/w/testdata/p/main.go": program, "app/vendor/README": ""}),
			at:    "w/testdata/p", named: "example.com/app/w/testdata/p",
		},
		{
			name:  "a link to the vendor directory",
			files: linkedProgram(map[string]string{"app/vendor/example.com/v/main.go": program}),
			links: map[string]string{"app/x": "vendor"},
			at:    "vendor/example.com/v", named: "example.com/app/x/example.com/v",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := unplacedTree(t, tc.files, tc.links)
			real, err := filepath.EvalSymlinks(filepath.Dir(app))
			if err != nil {
				t.Fatalf("Setup: resolve %s: %v", app, err)
			}
			for _, fifo := range tc.fifos {
				sandbox.Fifo(t, filepath.Join(real, filepath.FromSlash(fifo)))
			}
			got := analyzedUnplaced(t, app)
			if got.code != contractExitCodes(t)["findings"] {
				t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
			}
			if want := []string{"DS1001 core/core.go Helper possible"}; !slices.Equal(got.found, want) {
				t.Errorf("analyze reported %q, want %q", got.found, want)
			}
			noted := slices.IndexFunc(got.notes, func(n report.Note) bool {
				return n.Kind == report.NoteKindUnreadEntry && n.Path == tc.at && strings.Contains(n.Message, tc.named)
			})
			if noted < 0 || len(got.notes) != 1 {
				t.Errorf("analyze noted %+v, want one unread-entry note at %s naming %s", got.notes, tc.at, tc.named)
			}
		})
	}
}

// A directory ./... does not name is read only where the run reads every Go file
// there a build may compile, so a file only another platform, tag or cgo setting
// compiles leaves it out with one unread-entry note, though the go command lists
// it ([program.4]): its call makes Helper possible. A package vendor/modules.txt
// lists builds under -mod=vendor as the dependency's, so Helper stays certain
// with no note. Each row reaches its target through a link.
func TestAnalyzeReadsADirectoryThePatternDoesNotNameOnlyByEveryGoFile(t *testing.T) {
	t.Parallel()
	user := "package p\n\nimport \"example.com/app/core\"\n\nfunc init() { println(core.Helper()) }\n"
	cgo := "package p\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n\nimport \"example.com/app/core\"\n\nfunc init() { println(core.Helper()) }\n"
	vendored := linkedProgram(map[string]string{
		"app/go.mod":                        "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/dep v1.0.0\n",
		"app/vendor/modules.txt":            "# example.com/dep v1.0.0\n## explicit; go 1.27\nexample.com/dep/p\n",
		"app/vendor/example.com/dep/p/p.go": user,
		"app/vendor/example.com/dep/go.mod": "module example.com/dep\n\ngo 1.27\n",
	})
	for _, tc := range []struct {
		files    map[string]string
		env      []string
		name, at string   // the row's name and the path of its one note, none where empty
		want     []string // the findings, Helper possible where nil
	}{
		{name: "a file only another platform's name builds", files: linkedProgram(map[string]string{"app/testdata/p/p_windows.go": user}), at: "testdata/p"},
		{name: "a file only another platform's constraint builds", files: linkedProgram(map[string]string{"app/testdata/p/p.go": "//go:build windows\n\n" + user}), at: "testdata/p"},
		{name: "a file only a cgo build compiles", files: linkedProgram(map[string]string{"app/testdata/p/p.go": cgo}), env: []string{"CGO_ENABLED=0"}, at: "testdata/p"},
		{name: "an underscore directory's file only another platform builds", files: linkedProgram(map[string]string{"app/_examples/demo/p_windows.go": user}), at: "_examples/demo"},
		{
			name:  "a host file beside one only another platform builds",
			files: linkedProgram(map[string]string{"app/testdata/p/h.go": "package p\n", "app/testdata/p/p_windows.go": user}), at: "testdata/p",
		},
		{name: "a vendored package of a dependency", files: vendored, want: []string{"DS1001 core/core.go Helper certain", "DS1701 go.mod example.com/dep certain"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := analyzedUnplacedEnv(t, unplacedTree(t, tc.files, nil), tc.env)
			if got.code != contractExitCodes(t)["findings"] {
				t.Fatalf("analyze = %d, want findings\nstderr: %s", got.code, got.stderr)
			}
			want := tc.want
			if want == nil {
				want = []string{"DS1001 core/core.go Helper possible"}
			}
			if !slices.Equal(got.found, want) {
				t.Errorf("analyze reported %q, want %q", got.found, want)
			}
			noted := slices.IndexFunc(got.notes, func(n report.Note) bool {
				return n.Kind == report.NoteKindUnreadEntry && n.Path == tc.at && strings.Contains(n.Message, "the run does not count the Go files there")
			})
			if tc.at == "" && len(got.notes) > 0 || tc.at != "" && (noted < 0 || len(got.notes) != 1) {
				t.Errorf("analyze noted %+v, want one unread-entry note at %q, or none where empty", got.notes, tc.at)
			}
		})
	}
}

// configurationTree is an application at app whose main package calls lib.F and
// other.Used, so lib.G and other.Dead are called by nothing the host build reads,
// with files added.
func configurationTree(files map[string]string) map[string]string {
	held := map[string]string{
		"app/go.mod":                "module example.com/app\n\ngo 1.27.1\n",
		"app/" + repositoryDocument: `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`,
		"app/main.go":               "package main\n\nimport (\n\t\"example.com/app/lib\"\n\t\"example.com/app/other\"\n)\n\nfunc main() { lib.F(); other.Used() }\n",
		"app/lib/lib.go":            "// Package lib serves.\npackage lib\n\n// F is called.\nfunc F() {}\n\n// G is called where another platform builds.\nfunc G() {}\n",
		"app/other/other.go":        "// Package other serves.\npackage other\n\n// Used is called.\nfunc Used() {}\n\n// Dead is called by nothing.\nfunc Dead() {}\n",
	}
	maps.Copy(held, files)
	return held
}

// The run reads a directory by one read set, every Go file any load of any
// configuration compiled ([program.4]). A file of an imported package no
// configuration compiles, a conjunction the matrix does not derive or one
// importing "C" with cgo off, leaves its directory out: Helper possible, one
// note. A directory the windows configuration reads whole is read for the host
// configuration too: one note, from the package windows lists, and Dead and the
// requirement findings stay certain; a package one configuration counts is left
// out only where another compiles a file of it no load counts ([countedPairs]).
func TestAnalyzeReadsADirectoryByEveryConfigurationOfTheRun(t *testing.T) {
	t.Parallel()
	user := "package p\n\nimport \"example.com/app/core\"\n\nfunc init() { println(core.Helper()) }\n"
	cgo := "package p\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n\nimport \"example.com/app/core\"\n\nfunc init() { println(core.Helper()) }\n"
	imported := func(name, content string) map[string]string {
		return linkedProgram(map[string]string{
			"app/main.go":            "package main\n\nimport (\n\t\"example.com/app/core\"\n\t\"example.com/app/testdata/p\"\n)\n\nfunc main() { println(core.Lonely(), p.H()) }\n",
			"app/testdata/p/h.go":    "package p\n\n// H is called.\nfunc H() int { return 1 }\n",
			"app/testdata/p/" + name: content,
		})
	}
	calling := func(name string) string {
		return "package " + name + "\n\nimport \"example.com/app/lib\"\n\nfunc init() { lib.G() }\n"
	}
	windows := map[string]string{"app/main_windows.go": "package main\n\nfunc init() {}\n"}
	nested := map[string]string{
		"app/examples/go.mod":                "module example.com/app/examples\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
		"app/examples/main.go":               "package main\n\nimport \"example.com/app/lib\"\n\nfunc main() { lib.F() }\n",
		"app/examples/winsvc/svc_windows.go": calling("winsvc"),
	}
	dep := map[string]string{
		"app/go.mod": "module example.com/app\n\ngo 1.27.1\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n",
		"dep/go.mod": "module example.com/dep\n\ngo 1.27.1\n",
		"dep/d.go":   "// Package dep serves.\npackage dep\n\n// D serves.\nfunc D() {}\n",
	}
	unloaded, listed := "the run does not count the Go files there", "package example.com/app/testdata/p "
	counted := countedPairs()
	for _, tc := range append([]configurationRow{
		{
			name: "an imported package's file only an underived conjunction builds", files: imported("z.go", "//go:build windows && arm64\n\n"+user),
			at: "testdata/p", named: unloaded, want: []string{"DS1001 core/core.go Helper possible"},
		},
		{
			name: "an imported package's file only a cgo build compiles", files: imported("c.go", cgo), env: []string{"CGO_ENABLED=0"},
			at: "testdata/p", named: unloaded, want: []string{"DS1001 core/core.go Helper possible"},
		},
		{name: "an imported package's file the windows configuration compiles", files: imported("p_windows.go", user)},
		{
			name: "a directory only the windows configuration lists", files: configurationTree(merged(windows, map[string]string{"app/testdata/p/p_windows.go": calling("p")})),
			at: "testdata/p", named: listed, want: []string{"DS1001 lib/lib.go G possible", "DS1001 other/other.go Dead certain"},
		},
		{
			name: "a directory only the windows configuration lists, beside a required module", files: configurationTree(merged(windows, dep, map[string]string{"app/testdata/p/p_windows.go": calling("p")})),
			at: "testdata/p", named: listed,
			want: []string{"DS1001 lib/lib.go G possible", "DS1001 other/other.go Dead certain", "DS1701 go.mod example.com/dep certain", "DS1702 go.mod example.com/dep certain"},
		},
		{
			name: "a directory no configuration of the matrix lists", files: configurationTree(map[string]string{"app/testdata/p/p_windows.go": calling("p")}),
			at: "testdata/p", named: unloaded, want: []string{"DS1001 lib/lib.go G possible", "DS1001 other/other.go Dead possible"},
		},
		{name: "a nested module's directory the windows configuration lists", files: configurationTree(merged(windows, nested)), want: []string{"DS1001 other/other.go Dead certain"}},
		{
			name: "a nested module's directory no configuration of the matrix lists", files: configurationTree(nested),
			at: "examples/winsvc", named: unloaded, want: []string{"DS1001 lib/lib.go G possible", "DS1001 other/other.go Dead possible"},
		},
	}, counted...) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := analyzedUnplacedEnv(t, unplacedTree(t, tc.files, nil), tc.env)
			code := contractExitCodes(t)["findings"]
			if len(tc.want) == 0 {
				code = contractExitCodes(t)["clean"]
			}
			if got.code != code {
				t.Fatalf("analyze = %d, want %d\nstderr: %s", got.code, code, got.stderr)
			}
			if !slices.Equal(got.found, tc.want) {
				t.Errorf("analyze reported %q, want %q", got.found, tc.want)
			}
			noted := slices.IndexFunc(got.notes, func(n report.Note) bool {
				return n.Kind == report.NoteKindUnreadEntry && n.Path == tc.at && strings.Contains(n.Message, tc.named)
			})
			if tc.at == "" && len(got.notes) > 0 || tc.at != "" && (noted < 0 || len(got.notes) != 1) {
				t.Errorf("analyze noted %+v, want one unread-entry note at %q saying %q, or none where empty", got.notes, tc.at, tc.named)
			}
		})
	}
}

// configurationRow is one row of TestAnalyzeReadsADirectoryByEveryConfigurationOfTheRun.
type configurationRow struct {
	files    map[string]string
	env      []string
	name, at string   // the row's name and the path of its one note, none where empty
	named    string   // what the note's message says
	want     []string // the findings
}

// countedPairs are rows of a package p only a module's windows file imports, its
// p.go calling core.Helper, each beside a twin whose host-only p_host.go calls
// core.Lonely. Where windows counts p, the target's or a unit's import, the host
// configuration leaves out nothing and Lonely stays certain, a windows-only file
// of p or core changing nothing; the twin's host file no load counts, so p is left
// out, one note. No load counts a nested module's own import ([program.4]): both
// its rows leave p out.
func countedPairs() []configurationRow {
	core := "// Package core helps.\npackage core\n\n// Used is called.\nfunc Used() int { return 0 }\n\n" +
		"// Helper is called only from p.\nfunc Helper() int { return 1 }\n\n// Lonely is called by nothing.\nfunc Lonely() int { return 2 }\n"
	document := `{"target": {"kind": "application"}, "analysis": {"min_confidence": "possible"}}`
	module := func(dir, path, coreAt, needs string) map[string]string {
		mod := "module " + path + "\n\ngo 1.27.1\n"
		if needs != "" {
			mod += "\nrequire " + needs + " v0.0.0\n\nreplace " + needs + " => " + strings.Repeat("../", strings.Count(dir, "/")) + "\n"
		}
		return map[string]string{
			dir + "/go.mod":          mod,
			dir + "/main.go":         "package main\n\nimport \"" + coreAt + "\"\n\nfunc main() { println(core.Used()); run() }\n",
			dir + "/main_other.go":   "//go:build !windows\n\npackage main\n\nfunc run() {}\n",
			dir + "/main_windows.go": "package main\n\nimport \"" + path + "/testdata/p\"\n\nfunc run() { p.Used() }\n",
		}
	}
	user := func(dir, coreAt string) map[string]string {
		return map[string]string{dir + "/testdata/p/p.go": "// Package p runs.\npackage p\n\nimport \"" + coreAt + "\"\n\n// Used runs.\nfunc Used() { println(core.Helper()) }\n"}
	}
	host := func(dir, coreAt string) map[string]string {
		return map[string]string{dir + "/testdata/p/p_host.go": "//go:build !windows\n\npackage p\n\nimport \"" + coreAt + "\"\n\nfunc init() { println(core.Lonely()) }\n"}
	}
	plain := merged(module("app", "example.com/app", "example.com/app/core", ""), user("app", "example.com/app/core"), map[string]string{"app/" + repositoryDocument: document, "app/core/core.go": core})
	units := merged(module("app/svc", "example.com/svc", "example.com/lib/core", ""), user("app/svc", "example.com/lib/core"), map[string]string{
		"app/" + repositoryDocument: document, "app/lib/go.mod": "module example.com/lib\n\ngo 1.27.1\n", "app/lib/core/core.go": core,
	})
	units["app/svc/go.mod"] = "module example.com/svc\n\ngo 1.27.1\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n"
	nested := merged(module("app/examples", "example.com/app/examples", "example.com/app/core", "example.com/app"), user("app/examples", "example.com/app/core"), map[string]string{
		"app/" + repositoryDocument: document, "app/go.mod": "module example.com/app\n\ngo 1.27.1\n", "app/core/core.go": core,
		"app/main.go": "package main\n\nfunc main() {}\n", "app/main_windows.go": "package main\n\nfunc init() {}\n",
	})
	return []configurationRow{
		{name: "a package only the target's windows file imports", files: plain, want: []string{"DS1001 core/core.go Lonely certain"}},
		{
			name: "a package only the target's windows file imports, beside a file only windows compiles of core", want: []string{"DS1001 core/core.go Lonely certain"},
			files: merged(plain, map[string]string{
				"app/core/w_windows.go": "package core\n\n// W is called by the windows build.\nfunc W() int { return 3 }\n",
				"app/main_windows.go":   "package main\n\nimport (\n\t\"example.com/app/core\"\n\t\"example.com/app/testdata/p\"\n)\n\nfunc run() { p.Used(); println(core.W()) }\n",
			}),
		},
		{
			name: "a package only the target's windows file imports, through a helper of core each platform builds from its own file", want: []string{"DS1001 core/core.go Lonely certain"},
			files: merged(plain, map[string]string{
				"app/core/core.go":        strings.Replace(core, "func Helper() int { return 1 }", "func Helper() int { return sep() }", 1),
				"app/core/sep_other.go":   "//go:build !windows\n\npackage core\n\nfunc sep() int { return 2 }\n",
				"app/core/sep_windows.go": "package core\n\nfunc sep() int { return 1 }\n",
			}),
		},
		{
			name: "a package only the target's windows file imports, beside a file only windows compiles of it", want: []string{"DS1001 core/core.go Lonely certain"},
			files: merged(plain, map[string]string{"app/testdata/p/p_windows.go": "package p\n\nfunc init() {}\n"}),
		},
		{
			name: "a package only the target's windows file imports, with a host file", files: merged(plain, host("app", "example.com/app/core")),
			at: "testdata/p", named: "package example.com/app/testdata/p ", want: []string{"DS1001 core/core.go Lonely possible"},
		},
		{name: "a package only a unit's windows file imports", files: units, want: []string{"DS1001 lib/core/core.go Lonely certain"}},
		{
			name: "a package only a unit's windows file imports, with a host file", files: merged(units, host("app/svc", "example.com/lib/core")),
			at: "svc/testdata/p", named: "package example.com/svc/testdata/p ", want: []string{"DS1001 lib/core/core.go Lonely possible"},
		},
		{
			name: "a package only a nested module's windows file imports", files: nested,
			at: "examples/testdata/p", named: "package example.com/app/examples/testdata/p ", want: []string{"DS1001 core/core.go Helper possible", "DS1001 core/core.go Lonely possible"},
		},
		{
			name: "a package only a nested module's windows file imports, with a host file", files: merged(nested, host("app/examples", "example.com/app/core")),
			at: "examples/testdata/p", named: "package example.com/app/examples/testdata/p ", want: []string{"DS1001 core/core.go Helper possible", "DS1001 core/core.go Lonely possible"},
		},
	}
}

// merged is every file of each of trees, a later one's winning.
func merged(trees ...map[string]string) map[string]string {
	held := make(map[string]string)
	for _, tree := range trees {
		maps.Copy(held, tree)
	}
	return held
}

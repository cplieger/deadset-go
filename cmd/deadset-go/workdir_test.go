package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// TestAnInvocationReadsItsPathsFromItsDirectoryAsAProcessThere holds invoke to its
// directory: from an empty working directory, every verb that reads a target, its
// paths relative to the target's directory, answers and writes as the built command
// run there, byte for byte; print-config's paths, spelled joined to the directory in
// process, are compared as the paths they name. It owns the working directory, so
// it does not run in parallel.
func TestAnInvocationReadsItsPathsFromItsDirectoryAsAProcessThere(t *testing.T) {
	base := sandbox.Dir(t)
	determinismModule(t, base)
	if err := sandbox.WriteFile(filepath.Join(base, templateName), []byte(templateDocument), 0o600); err != nil {
		t.Fatalf("Setup: write the template: %v", err)
	}
	if err := sandbox.WriteFile(filepath.Join(base, "central.json"), []byte(`{"reporters": {"sort": "position"}}`), 0o600); err != nil {
		t.Fatalf("Setup: write the central configuration: %v", err)
	}
	documents := []string{"--target=./app", "--config=app/" + repositoryDocument, "--central=central.json"}
	analyze := append([]string{"analyze"}, documents...)
	analyze = append(analyze, "--report=report.json", "--facts=facts.json", "--template="+templateName, "--baseline-write=baseline.json")
	for _, format := range slices.Sorted(maps.Keys(renderings)) {
		analyze = append(analyze, "--format="+string(format))
	}
	invocations := [][]string{
		analyze,
		append([]string{"print-roots"}, documents...),
		append([]string{"print-retained"}, documents...),
		append(append([]string{"explain"}, documents...), "go://example.com/app#Widened"),
		append([]string{"print-config"}, documents...),
	}
	before := filesUnder(t, base)
	processes := make([]invoked, len(invocations))
	processWrote := make([]map[string][]byte, len(invocations))
	for i, args := range invocations {
		processes[i] = processIn(t, base, nil, args...)
		if processes[i].code == vocab.ExitUsage || processes[i].code == vocab.ExitFailure {
			t.Fatalf("Setup: %q run from %s = %d, want an answer: %s", args, base, processes[i].code, processes[i].stderr)
		}
		processWrote[i] = writtenSince(t, base, before)
	}

	t.Chdir(sandbox.Dir(t))
	for i, args := range invocations {
		var stdout, stderr bytes.Buffer
		code := invoke(t.Context(), base, args, &stdout, &stderr)
		inProcessWrote := writtenSince(t, base, before)
		printed, want := stdout.String(), processes[i]
		same := printed == want.stdout
		if args[0] == "print-config" {
			same = samePrintedConfiguration(t, base, printed, want.stdout)
		}
		if code != want.code || !same || stderr.String() != want.stderr {
			t.Errorf("invoke(%s, %q) = %d writing %q and %q, want the process's %d writing %q and %q",
				base, args, code, printed, stderr.String(), want.code, want.stdout, want.stderr)
		}
		for _, name := range slices.Sorted(maps.Keys(processWrote[i])) {
			if !bytes.Equal(inProcessWrote[name], processWrote[i][name]) {
				t.Errorf("%s in process wrote %s as\n%s\nwant the process's\n%s", args[0], name, inProcessWrote[name], processWrote[i][name])
			}
		}
		if extra := slices.DeleteFunc(slices.Collect(maps.Keys(inProcessWrote)), func(name string) bool { _, held := processWrote[i][name]; return held }); len(extra) > 0 {
			t.Errorf("%s in process wrote %v, which the process did not", args[0], extra)
		}
	}
	if left := filesUnder(t, "."); len(left) > 0 {
		t.Errorf("invoke wrote %v in the process's working directory, want nothing there", slices.Sorted(maps.Keys(left)))
	}
}

// samePrintedConfiguration reports whether print-config's document printed in
// process names the configuration the process run in base printed: the same
// once both are decoded, the paths in process joined to base.
func samePrintedConfiguration(t *testing.T, base, inProcess, process string) bool {
	t.Helper()

	var got, want any
	if err := json.Unmarshal([]byte(inProcess), &got); err != nil {
		t.Errorf("print-config in process printed %q, not a JSON document: %v", inProcess, err)
		return false
	}
	if err := json.Unmarshal([]byte(process), &want); err != nil {
		t.Errorf("print-config as a process printed %q, not a JSON document: %v", process, err)
		return false
	}
	return sameDecoded(base, got, want)
}

// sameDecoded reports whether got, decoded from the document printed in process,
// equals want, decoded from the process's, where a string may differ only by
// naming, joined to base, the path want names relative to it, after the same
// "source: " prefix.
func sameDecoded(base string, got, want any) bool {
	switch want := want.(type) {
	case map[string]any:
		got, held := got.(map[string]any)
		if !held || len(got) != len(want) {
			return false
		}
		for key, value := range want {
			if inProcess, held := got[key]; !held || !sameDecoded(base, inProcess, value) {
				return false
			}
		}
		return true
	case []any:
		got, held := got.([]any)
		if !held || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !sameDecoded(base, got[i], want[i]) {
				return false
			}
		}
		return true
	case string:
		got, held := got.(string)
		return held && (got == want || sameJoinedPath(base, got, want))
	default:
		return got == want
	}
}

// sameJoinedPath reports whether got names, joined to base, the relative path
// want names, both after the same "source: " prefix.
func sameJoinedPath(base, got, want string) bool {
	gotSource, gotPath, gotSourced := strings.Cut(got, ": ")
	wantSource, wantPath, wantSourced := strings.Cut(want, ": ")
	if gotSourced != wantSourced || gotSource != wantSource {
		return false
	}
	if !gotSourced {
		gotPath, wantPath = got, want
	}
	if filepath.IsAbs(wantPath) || !filepath.IsAbs(gotPath) {
		return false
	}
	rel, err := filepath.Rel(base, gotPath)
	return err == nil && rel == filepath.Clean(filepath.FromSlash(wantPath))
}

// A path JSON escapes, as every Windows path is, still compares as the path it names.
func TestPrintConfigsDocumentsCompareByThePathsTheyName(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), `run\001`)
	document := func(central, repository string) string {
		body, err := json.Marshal(map[string]any{"provenance": map[string]string{
			"reporters.sort": "central: " + central, "target.kind": "repository: " + repository, "severity": "default",
		}})
		if err != nil {
			t.Fatalf("Setup: encode the document: %v", err)
		}
		return string(body)
	}
	joined := func(name string) string { return base + string(filepath.Separator) + name }
	process := document("central.json", "app/deadset.json")
	tests := []struct {
		name, inProcess string
		want            bool
	}{
		{"joined to the directory", document(joined("central.json"), joined("app/deadset.json")), true},
		{"another file", document(joined("other.json"), joined("app/deadset.json")), false},
		{"another source", strings.Replace(document(joined("central.json"), joined("app/deadset.json")), "central: ", "flag: ", 1), false},
		{"outside the directory", document(filepath.Join(filepath.Dir(base), "central.json"), joined("app/deadset.json")), false},
		{"the relative path unjoined", document("central.json", "app/deadset.json"), true},
		{"another setting", strings.Replace(document(joined("central.json"), joined("app/deadset.json")), `"default"`, `"deny"`, 1), false},
	}
	for _, tt := range tests {
		if got := samePrintedConfiguration(t, base, tt.inProcess, process); got != tt.want {
			t.Errorf("%s: samePrintedConfiguration(%s, %s, %s) = %t, want %t", tt.name, base, tt.inProcess, process, got, tt.want)
		}
	}
}

// filesUnder is every regular file below dir, by its slash path relative to dir.
func filesUnder(t *testing.T, dir string) map[string][]byte {
	t.Helper()

	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = body
		return err
	})
	if err != nil {
		t.Fatalf("Setup: read the files under %s: %v", dir, err)
	}
	return files
}

// writtenSince is every file below dir that is not in before, removed once read,
// so the next run writes into the directory as the first found it.
func writtenSince(t *testing.T, dir string, before map[string][]byte) map[string][]byte {
	t.Helper()

	written := filesUnder(t, dir)
	for name := range before {
		delete(written, name)
	}
	for name := range written {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("Setup: remove %s: %v", name, err)
		}
	}
	return written
}

func TestUnderReadsAPathAsAProcessInTheDirectoryOpensIt(t *testing.T) {
	t.Parallel()

	tests := []struct{ dir, path, want string }{
		{"/run", "", ""},
		{"", "a", "a"},
		{"/run", "a", "/run/a"},
		{"/run", ".", "/run/."},
		{"/run", "a/../b", "/run/a/../b"},
		{"/run", "/abs/a", "/abs/a"},
		{"/", "a", "/a"},
	}
	if runtime.GOOS == "windows" {
		tests = []struct{ dir, path, want string }{
			{`C:\run`, "", ""},
			{`C:\run`, `a`, `C:\run\a`},
			{`C:\run`, `a\..\b`, `C:\run\a\..\b`},
			{`C:\run`, `D:\abs`, `D:\abs`},
			{`C:\run`, `\rooted`, `C:\rooted`},
			{`C:\run`, `D:drive-relative`, `D:drive-relative`},
			{`C:\run`, `C:drive-relative`, `C:\run\drive-relative`},
			{`C:\run`, `c:drive-relative`, `C:\run\drive-relative`},
			{`C:\run`, `C:`, `C:\run`},
			{`C:\`, `a`, `C:\a`},
		}
	}
	for _, tc := range tests {
		if got := under(tc.dir, tc.path); got != tc.want {
			t.Errorf("under(%q, %q) = %q, want %q", tc.dir, tc.path, got, tc.want)
		}
	}
}

// TestEveryPathOptionOfEveryVerbIsReadFromTheInvocationsDirectory holds parse to
// cli.json: of every verb this analyzer serves, every option whose value is a path
// is read from the invocation's directory, and no other option is changed.
func TestEveryPathOptionOfEveryVerbIsReadFromTheInvocationsDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(sandbox.Dir(t), "run")
	for _, served := range vocab.AnalyzerVerbs(language) {
		flags := configuredFlagSet(served.Name, "", dir, io.Discard)
		var args []string
		for _, name := range served.Options {
			if flags.set.Lookup(name) == nil {
				flags.set.String(name, "", "")
			}
			args = append(args, "--"+name+"=rel/"+name)
		}
		if err := flags.parse(args); err != nil {
			t.Fatalf("parse(%s %q) = %v", served.Name, args, err)
		}
		for _, name := range served.Options {
			option, _ := vocab.OptionOf(name)
			want := "rel/" + name
			if option.Value == vocab.OptionValuePath {
				want = under(dir, want)
			}
			if got := flags.set.Lookup(name).Value.String(); got != want {
				t.Errorf("%s --%s=rel/%s parsed as %q, want %q", served.Name, name, name, got, want)
			}
		}
		if got := flags.set.Lookup("target").Value.String(); !slices.Contains(served.Options, "target") && got != under(dir, ".") {
			t.Errorf("%s's default target parsed as %q, want %q", served.Name, got, under(dir, "."))
		}
	}
}

// TestNoCodeBelowMainReadsTheProcesssWorkingDirectory holds the product to one read
// of the process's working directory, workingDir's, and every verb's flags to
// parse: a second read, or a verb parsing its flags past parse, would read a
// relative path from the process's directory in place of the invocation's.
func TestNoCodeBelowMainReadsTheProcesssWorkingDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir() && (entry.Name() == "testdata" || entry.Name() == "testsupport" || strings.HasPrefix(entry.Name(), ".")) && path != root:
			return filepath.SkipDir
		case entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, held := decl.(*ast.FuncDecl)
			if !held {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				call, held := node.(*ast.CallExpr)
				if !held {
					return true
				}
				selector, held := call.Fun.(*ast.SelectorExpr)
				if !held {
					return true
				}
				at := fset.Position(call.Pos())
				if pkg, held := selector.X.(*ast.Ident); held && pkg.Name == "os" && (selector.Sel.Name == "Getwd" || selector.Sel.Name == "Chdir") && fn.Name.Name != "workingDir" {
					t.Errorf("%s: os.%s in %s, want workingDir's read alone", at, selector.Sel.Name, fn.Name.Name)
				}
				if selector.Sel.Name == "Parse" && flagSet(selector.X) && fn.Name.Name != "parse" && fn.Name.Name != "invoke" {
					t.Errorf("%s: %s parses a verb's flags past configuredFlags.parse", at, fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: read the module's sources: %v", err)
	}
}

// flagSet reports whether x names a flag set as this package spells one: set, or a
// set field.
func flagSet(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name == "set"
	case *ast.SelectorExpr:
		return x.Sel.Name == "set"
	}
	return false
}

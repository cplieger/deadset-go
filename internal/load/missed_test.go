package load

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// Every value [treeWalk.apply] acts on leaves its record, [dirSeen] the one a
// building path's earlier meeting left, and every other value is an error: a new outcome with no row here fails the test, so no arm of the walk
// drops a directory with no record.
func TestEveryDirectoryOutcomeOfTheWalkLeavesItsRecord(t *testing.T) {
	root := writeTree(t, map[string]string{"app/go.mod": "module example.com/app\n\ngo 1.27.1\n", "app/p/p.go": "package p\n"})
	app := filepath.Join(root, "app")
	records := map[dirOutcome]func(w *treeWalk, path string) bool{
		dirSeen:       seenWhereABuildingPathRecorded,
		dirModuleRoot: func(w *treeWalk, path string) bool { return len(w.tree.roots) == 1 && w.tree.roots[0].at == path },
		dirUnread:     func(w *treeWalk, path string) bool { return len(w.top.Unread) == 1 && w.top.Unread[0] == path },
		dirUnlistable: func(w *treeWalk, path string) bool { return leftOutAs(w.tree.unloaded, path, LeftOutUnlistable) },
		dirUnbounded:  func(w *treeWalk, path string) bool { return leftOutAs(w.tree.unloaded, path, LeftOutUnbounded) },
		dirWalked:     func(w *treeWalk, path string) bool { return len(w.tree.dirs) == 1 && w.tree.dirs[0].pattern == "./p" },
		dirUnbuilt: func(w *treeWalk, _ string) bool {
			return len(w.tree.dirs)+len(w.tree.unloaded)+len(w.tree.roots)+len(w.top.Unread) == 0
		},
	}
	for outcome := range dirOutcome(math.MaxUint8) {
		top := &Topology{Root: app, root: scope.RootOf(app)}
		w := &treeWalk{ctx: t.Context(), top: top, fsys: hostFS, seen: make(map[dirID]vendorSpelling), above: make(map[dirID]bool)}
		path := filepath.Join(app, "p")
		met := metDir{outcome: outcome, entries: []fs.DirEntry{goFileEntry{}}, records: true}
		err := w.apply(path, "p", &met)
		record, ok := records[outcome]
		switch {
		case !ok && errors.Is(err, errNoOutcome):
		case !ok:
			t.Errorf("apply(outcome %d) = %v, an outcome with no record in this test: give it one", outcome, err)
		case err != nil:
			t.Errorf("apply(outcome %d) = %v, want no error", outcome, err)
		case !record(w, path):
			t.Errorf("apply(outcome %d) left tree %+v and unread %v, want its record", outcome, w.tree, top.Unread)
		}
	}
}

// seenWhereABuildingPathRecorded reports whether the walk w, meeting the directory
// at path first below the vendor directory, which builds no package of the
// module by that path, and then by a path that builds it, recorded it at the second meeting, and finds
// it [dirSeen] at a third.
func seenWhereABuildingPathRecorded(w *treeWalk, path string) bool {
	if w.meet(path, "vendor/p", reachPlain) != nil || len(w.tree.dirs) != 0 || w.meet(path, "p", reachPlain) != nil {
		return false
	}
	return w.outcome(path, "p", reachPlain).outcome == dirSeen && len(w.tree.dirs) == 1 && w.tree.dirs[0].pattern == "./p"
}

// goFileEntry is a listing's regular file p.go.
type goFileEntry struct{}

func (goFileEntry) Name() string               { return "p.go" }
func (goFileEntry) IsDir() bool                { return false }
func (goFileEntry) Type() fs.FileMode          { return 0 }
func (goFileEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// leftOutAs reports whether held leaves out the directory at path for reason.
func leftOutAs(held []LeftOutPackage, path string, reason LeftOutReason) bool {
	return len(held) == 1 && held[0].Dir == path && held[0].Reason == reason
}

// A configuration's build opens a Go-named entry by its name alone, as go/build
// decides before it reads a file: another platform's suffix, a leading
// underscore or dot is never opened, and a test file is.
func TestAConfigurationOpensAGoFileByItsNameAlone(t *testing.T) {
	linux := Configuration{OS: "linux", Arch: "amd64"}
	windows := Configuration{OS: "windows", Arch: "amd64"}
	for _, tc := range []struct {
		name          string
		linux, window bool
	}{
		{name: "x.go", linux: true, window: true},
		{name: "x_test.go", linux: true, window: true},
		{name: "x_linux.go", linux: true},
		{name: "x_amd64.go", linux: true, window: true},
		{name: "x_windows.go", window: true},
		{name: "x_windows_test.go", window: true},
		{name: "x_plan9.go"},
		{name: "x_linux_arm64.go"},
		{name: "_x.go"},
		{name: ".x.go"},
	} {
		if got := linux.opens([]string{tc.name}); got != tc.linux {
			t.Errorf("linux/amd64 opens(%s) = %t, want %t", tc.name, got, tc.linux)
		}
		if got := windows.opens([]string{tc.name}); got != tc.window {
			t.Errorf("windows/amd64 opens(%s) = %t, want %t", tc.name, got, tc.window)
		}
	}
}

// No function of the package but [statKind] tells one Stat or listing error from
// another, so every walk classifies an entry it cannot stat one way: any form
// [statErrorSites] refuses, anywhere else in a production file, fails the test.
func TestOnlyStatKindTellsOneStatErrorFromAnother(t *testing.T) {
	t.Parallel()
	if sites := statErrorSites(t, productionSources(t)); len(sites) > 0 {
		t.Errorf("%v tell one Stat error from another; ask statKind", sites)
	}
}

// productionSources is the content of every production file of the package, by
// file name.
func productionSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	sources := make(map[string]string)
	for _, entry := range entries {
		if name := entry.Name(); filepath.Ext(name) == goFileExt && !strings.HasSuffix(name, "_test.go") {
			content, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			sources[name] = string(content)
		}
	}
	if len(sources) == 0 {
		t.Fatal("Setup: no production file read")
	}
	return sources
}

// Every form of telling one Stat error from another is refused, in a function or
// a package variable's function literal, against a sentinel of fs, os or syscall
// or one the package declares from such a sentinel or an errno; statKind itself,
// an error no Stat gives and extracting a path error's path are not.
func TestStatErrorSitesRefusesEveryFormOfTellingStatErrorsApart(t *testing.T) {
	t.Parallel()
	const head = "package p\n\nimport (\n\t\"errors\"\n\t\"io\"\n\t\"io/fs\"\n\t\"os\"\n\t\"syscall\"\n)\n\n" +
		"var _, _, _, _, _ = errors.Is, io.EOF, fs.ErrNotExist, os.Stat, syscall.ENOENT\n\n"
	for _, tc := range []struct{ name, body string }{
		{"errors.Is with an fs sentinel", "func f(err error) bool { return errors.Is(err, fs.ErrNotExist) }"},
		{"errors.Is with a syscall errno", "func f(err error) bool { return errors.Is(err, syscall.ENAMETOOLONG) }"},
		{"os.IsNotExist", "func f(err error) bool { return os.IsNotExist(err) }"},
		{"os.IsTimeout", "func f(err error) bool { return os.IsTimeout(err) }"},
		{"a comparison", "func f(err error) bool { return err == syscall.ENOENT }"},
		{"a switch case", "func f(err error) bool {\n\tswitch err {\n\tcase fs.ErrPermission:\n\t\treturn true\n\t}\n\treturn false\n}"},
		{"errors.AsType of an errno", "func f(err error) bool { _, ok := errors.AsType[syscall.Errno](err); return ok }"},
		{"errors.As into an errno", "func f(err error) bool { var e syscall.Errno; return errors.As(err, &e) }"},
		{"a type assertion to an errno", "func f(err error) bool { _, ok := err.(syscall.Errno); return ok }"},
		{"a type switch on an errno", "func f(err error) bool {\n\tswitch err.(type) {\n\tcase syscall.Errno:\n\t\treturn true\n\t}\n\treturn false\n}"},
		{"a package sentinel from fs", "var errGone = fs.ErrNotExist\n\nfunc f(err error) bool { return errors.Is(err, errGone) }"},
		{"a package errno constant", "const errLoop = syscall.Errno(1921)\n\nfunc f(err error) bool { return err == errLoop }"},
		{"a package variable's function literal", "var f = func(err error) bool { return os.IsNotExist(err) }"},
		{"a function literal in a function", "func f() func(error) bool { return func(err error) bool { return os.IsPermission(err) } }"},
	} {
		if sites := statErrorSites(t, map[string]string{"p.go": head + tc.body + "\n"}); len(sites) == 0 {
			t.Errorf("statErrorSites(%s) = none, want the site refused", tc.name)
		}
	}
	control := head + "func statKind(err error) bool { return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) }\n\n" +
		"func g(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, fs.SkipAll) }\n\n" +
		"func h(err error) string {\n\tif e, ok := errors.AsType[*fs.PathError](err); ok {\n\t\treturn e.Path\n\t}\n\treturn \"\"\n}\n"
	if sites := statErrorSites(t, map[string]string{"p.go": control}); len(sites) > 0 {
		t.Errorf("statErrorSites(statKind, io.EOF, fs.SkipAll and a path error's path) = %v, want none", sites)
	}
}

// statErrorSites is where in sources, by file name and declaration, an error is
// told apart as a Stat or listing error outside the function statKind: errors.Is
// with a sentinel, an os.Is function, a comparison or a switch case with a
// sentinel, or errors.AsType, a type assertion or a variable of syscall.Errno. A sentinel is an Err of fs or os, an E of syscall, or a package
// constant or variable declared from one or from a syscall.Errno conversion.
func statErrorSites(t *testing.T, sources map[string]string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files := make(map[string]*ast.File)
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		file, err := parser.ParseFile(fset, name, sources[name], parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		files[name] = file
	}
	c := statErrorClassifier{local: make(map[string]bool)}
	for _, file := range files {
		c.declareSentinels(file)
	}
	var sites []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		for _, decl := range files[name].Decls {
			at := name
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Name.Name == "statKind" {
					continue
				}
				at += ":" + fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if c.classifies(n) {
					sites = append(sites, at)
				}
				return true
			})
		}
	}
	return sites
}

// statErrorClassifier is what [statErrorSites] reads of a package: the name of
// each sentinel the package declares.
type statErrorClassifier struct {
	local map[string]bool
}

// declareSentinels records each package constant or variable of file whose value
// is a sentinel or a syscall.Errno conversion.
func (c statErrorClassifier) declareSentinels(file *ast.File) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, v := range value.Values {
				if call, ok := v.(*ast.CallExpr); c.sentinel(v) || ok && isErrno(call.Fun) {
					c.local[value.Names[i].Name] = true
				}
			}
		}
	}
}

// sentinel reports whether e names a Stat or listing error's sentinel.
func (c statErrorClassifier) sentinel(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return c.local[e.Name]
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && (pkg.Name == "fs" || pkg.Name == "os") && strings.HasPrefix(e.Sel.Name, "Err") ||
			ok && pkg.Name == "syscall" && strings.HasPrefix(e.Sel.Name, "E")
	}
	return false
}

// isErrno reports whether e is the type syscall.Errno.
func isErrno(e ast.Expr) bool {
	return selects(e, "syscall", "Errno")
}

// classifies reports whether n tells an error apart as [statErrorSites] says.
func (c statErrorClassifier) classifies(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CallExpr:
		return c.classifyingCall(n)
	case *ast.BinaryExpr:
		return (n.Op == token.EQL || n.Op == token.NEQ) && (c.sentinel(n.X) || c.sentinel(n.Y))
	case *ast.CaseClause:
		return slices.ContainsFunc(n.List, c.sentinel)
	case *ast.TypeAssertExpr:
		return n.Type != nil && isErrno(n.Type)
	case *ast.ValueSpec:
		return n.Type != nil && isErrno(n.Type)
	}
	return false
}

// classifyingCall reports whether call is errors.Is with a sentinel, an os.Is
// function of an error, or errors.AsType of syscall.Errno; errors.As into one
// needs a variable of it, which [statErrorClassifier.classifies] refuses.
func (c statErrorClassifier) classifyingCall(call *ast.CallExpr) bool {
	if index, ok := call.Fun.(*ast.IndexExpr); ok {
		return selects(index.X, "errors", "AsType") && isErrno(index.Index)
	}
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := fun.X.(*ast.Ident)
	switch {
	case !ok:
		return false
	case pkg.Name == "errors" && fun.Sel.Name == "Is" && len(call.Args) == 2:
		return c.sentinel(call.Args[1])
	}
	return pkg.Name == "os" && slices.Contains([]string{"IsNotExist", "IsExist", "IsPermission", "IsTimeout"}, fun.Sel.Name)
}

// selects reports whether e is the selector pkg.name.
func selects(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg && sel.Sel.Name == name
}

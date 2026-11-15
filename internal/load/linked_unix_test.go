//go:build unix

package load

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/build"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"golang.org/x/tools/go/packages"
)

// coreUser is a package named name that calls the target's core.F.
func coreUser(name string) string {
	return "package " + name + "\n\nimport \"example.com/app/core\"\n\n// G calls core.F.\nfunc G() { core.F() }\n"
}

// differenceGuardTree is the fixture of the difference guard: an application at
// app calling core.F, a module app/nest below it and a declared consumer cons
// beside it, both building against app, and below each module every kind of
// directory ./... does not match that a build naming it compiles (links out, below
// a link, to an ancestor, to another device, to module roots; testdata,
// underscore, dot, ignored, node_modules, nested vendor). Below app also: the
// [differenceGuardSpecials] and [fileGrainShapes], testdata and dot modules, and
// a root vendor directory holding a program and a link to w/testdata/p.
func differenceGuardTree() (files, links map[string]string) {
	files = map[string]string{
		"app/go.mod": "module example.com/app\n\ngo 1.27.1\n\nignore ./skip\n",
		"app/main.go": "package main\n\nimport (\n\t\"example.com/app/core\"\n\t\"example.com/app/testdata/ic\"\n\t\"example.com/app/testdata/icgo\"\n)\n\n" +
			"func main() { core.F(); ic.H(); icgo.H() }\n",
		"app/main_windows.go": "package main\n\nimport (\n\t\"example.com/app/testdata/s1\"\n\t\"example.com/app/testdata/s1b\"\n)\n\n" +
			"func init() { s1.G(); s1b.G() }\n",
		"app/core/core.go": "// Package core is the target's.\npackage core\n\n// F is called.\nfunc F() {}\n",
		"app/nest/go.mod":  "module example.com/nest\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n\nignore ./skip\n",
		"app/nest/main.go": "package main\n\nimport \"example.com/app/core\"\n\nfunc main() { core.F() }\n",
		"cons/go.mod":      "module example.com/cons\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../app\n\nignore ./skip\n",
		"cons/main.go":     "package main\n\nimport \"example.com/app/core\"\n\nfunc main() { core.F() }\n",
		"plain/p.go":       "package plain\n",
		"broken/p.go":      "package broken\n\nimport \"example.com/absent\"\n\n// G calls absent.\nfunc G() { absent.Run() }\n",
		"other/go.mod":     "module example.com/other\n\ngo 1.27.1\n",
		"other/p.go":       "package other\n",
		"renamed/go.mod":   "module example.com/renamer\n\ngo 1.27.1\n\nrequire example.com/renamed v0.0.0\n\nreplace example.com/renamed => ../app\n",
		"renamed/main.go":  "package main\n\nimport \"example.com/renamed/core\"\n\nfunc main() { core.F() }\n",
	}
	links = map[string]string{"app/renamed": "../renamed", "app/other": "../other", "app/testdata/q/x_aix.go": "missing.go"}
	files["app/testdata/q/p.go"] = coreUser("q")
	files["app/testdata/r/p.go"] = coreUser("r")
	files["app/testdata/locked/c/p.go"] = coreUser("c")
	files["app/vendor/v.go"] = coreUser("vendor")
	files["app/w/testdata/p/p.go"] = coreUser("p")
	links["app/vendor/l"] = "../w/testdata/p"
	maps.Copy(files, fileGrainShapes())
	for _, module := range []string{"app/testdata/tdmod", "app/.mods/dotmod"} {
		files[module+"/go.mod"] = "module example.com/" + filepath.Base(module) + "\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../../\n"
		files[module+"/main.go"] = "package main\n\nimport \"example.com/app/core\"\n\nfunc main() { core.F() }\n"
	}
	for _, module := range []string{"app", "app/nest", "cons"} {
		out := strings.ReplaceAll(module, "/", "-") + "-out"
		up := strings.Repeat("../", strings.Count(module, "/")+1)
		for _, kind := range []string{"testdata/p", "_x", ".hid", "skip", "node_modules/p", "x/vendor/q"} {
			files[module+"/"+kind+"/p.go"] = coreUser("p")
		}
		files[out+"/p.go"] = coreUser("out")
		files[out+"/below/p.go"] = coreUser("below")
		files[out+"-deeper/p.go"] = coreUser("deeper")
		links[module+"/l"] = up + out
		links[out+"/deeper"] = "../" + out + "-deeper"
		links[module+"/plain"] = up + "plain"
		links[module+"/broken"] = up + "broken"
		links[module+"/up"] = ".."
		links[module+"/dev"] = "/dev"
	}
	return files, links
}

// cgoUser is a package named name, importing "C", that calls core.F.
func cgoUser(name string) string {
	return "package " + name + "\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n\nimport \"example.com/app/core\"\n\n// G calls core.F.\nfunc G() { core.F() }\n"
}

// fileGrainShapes are directories one configuration reads only some Go files
// of. Read whole by windows: below app, one only a windows name or constraint
// builds, a host file beside a windows one, one below an underscore directory, and
// below app/nest a windows-only one. Read by none: one only plan9 builds, one
// importing "C" with cgo off, two app imports holding a host file beside one only
// an underived conjunction or cgo builds. An ignore-only file beside a host file;
// s1 and s1b, which windows imports and counts, s1b with a host-only file.
func fileGrainShapes() map[string]string {
	hostFile := func(name string) string { return "package " + name + "\n\n// H is called.\nfunc H() {}\n" }
	return map[string]string{
		"app/testdata/wonly/p_windows.go": coreUser("wonly"),
		"app/testdata/p9/p_plan9.go":      coreUser("p9"),
		"app/testdata/ic/h.go":            hostFile("ic"),
		"app/testdata/ic/z.go":            "//go:build windows && arm64\n\n" + coreUser("ic"),
		"app/testdata/icgo/h.go":          hostFile("icgo"),
		"app/testdata/icgo/c.go":          cgoUser("icgo"),
		"app/nest/winsvc/svc_windows.go":  coreUser("winsvc"),
		"app/testdata/win/p_windows.go":   coreUser("win"),
		"app/testdata/tag/p.go":           "//go:build windows\n\n" + coreUser("tag"),
		"app/testdata/cgo/p.go":           cgoUser("cgo"),
		"app/_ex/demo/p_windows.go":       coreUser("demo"),
		"app/testdata/mix/h.go":           "package mix\n",
		"app/testdata/mix/p_windows.go":   coreUser("mix"),
		"app/testdata/ign/p.go":           coreUser("ign"),
		"app/testdata/ign/x.go":           "//go:build ignore\n\npackage ign\n",
		"app/testdata/s1/p.go":            coreUser("s1"),
		"app/testdata/s1b/p.go":           coreUser("s1b"),
		"app/testdata/s1b/host.go":        "//go:build !windows\n\npackage s1b\n\nimport \"example.com/app/core\"\n\nfunc init() { core.F() }\n",
	}
}

// Every directory a build of a loaded module can name that holds a Go file, minus
// every Go file any load of any configuration of the run compiled, is left out
// ([program.4]): each package of it reaching app's core, each directory that does
// not list or whose files the run read only some of unloaded, each link with no
// bound at the link, and no package whose every file there the run counts. The
// oracle computes it per configuration from a plain walk, links followed, and the
// go command's answers (`go list -e -json -deps`, `go list -m -json all`).
func TestTheLoadLeavesOutEveryDirectoryABuildNamesThatNoLoadListed(t *testing.T) {
	deadline.Row(t, 5*time.Minute, "a load of a tree holding directories ./... does not match did not return", func(t *testing.T) {
		stableToolchain(t)
		files, links := differenceGuardTree()
		root := writeTree(t, files)
		for _, link := range slices.Sorted(maps.Keys(links)) {
			if err := sandbox.Symlink(links[link], filepath.Join(root, filepath.FromSlash(link))); err != nil {
				t.Fatalf("Setup: link %s to %s: %v", link, links[link], err)
			}
		}
		differenceGuardSpecials(t, root)
		app, cons := filepath.Join(root, "app"), filepath.Join(root, "cons")
		doc := scope.Document{Target: scope.Module{Path: app}, Consumers: []scope.Module{{Path: cons, ID: "example.com/cons"}}}
		top, err := Discover(deadline.Context(t), doc)
		if err != nil {
			t.Fatalf("Discover(a tree holding directories ./... does not match) = _, %v, want no error", err)
		}
		configurations := []Configuration{HostConfiguration(), {ID: "windows-amd64", OS: "windows", Arch: "amd64", Origin: Derived}}
		m := NewMatrix(top)
		var listed []map[[2]uint64]bool
		read := make(map[[2]uint64]map[string]bool)
		kept, _, err := m.All(deadline.Context(t), configurations, func(r *Result) error {
			held, compiled := listedIDs(t, r)
			listed = append(listed, held)
			for id, names := range compiled {
				if read[id] == nil {
					read[id] = make(map[string]bool)
				}
				maps.Copy(read[id], names)
			}
			return nil
		})
		if err != nil || len(kept) != len(configurations) {
			t.Fatalf("Matrix.All(a tree holding directories ./... does not match) = %v, %v, want every configuration and no error", kept, err)
		}
		loaded := []string{app, filepath.Join(app, "nest"), cons}
		o := differenceOracle{
			t: t, root: root, app: resolvedDir(t, app), loaded: loaded, read: read, counted: make(map[[2]uint64]map[string]bool),
			unloaded: make(map[string]bool), consumers: make(map[string]bool), unread: make(map[string]bool),
		}
		for _, module := range loaded {
			o.walk(module)
		}
		o.settle(configurations, listed)
		got := make([][]LeftOutPackage, len(kept))
		for i := range kept {
			got[i] = m.LeftOut(kept[i].ID)
		}
		o.check(got)
		var unread []string
		for _, path := range top.Unread {
			unread = append(unread, o.rel(path))
		}
		if want := slices.Sorted(maps.Keys(o.unread)); !slices.Equal(slices.Sorted(slices.Values(unread)), want) {
			t.Errorf("Discover().Unread = %v, want %v", unread, want)
		}
	})
}

// differenceOracle is what the guard's own reading expects: every directory the
// walk met holding a Go file, the names of the Go files a load of any
// configuration compiled in each directory, by identity, and of those whose
// references a load of any configuration counts, for each configuration the
// resolved directory, import path and compiled files of each package of the
// difference that reaches app's core, the path below root of each directory or
// link left out unloaded, and of each link to a module whose graph builds app's
// directory.
type differenceOracle struct {
	t         *testing.T
	read      map[[2]uint64]map[string]bool
	counted   map[[2]uint64]map[string]bool
	left      []map[string]oracleLeft
	unloaded  map[string]bool
	consumers map[string]bool
	unread    map[string]bool
	root, app string
	loaded    []string
	dirs      []oracleDir
}

// oracleLeft is one package the oracle expects a configuration to leave out: its
// import path, the identity of its directory and the names of the Go files the
// configuration compiles there.
type oracleLeft struct {
	path  string
	files []string
	id    [2]uint64
}

// oracleDir is one directory the oracle's walk met holding a Go file: the
// module walked, its path through the links that reach it, its identity, its
// regular Go files' names, the names of its Go-named entries that are no regular
// file, and whether ./... run in the module does not name it by that path.
type oracleDir struct {
	module, dir        string
	goFiles, irregular []string
	id                 [2]uint64
	beyond             bool
}

// differenceGuardSpecials makes, below the guard's tree at root, a pipe only
// another platform's build opens beside app/testdata/q's link to nothing of such a
// name, a pipe this platform's build opens in app/testdata/r, and leaves
// app/testdata/locked listable but not enterable until the test ends, so its
// directory c cannot be stat'd: to a run as root every entry stays enterable.
func differenceGuardSpecials(t *testing.T, root string) {
	t.Helper()
	sandbox.Fifo(t, filepath.Join(root, "app", "testdata", "q", "x_plan9.go"))
	sandbox.Fifo(t, filepath.Join(root, "app", "testdata", "r", "x.go"))
	locked := filepath.Join(root, "app", "testdata", "locked")
	if err := sandbox.Chmod(locked, 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = sandbox.Chmod(locked, 0o700) })
}

// opensByName reports whether a build of c opens the Go-named file name, by
// go/build's own name rule: it reads a file's constraints only after its name
// matches, so the stub it opens decides nothing.
func opensByName(c *Configuration, name string) bool {
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH = c.OS, c.Arch
	ctxt.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("package p\n")), nil }
	matched, err := ctxt.MatchFile(".", name)
	return matched || err != nil
}

// listedIDs is the device and inode of the directory of every package the graphs
// of got's packages and of its consumers' packages hold, and the names of the Go
// files each compiles there, its test files among them.
func listedIDs(t *testing.T, got *Result) (map[[2]uint64]bool, map[[2]uint64]map[string]bool) {
	t.Helper()
	roots := slices.Clone(got.Packages)
	for _, consumer := range slices.Concat(got.Nested, got.Consumers) {
		roots = append(roots, consumer.Packages...)
	}
	held := make(map[[2]uint64]bool)
	read := make(map[[2]uint64]map[string]bool)
	packages.Visit(roots, nil, func(p *packages.Package) {
		dirs := []string{p.Dir}
		for _, file := range p.GoFiles {
			dirs = append(dirs, filepath.Dir(file))
			if id, ok := statID(filepath.Dir(file)); ok {
				if read[id] == nil {
					read[id] = make(map[string]bool)
				}
				read[id][filepath.Base(file)] = true
			}
		}
		for _, dir := range dirs {
			if id, ok := statID(dir); ok && dir != "" {
				held[id] = true
			}
		}
	})
	return held, read
}

// statID is the device and inode of the directory at path, links followed.
func statID(path string) ([2]uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return [2]uint64{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]uint64{}, false
	}
	return [2]uint64{uint64(int64(st.Dev)), st.Ino}, true
}

// walk walks the module rooted at module on every path but one that loops,
// entering no other module root, and records each directory holding a Go file
// ([oracleDir]), which [differenceOracle.settle] judges. One it cannot stat or
// list is unread inside the root and unloaded outside it, a link to a strict
// ancestor or another device is unloaded, and every other module root is asked
// of its module graph.
func (o *differenceOracle) walk(module string) {
	o.t.Helper()
	start, _ := statID(module)
	above := make(map[[2]uint64]bool)
	for at := filepath.Dir(resolvedDir(o.t, module)); ; at = filepath.Dir(at) {
		if id, ok := statID(at); ok {
			above[id] = true
		}
		if filepath.Dir(at) == at {
			break
		}
	}
	var visit func(dir string, entered map[[2]uint64]bool, beyond bool)
	visit = func(dir string, entered map[[2]uint64]bool, beyond bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			o.unlisted(dir)
			return
		}
		holds := false
		at := oracleDir{module: module, dir: dir, beyond: beyond}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			goNamed := filepath.Ext(path) == ".go"
			switch {
			case errors.Is(err, fs.ErrNotExist) && entry.Type()&fs.ModeSymlink != 0:
				if goNamed {
					at.irregular = append(at.irregular, entry.Name())
				}
			case err != nil:
				o.unlisted(path)
			case info.Mode().IsRegular():
				holds = holds || goNamed
				if goNamed && !strings.HasPrefix(entry.Name(), "_") && !strings.HasPrefix(entry.Name(), ".") {
					at.goFiles = append(at.goFiles, entry.Name())
				}
			case !info.IsDir():
				if goNamed {
					at.irregular = append(at.irregular, entry.Name())
				}
			case fileExists(filepath.Join(path, "go.mod")):
				if mod, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && mod.Mode().IsRegular() {
					o.module(path)
				}
			default:
				id, _ := statID(path)
				if entry.Type()&fs.ModeSymlink != 0 && (above[id] || id[0] != start[0]) {
					o.unloaded[o.rel(path)] = true
				} else if !entered[id] {
					below := maps.Clone(entered)
					below[id] = true
					visit(path, below, beyond || entry.Type()&fs.ModeSymlink != 0 || patternSkips(module, path))
				}
			}
		}
		if holds {
			at.id, _ = statID(dir)
			o.dirs = append(o.dirs, at)
		}
	}
	visit(module, map[[2]uint64]bool{start: true}, false)
}

// settle asks, in each configuration, the go command of every directory the
// walk met that the run's loads of that configuration did not list, listed by
// configuration, path by path until one builds it (the go command's refusal
// decides a vendored path), and then judges each directory under the first path
// that builds it by the files the whole run read ([differenceOracle.unreadAt]):
// one a configuration's build opens a Go-named irregular entry of is unread. A
// package of an unread directory, or one whose every compiled file the run
// counts ([differenceOracle.count]), is left out in no configuration.
func (o *differenceOracle) settle(configurations []Configuration, listed []map[[2]uint64]bool) {
	o.t.Helper()
	first := make(map[[2]uint64]int)
	opened := make(map[[2]uint64]bool)
	failed := make(map[[2]uint64]bool)
	o.left = make([]map[string]oracleLeft, len(configurations))
	for ci := range configurations {
		c := &configurations[ci]
		o.left[ci] = make(map[string]oracleLeft)
		o.count(c)
		decided := maps.Clone(listed[ci])
		for i := range o.dirs {
			d := &o.dirs[i]
			building := true
			switch {
			case decided[d.id]:
			case slices.ContainsFunc(d.irregular, func(name string) bool { return opensByName(c, name) }):
				opened[d.id] = true
			default:
				var refused bool
				building, refused = o.ask(c, d, o.left[ci])
				failed[d.id] = failed[d.id] || refused
			}
			if !building {
				continue
			}
			decided[d.id] = true
			if _, met := first[d.id]; !met {
				first[d.id] = i
			}
		}
	}
	for id, i := range first {
		if d := &o.dirs[i]; o.unreadAt(d, opened[id], failed[id]) {
			o.unloaded[o.rel(d.dir)] = true
			for _, left := range o.left {
				delete(left, resolvedDir(o.t, d.dir))
			}
		}
	}
	for _, left := range o.left {
		maps.DeleteFunc(left, func(_ string, held oracleLeft) bool {
			return len(held.files) > 0 && !slices.ContainsFunc(held.files, func(name string) bool { return !o.counted[held.id][name] })
		})
	}
}

// count adds to the run's counted set the Go files the go command compiles in
// configuration c for every package whose references the run counts: each
// package of the ./... of every loaded module but one below a node_modules or
// vendor directory, which is no package of its module, each package of app's
// module its production code imports from outside them, and each package of an
// underscore directory directly below app that lists with no error, which the run
// loads as app's. The fixture holds no test file, so no test imports a package.
func (o *differenceOracle) count(c *Configuration) {
	o.t.Helper()
	add := func(p *listedPackage) {
		id, ok := statID(p.Dir)
		if !ok || p.Error != nil {
			return
		}
		if o.counted[id] == nil {
			o.counted[id] = make(map[string]bool)
		}
		for _, name := range slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles) {
			o.counted[id][name] = true
		}
	}
	for _, module := range o.loaded {
		unit := resolvedDir(o.t, module) == o.app
		decode(o.t, o.goJSON(c, module, "list", "-e", "-json", "-deps", "./..."), func(dec *json.Decoder) error {
			var p listedPackage
			err := dec.Decode(&p)
			own := err == nil && !p.DepOnly && !slices.ContainsFunc(strings.Split(filepath.ToSlash(mustRel(o.t, module, p.Dir)), "/"), func(elem string) bool {
				return elem == "node_modules" || elem == "vendor"
			})
			if err == nil && (own || p.DepOnly && unit && p.Module != nil && p.Module.Path == "example.com/app") {
				add(&p)
			}
			return err
		})
	}
	for i := range o.dirs {
		d := &o.dirs[i]
		if d.module != o.loaded[0] || !strings.HasPrefix(mustRel(o.t, d.module, d.dir), "_") {
			continue
		}
		decode(o.t, o.goJSON(c, d.module, "list", "-e", "-json", "./"+filepath.ToSlash(mustRel(o.t, d.module, d.dir))), func(dec *json.Decoder) error {
			var p listedPackage
			err := dec.Decode(&p)
			if err == nil {
				add(&p)
			}
			return err
		})
	}
}

// patternSkips reports whether ./... run in module skips the directory at path
// by its own name: testdata, vendor, node_modules, a leading dot or underscore,
// or the fixture's ignored skip.
func patternSkips(module, path string) bool {
	name := filepath.Base(path)
	return name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") ||
		strings.HasPrefix(name, "_") || filepath.Dir(path) == module && name == "skip"
}

// unreadAt reports whether the run does not read the directory d by the files
// any configuration's loads compiled: where a build opens one of its irregular
// entries, which no load compiles; where ./... does not name it or a
// configuration's listing of it failed, where one of its Go files a build may
// compile is not read; where a module other than app's names it, where none is,
// since no derived configuration reads such a module's constraints. A file only
// the ignore tag builds compiles in no build of the run.
func (o *differenceOracle) unreadAt(d *oracleDir, opened, failed bool) bool {
	built, held := 0, 0
	for _, name := range d.goFiles {
		switch {
		case o.read[d.id][name]:
			built, held = built+1, held+1
		case !onlyIgnoreBuilds(o.t, filepath.Join(d.dir, name)):
			built++
		}
	}
	switch {
	case opened:
		return true
	case d.beyond || failed:
		return held < built
	case resolvedDir(o.t, d.module) != o.app:
		return built > 0 && held == 0
	}
	return false
}

// onlyIgnoreBuilds reports whether the Go file at path opens with the line
// //go:build ignore.
func onlyIgnoreBuilds(t *testing.T, path string) bool {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	first, _, _ := strings.Cut(string(content), "\n")
	return first == "//go:build ignore"
}

// unlisted records the directory at path, which the walk cannot stat or list:
// unread inside the root and unloaded outside it.
func (o *differenceOracle) unlisted(path string) {
	if rel := o.rel(path); strings.HasPrefix(rel, "app/") {
		o.unread[rel] = true
	} else {
		o.unloaded[rel] = true
	}
}

// rel is path below the fixture's root, by its names.
func (o *differenceOracle) rel(path string) string {
	return filepath.ToSlash(mustRel(o.t, o.root, path))
}

// listedPackage is one package of `go list -e -json`.
type listedPackage struct {
	Error        *struct{ Err string }
	DepsErrors   []struct{ Err string }
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Deps         []string
	Module       *struct{ Path string }
	DepOnly      bool
}

// ask records what the go command lists of the directory d in configuration c,
// by its path from its module, adding the files it compiles there to what the
// run reads where the listing reports no error, and reports whether that path
// builds it, which it does not where the go command refuses it as a path below
// the vendor directory, and whether the listing reports an error. A package there
// that compiles a file and reaches app's core is left out in left, the
// configuration's, at the directory the go command names.
func (o *differenceOracle) ask(c *Configuration, d *oracleDir, left map[string]oracleLeft) (building, failed bool) {
	o.t.Helper()
	pattern := "./" + filepath.ToSlash(mustRel(o.t, d.module, d.dir))
	vendored := false
	var named *listedPackage
	decode(o.t, o.goJSON(c, d.module, "list", "-e", "-json", "-deps", pattern), func(dec *json.Decoder) error {
		var p listedPackage
		err := dec.Decode(&p)
		if err == nil {
			failed = failed || p.Error != nil || len(p.DepsErrors) > 0
			vendored = vendored || p.ImportPath == pattern && p.Error != nil &&
				(strings.Contains(p.Error.Err, "has no package path") || strings.Contains(p.Error.Err, "is not a package listed in vendor/modules.txt"))
			if filepath.Clean(p.Dir) == filepath.Clean(d.dir) {
				named = &p
			}
		}
		return err
	})
	if vendored {
		return false, false
	}
	if o.read[d.id] == nil {
		o.read[d.id] = make(map[string]bool)
	}
	if named != nil && !failed {
		for _, name := range slices.Concat(named.GoFiles, named.CgoFiles, named.TestGoFiles, named.XTestGoFiles) {
			o.read[d.id][name] = true
		}
		if len(named.GoFiles) > 0 && slices.Contains(named.Deps, "example.com/app/core") {
			id, _ := statID(named.Dir)
			left[resolvedDir(o.t, named.Dir)] = oracleLeft{path: named.ImportPath, id: id, files: slices.Concat(named.GoFiles, named.CgoFiles)}
		}
	}
	return true, failed
}

// goJSON is the standard output of the go command run with args in dir for
// configuration c, the host's where nil, in a process group of its own, fetching
// nothing, under no workspace and in the -mod mode the module's own files decide,
// as the run's loads are.
func (o *differenceOracle) goJSON(c *Configuration, dir string, args ...string) []byte {
	o.t.Helper()
	cmd := deadline.Command(deadline.Context(o.t), "go", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOPROXY=off", "GOFLAGS=", "CGO_ENABLED=0", "GOWORK=off")
	if c != nil {
		cmd.Env = append(cmd.Env, "GOOS="+c.OS, "GOARCH="+c.Arch)
	}
	out, err := cmd.Output()
	deadline.Reap(cmd)
	if err != nil {
		o.t.Fatalf("Setup: go %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return out
}

// decode calls next until the stream out ends.
func decode(t *testing.T, out []byte, next func(*json.Decoder) error) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(out))
	for {
		if err := next(d); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatalf("Setup: read the go command's answer: %v", err)
		}
	}
}

// module records the module root at link, through links or not, as a consumer
// where the module graph the go command reports in the directory it leads to,
// where that module's own build runs, builds a module from app's directory: a
// loaded module, which the run reads, is none.
func (o *differenceOracle) module(link string) {
	o.t.Helper()
	if slices.ContainsFunc(o.loaded, func(dir string) bool { return resolvedDir(o.t, dir) == resolvedDir(o.t, link) }) {
		return
	}
	decode(o.t, o.goJSON(nil, resolvedDir(o.t, link), "list", "-m", "-e", "-json", "-mod=readonly", "all"), func(d *json.Decoder) error {
		var m struct {
			Replace   *struct{ Dir, Version string }
			Dir, Path string
			Main      bool
		}
		err := d.Decode(&m)
		from := m.Dir
		if m.Replace != nil {
			from = m.Replace.Dir
		}
		if err == nil && from != "" && (m.Main || m.Replace != nil) && resolvedDir(o.t, from) == o.app {
			o.consumers[o.rel(link)] = true
		}
		return err
	})
}

// check holds got, the left-out set of each configuration of the run, to the
// oracle's: each package it expects a configuration to leave out is left out
// there at its resolved directory, and no other; each directory and link it
// expects unloaded is left out unloaded, each link to a module building app is
// left out for its module, and the run leaves out nothing else.
func (o *differenceOracle) check(got [][]LeftOutPackage) {
	t := o.t
	t.Helper()
	unloaded := make(map[string]bool)
	consumers := make(map[string]bool)
	for ci, configuration := range got {
		left := make(map[string]bool)
		for _, held := range configuration {
			switch {
			case held.Path != "":
				left[resolvedDir(t, held.Dir)] = true
				if _, expected := o.left[ci][resolvedDir(t, held.Dir)]; !expected {
					t.Errorf("configuration %d left out %s at %s, which is no directory of the difference reaching app's core that it compiles a file of no load counts", ci, held.Path, held.Dir)
				}
			case held.Reason == LeftOutConsumer || held.Reason == LeftOutRenamed || held.Reason == LeftOutUnanswered:
				consumers[o.rel(held.Dir)] = true
			default:
				unloaded[o.rel(held.Dir)] = true
			}
		}
		for dir, held := range o.left[ci] {
			if !left[dir] {
				t.Errorf("configuration %d compiles %s from %s, which no load listed, and its LeftOut %+v does not hold it", ci, held.path, dir, configuration)
			}
		}
	}
	for name, want := range map[string][2]map[string]bool{"unloaded": {o.unloaded, unloaded}, "left out for its module": {o.consumers, consumers}} {
		if !maps.Equal(want[0], want[1]) {
			t.Errorf("Load() left out %v %s, want %v", slices.Sorted(maps.Keys(want[1])), name, slices.Sorted(maps.Keys(want[0])))
		}
	}
	o.checkShapes()
}

// checkShapes fails where the oracle's own expectation misses a shape of the
// fixture, so a guard whose oracle drifted with the product fails. Of the two
// packages app's windows-only file imports, s1 is left out in no configuration,
// and s1b only in the host's, the first.
func (o *differenceOracle) checkShapes() {
	t := o.t
	t.Helper()
	paths := func(left map[string]oracleLeft) []string {
		var held []string
		for _, one := range left {
			held = append(held, one.path)
		}
		return held
	}
	var held []string
	dirs := make(map[string]bool)
	for _, left := range o.left {
		held = append(held, paths(left)...)
		for dir := range left {
			dirs[dir] = true
		}
	}
	unread := []string{"app/testdata/cgo", "app/testdata/p9", "app/testdata/ic", "app/testdata/icgo"}
	read := []string{"example.com/app/testdata/win", "example.com/app/testdata/tag", "example.com/app/testdata/mix", "example.com/app/testdata/wonly"}
	if len(dirs) < 30 || len(o.unloaded) < 11 || len(o.consumers) != 3 ||
		!o.unloaded["app/testdata/r"] || o.unloaded["app/nest/winsvc"] || o.unloaded["app/_ex/demo"] ||
		slices.ContainsFunc(unread, func(dir string) bool { return !o.unloaded[dir] }) ||
		slices.ContainsFunc(read, func(path string) bool { return !slices.Contains(held, path) }) || !slices.Contains(held, "example.com/app/testdata/ign") ||
		!o.consumers["app/testdata/tdmod"] || !o.consumers["app/.mods/dotmod"] ||
		!slices.Contains(held, "example.com/app/testdata/q") || !slices.Contains(held, "example.com/app/vendor") || !slices.Contains(held, "example.com/app/w/testdata/p") {
		t.Errorf("the oracle expects %v left out, %v unloaded and %v consuming modules, want every shape of the fixture among them", o.left, o.unloaded, o.consumers)
	}
	if slices.Contains(held, "example.com/app/testdata/s1") || o.unloaded["app/testdata/s1"] || o.unloaded["app/testdata/s1b"] ||
		!slices.Contains(paths(o.left[0]), "example.com/app/testdata/s1b") || slices.Contains(paths(o.left[1]), "example.com/app/testdata/s1b") {
		t.Errorf("the oracle expects %v left out by configuration, want testdata/s1 in none and testdata/s1b only in the host's", o.left)
	}
}

// fileExists reports whether a file is at path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// mustRel is dir relative to base.
func mustRel(t *testing.T, base, dir string) string {
	t.Helper()
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		t.Fatalf("Setup: %s relative to %s: %v", dir, base, err)
	}
	return rel
}

// resolvedDir is dir with every link resolved.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("Setup: resolve %s: %v", dir, err)
	}
	return resolved
}

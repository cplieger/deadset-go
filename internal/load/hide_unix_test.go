//go:build unix

package load

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/deadline"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// layout is one draw of the axes the go command decides what it reads by: the
// workspace file's go line, the module's, the vendor tree, the go env file's
// GOFLAGS, a nested module and how a replace directive names the replaced module;
// and those the scan must bound or enter: a symbolic link to an ancestor of a
// package, a testdata fixture whose module file replaces a module with the
// filesystem root, a graph of links whose paths multiply, a link out of the tree,
// a package in a directory a dot starts, a package named objects beside git's HEAD
// file and refs directory, and a .git directory holding an object store.
type layout struct {
	workGo, modGo string // workGo empty for no workspace file
	vendor        int    // 0 none, 1 written for module mode, 2 its workspace marker flipped, 3 no vendor list
	modMode       bool   // whether the go env file sets -mod=mod
	nested        int    // 0 none, 1 a nested module the target imports through a replace, 2 one nothing imports
	replace       int    // 0 unversioned, 1 at the required version, 2 at another one
	ancestorLink  bool   // whether p/up links to the module's root
	fixture       bool   // whether testdata/f holds a module file replacing a module with /
	dag           bool   // whether p/d links to dag/L0, each of dag/L0 to L5 linking twice to the next
	out           int    // 0 none, 1 p/o links to a directory outside the tree holding pipes, 2 p imports that directory's package k
	dot           bool   // whether p imports the package of .hid
	store         bool   // whether p imports store/objects, beside store/HEAD and store/refs
	git           bool   // whether .git holds HEAD, refs and objects
}

// fixtureDir is the directory of a layout's fixture module, below the root.
const fixtureDir = "testdata/f"

// drawLayout is the layout seed draws.
func drawLayout(seed uint64) layout {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return layout{
		workGo:       []string{"", "1.18", "1.21", "1.22"}[r.IntN(4)],
		modGo:        []string{"1.13", "1.22"}[r.IntN(2)],
		vendor:       r.IntN(4),
		modMode:      r.IntN(2) == 0,
		nested:       r.IntN(3),
		replace:      r.IntN(3),
		ancestorLink: r.IntN(2) == 0,
		fixture:      r.IntN(2) == 0,
		dag:          r.IntN(2) == 0,
		out:          r.IntN(3),
		dot:          r.IntN(2) == 0,
		store:        r.IntN(2) == 0,
		git:          r.IntN(2) == 0,
	}
}

// tree is the layout's tree: a module m whose main package imports p and the
// replaced module example.com/b's package q, which imports r; p imports a workspace
// module's package where a workspace file lists one, and the nested module's
// package where the layout imports one.
func (l layout) tree() map[string]string {
	required, replace := "v0.0.0", "replace example.com/b => ./b\n"
	switch l.replace {
	case 1:
		required, replace = "v1.0.0", "replace example.com/b v1.0.0 => ./b\n"
	case 2:
		required, replace = "v1.2.0", "replace example.com/b v1.0.0 => ./b\n"
	}
	goMod := "module example.com/m\n\ngo " + l.modGo + "\n\nrequire example.com/b " + required + "\n"
	pImports := []string{}
	if l.nested == 1 {
		goMod += "\nrequire example.com/m/sub v0.0.0\n"
		replace += "replace example.com/m/sub => ./sub\n"
		pImports = append(pImports, "example.com/m/sub/s")
	}
	if l.workGo != "" {
		pImports = append(pImports, "example.com/w/t")
	}
	if l.out == 2 {
		pImports = append(pImports, "example.com/m/p/o/k")
	}
	if l.dot {
		pImports = append(pImports, "example.com/m/.hid")
	}
	if l.store {
		pImports = append(pImports, "example.com/m/store/objects")
	}
	files := map[string]string{
		"go.mod":   goMod + "\n" + replace,
		"main.go":  "package main\n\nimport (\n\t_ \"example.com/b/q\"\n\t_ \"example.com/m/p\"\n)\n\nfunc main() {}\n",
		"p/p.go":   "package p\n" + blankImports(pImports),
		"b/go.mod": "module example.com/b\n\ngo 1.22\n",
		"b/q/q.go": "package q\n\nimport _ \"example.com/b/r\"\n",
		"b/r/r.go": "package r\n",
	}
	if l.nested != 0 {
		files["sub/go.mod"] = "module example.com/m/sub\n\ngo 1.22\n"
		files["sub/s/s.go"] = "package s\n"
	}
	if l.fixture {
		files[fixtureDir+"/go.mod"] = "module example.com/f\n\ngo 1.22\n\nreplace example.com/x => /\n"
	}
	if l.dag {
		files["dag/L5/z.go"] = "package z\n"
	}
	if l.dot {
		files[".hid/h.go"] = "package hid\n"
	}
	if l.store {
		files["store/HEAD"] = "ref: refs/heads/main\n"
		files["store/refs/heads/main"] = ""
		files["store/objects/o.go"] = "package objects\n"
	}
	if l.git {
		files[".git/HEAD"] = "ref: refs/heads/main\n"
		files[".git/refs/heads/main"] = ""
		files[".git/objects/ab/cdef"] = ""
	}
	if l.workGo != "" {
		files["go.work"] = "go " + l.workGo + "\n\nuse (\n\t.\n\t./w\n)\n"
		files["w/go.mod"] = "module example.com/w\n\ngo 1.18\n"
		files["w/t/t.go"] = "package t\n"
	}
	return files
}

// blankImports is a Go import declaration of each of paths, blank.
func blankImports(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nimport (\n")
	for _, path := range paths {
		fmt.Fprintf(&b, "\t_ %q\n", path)
	}
	b.WriteString(")\n")
	return b.String()
}

// prepare writes what the layout needs beyond its files: the module cache of a
// replace at another version, the vendor tree, the go env file and the links. It
// returns the directory p/o leads to, empty where the layout holds no such link.
func (l layout) prepare(t *testing.T, root string) (outside string) {
	t.Helper()
	if l.ancestorLink {
		linkTo("..")(t, filepath.Join(root, "p", "up"))
	}
	if l.dag {
		linkTo("../dag/L0")(t, filepath.Join(root, "p", "d"))
		for i := range 5 {
			mkdir(t, filepath.Join(root, "dag", fmt.Sprintf("L%d", i)))
			for j := range 2 {
				linkTo(fmt.Sprintf("../L%d", i+1))(t, filepath.Join(root, "dag", fmt.Sprintf("L%d", i), fmt.Sprintf("s%d", j)))
			}
		}
	}
	if l.out != 0 {
		outside = sandbox.Dir(t)
		writeBody("package k\n")(t, filepath.Join(mkdir(t, filepath.Join(outside, "k")), "k.go"))
		linkTo(outside)(t, filepath.Join(root, "p", "o"))
	}
	if l.replace == 2 {
		tidyFromCache(t, root, cacheEntry{path: "example.com/b", version: "v1.2.0", files: map[string]string{
			"go.mod": "module example.com/b\n\ngo 1.22\n", "q/q.go": "package q\n\nimport _ \"example.com/b/r\"\n", "r/r.go": "package r\n",
		}})
	}
	list := filepath.Join(root, vendorDirName, vendorListName)
	switch l.vendor {
	case 1, 2:
		if !tryGo(t, root, "mod", "vendor") {
			break
		}
		if l.vendor == 2 {
			text, err := os.ReadFile(list)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			writeBody("## workspace\n"+string(text))(t, list)
		}
	case 3:
		writeBody("package q\n")(t, filepath.Join(mkdir(t, filepath.Join(root, vendorDirName, "example.com", "b", "q")), "q.go"))
	}
	if l.modMode {
		envFile(t, "GOFLAGS=-mod=mod\n")
	}
	return outside
}

// unusedModules is the directory of every module of the layout's tree at root that
// no load uses, whose entries the scan need not hide: the fixture module, and the
// nested module nothing imports.
func (l layout) unusedModules(root string) []string {
	unused := []string{filepath.Join(root, filepath.FromSlash(fixtureDir))}
	if l.nested == 2 {
		unused = append(unused, filepath.Join(root, "sub"))
	}
	return unused
}

// tryGo runs the go command with args in dir as [runGo] does, and reports whether
// it succeeded: a layout the command refuses to vendor is drawn without a vendor
// tree.
func tryGo(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	cmd := deadline.Command(deadline.Context(t), "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -modcacherw")
	err := cmd.Run()
	deadline.Reap(cmd)
	return err == nil
}

// listedDirs is every package directory the toolchain lists, dependencies and test
// variants included, for each unit of top as its load runs it. The module and sum
// files a listing may rewrite are put back.
func listedDirs(t *testing.T, top *Topology) []string {
	t.Helper()
	for _, name := range []string{moduleFileName, "go.sum"} {
		path := filepath.Join(top.Root, name)
		if text, err := os.ReadFile(path); err == nil {
			defer writeBody(string(text))(t, path)
		} else {
			defer func() { _ = os.Remove(path) }()
		}
	}
	c := HostConfiguration()
	var dirs []string
	for _, unit := range top.Units {
		cmd := deadline.Command(deadline.Context(t), "go", "list", "-e", "-deps", "-test", "-json", "./...")
		cmd.Dir = unit.Dir
		env, err := loadEnv(&c, unit.Dir, unit.Workspace, toolchain{})
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		_ = cmd.Run() // a listing that fails still names the directories it read
		deadline.Reap(cmd)
		for decoder := json.NewDecoder(&out); ; {
			var p struct{ Dir string }
			if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("Setup: decode go list in %s: %v", unit.Dir, err)
			}
			if p.Dir != "" && !slices.Contains(dirs, p.Dir) {
				dirs = append(dirs, p.Dir)
			}
		}
	}
	return dirs
}

// treeDirs is every directory of the tree at root, root included, which the walk
// reaches without following a link.
func treeDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			dirs = append(dirs, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("Setup: walk %s: %v", root, err)
	}
	return dirs
}

// TestLoadHidesAPipeInEveryDirectoryTheToolchainLists draws layouts over the axes
// the go command decides what it reads by, with a pipe named as a Go file in every
// directory of the tree and behind a link out of it no import reads through.
// Within two minutes a draw, every pipe in the tree is hidden but in the modules no
// load uses ([layout.unusedModules]), and none in .git/objects or behind the link;
// the load ends as it does without them, and where it loads, every pipe in a
// directory the toolchain lists is named where code may hide ([program.6]).
func TestLoadHidesAPipeInEveryDirectoryTheToolchainLists(t *testing.T) {
	stableToolchain(t)
	for seed := range uint64(24) {
		l := drawLayout(seed)
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			deadline.Row(t, 2*time.Minute, "the toolchain read a pipe the run did not hide", func(t *testing.T) {
				root := writeTree(t, l.tree())
				outside := l.prepare(t, root)
				doc := scope.Document{Target: scope.Module{Path: root}}
				top, err := Discover(deadline.Context(t), doc)
				if err != nil {
					t.Fatalf("Discover(%+v) = _, %v, want no error", l, err)
				}
				c := HostConfiguration()
				_, without := Load(deadline.Context(t), top, &c)
				listed := listedDirs(t, top)
				var pipes, read []string
				for _, dir := range treeDirs(t, root) {
					pipe := filepath.Join(dir, "zz_hidden.go")
					sandbox.Fifo(t, pipe)
					pipes = append(pipes, pipe)
					if slices.Contains(listed, dir) {
						read = append(read, pipe)
					}
				}
				// The scan adds no path through a link out of every tree, so pipes go
				// behind it only where no import reads through it ([program.6]).
				var beyond []string
				if l.out == 1 {
					for _, dir := range treeDirs(t, outside) {
						sandbox.Fifo(t, filepath.Join(dir, "zz_hidden.go"))
						rest, _ := filepath.Rel(outside, dir)
						beyond = append(beyond, filepath.Join(root, "p", "o", rest, "zz_hidden.go"))
					}
				}
				top, err = Discover(deadline.Context(t), doc)
				if err != nil {
					t.Fatalf("Discover(%+v with a pipe in every directory) = _, %v, want no error", l, err)
				}
				unused, store := l.unusedModules(root), filepath.Join(root, ".git", "objects")
				for _, pipe := range pipes {
					dir := filepath.Dir(pipe)
					if slices.ContainsFunc(unused, func(m string) bool { return dir == m || scope.Below(m, dir) }) {
						continue
					}
					hidden := slices.ContainsFunc(top.hidden, func(h hiddenEntry) bool { return h.path == pipe })
					if want := dir != store && !scope.Below(store, dir); hidden != want {
						t.Errorf("Discover(%+v with a pipe in every directory) hid %v, want %s among them: %t", l, top.hidden, pipe, want)
					}
				}
				for _, pipe := range beyond {
					if slices.ContainsFunc(top.hidden, func(h hiddenEntry) bool { return h.path == pipe }) {
						t.Errorf("Discover(%+v with a pipe behind a link out of the tree) hid %s, want no path through that link", l, pipe)
					}
				}
				got, err := Load(deadline.Context(t), top, &c)
				t.Logf("%+v: %d pipes, %d in what the load lists, the load without them ending with %v", l, len(pipes), len(read), without)
				switch {
				case without != nil && err == nil, without == nil && err != nil:
					t.Fatalf("Load(%+v with a pipe in every directory) = _, %v, want what the load without them ended with, %v", l, err, without)
				case without != nil:
					return
				}
				for _, pipe := range read {
					if !slices.Contains(got.Hidden, pipe) {
						t.Errorf("Load(%+v with a pipe in every directory) hid %q where code may hide, want %s among them", l, got.Hidden, pipe)
					}
				}
			})
		})
	}
}

// TestDiscoverReadsNoDirectiveOfAModuleFileNoLoadUses pins, within a minute, that
// the scan reads the replace directives of no module file a load does not use
// ([program.6]): a testdata fixture whose module file replaces a module with the
// filesystem root leaves Discover within two seconds of its time without that
// directive, and hides nothing.
func TestDiscoverReadsNoDirectiveOfAModuleFileNoLoadUses(t *testing.T) {
	stableToolchain(t)
	deadline.Row(t, time.Minute, "the scan walked a directory a fixture's module file names", func(t *testing.T) {
		discovered := func(fixture string) time.Duration {
			root := writeTree(t, map[string]string{
				"go.mod":              "module example.com/a\n\ngo 1.22\n",
				"main.go":             "package main\n\nfunc main() {}\n",
				"testdata/old/go.mod": fixture,
			})
			start := time.Now()
			top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}})
			if err != nil {
				t.Fatalf("Discover(a module holding a fixture module file) = _, %v, want no error", err)
			}
			if len(top.hidden) != 0 {
				t.Errorf("Discover(a module holding a fixture module file) hid %v, want nothing", top.hidden)
			}
			return time.Since(start)
		}
		without := discovered("module example.com/old\n\ngo 1.22\n")
		if with := discovered("module example.com/old\n\ngo 1.22\n\nreplace example.com/x => /\n"); with > without+2*time.Second {
			t.Errorf("Discover(a module holding a fixture module file replacing a module with /) took %v, want within two seconds of its %v without the directive", with, without)
		}
	})
}

// TestDiscoverWalksEachDirectoryOnceHoweverLinksSpellIt pins, within a minute,
// that the scan memoises every directory by device and inode: a graph of symbolic
// links two wide and twenty deep, 2^20 paths to its last directory, inside the
// module and behind a link out of it, leaves Discover within two seconds of its
// time on the same directories without the links, and hides nothing. With a pipe
// in the last directory, the run hides it at its own path and through each of the
// two links to that directory, one link deep, and nothing behind the link out.
func TestDiscoverWalksEachDirectoryOnceHoweverLinksSpellIt(t *testing.T) {
	stableToolchain(t)
	deadline.Row(t, time.Minute, "the scan walked a directory once per path to it", func(t *testing.T) {
		const depth = 20
		discovered := func(links, pipe bool) (time.Duration, *Topology) {
			root := writeTree(t, map[string]string{
				"go.mod":  "module example.com/a\n\ngo 1.22\n",
				"main.go": "package main\n\nfunc main() {}\n",
			})
			outside := sandbox.Dir(t)
			for _, dag := range []string{filepath.Join(root, "dag"), outside} {
				for i := range depth + 1 {
					mkdir(t, filepath.Join(dag, fmt.Sprintf("L%d", i)))
					if links && i < depth {
						for j := range 2 {
							linkTo(fmt.Sprintf("../L%d", i+1))(t, filepath.Join(dag, fmt.Sprintf("L%d", i), fmt.Sprintf("s%d", j)))
						}
					}
				}
				writeBody("package z\n")(t, filepath.Join(dag, fmt.Sprintf("L%d", depth), "z.go"))
				if pipe {
					sandbox.Fifo(t, filepath.Join(dag, fmt.Sprintf("L%d", depth), "x.go"))
				}
			}
			linkTo(filepath.Join(outside, "L0"))(t, filepath.Join(root, "out"))
			start := time.Now()
			top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: root}})
			if err != nil {
				t.Fatalf("Discover(a module holding a graph of links) = _, %v, want no error", err)
			}
			return time.Since(start), top
		}
		without, _ := discovered(false, false)
		if with, top := discovered(true, false); with > without+2*time.Second || len(top.hidden) != 0 {
			t.Errorf("Discover(a module holding a graph of 2^%d paths) took %v and hid %v, want within two seconds of its %v without the links, hiding nothing",
				depth, with, top.hidden, without)
		}
		took, top := discovered(true, true)
		last, before := filepath.Join(top.Root, "dag", fmt.Sprintf("L%d", depth)), filepath.Join(top.Root, "dag", fmt.Sprintf("L%d", depth-1))
		want := []string{filepath.Join(last, "x.go"), filepath.Join(before, "s0", "x.go"), filepath.Join(before, "s1", "x.go")}
		slices.Sort(want)
		if got := hiddenPaths(top); took > without+2*time.Second || !slices.Equal(got, want) {
			t.Errorf("Discover(a module holding a graph of 2^%d paths to a pipe) took %v and hid %q, want within two seconds of %v and %q",
				depth, took, got, without, want)
		}
	})
}

// hiddenPaths is the path of every entry top hides, in path order.
func hiddenPaths(top *Topology) []string {
	paths := make([]string, 0, len(top.hidden))
	for _, h := range top.hidden {
		paths = append(paths, h.path)
	}
	return paths
}

// TestDiscoverHidesAnEntryThroughOneLinkInTheTreesAndNoFurther pins, within a
// minute, the paths the scan hides an entry at ([program.6]): its own, and its path
// through each symbolic link in the trees it walks to a directory there that holds
// it, one link deep. A pipe reached only through a link out of every tree, even
// one to the tree's parent, or through two links in a row, gets no path: the
// residual program.6 states, where the toolchain blocks on it as with no run.
func TestDiscoverHidesAnEntryThroughOneLinkInTheTreesAndNoFurther(t *testing.T) {
	stableToolchain(t)
	deadline.Row(t, time.Minute, "the scan opened a pipe", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"m/go.mod":  "module example.com/m\n\ngo 1.22\n",
			"m/main.go": "package main\n\nfunc main() {}\n",
			"ext/q.go":  "package ext\n",
		})
		m := filepath.Join(root, "m")
		for link, to := range map[string]string{"m/a": "b", "m/b/c": "../d", "m/d/e/up": "..", "m/up": "..", "m/shared": "../ext"} {
			path := filepath.Join(root, filepath.FromSlash(link))
			mkdir(t, filepath.Dir(path))
			linkTo(to)(t, path)
		}
		sandbox.Fifo(t, filepath.Join(m, "d", "x.go"))
		sandbox.Fifo(t, filepath.Join(root, "ext", "x.go"))
		top, err := Discover(deadline.Context(t), scope.Document{Target: scope.Module{Path: m}})
		if err != nil {
			t.Fatalf("Discover(a module holding links) = _, %v, want no error", err)
		}
		want := []string{filepath.Join(m, "b", "c", "x.go"), filepath.Join(m, "d", "e", "up", "x.go"), filepath.Join(m, "d", "x.go")}
		if got := hiddenPaths(top); !slices.Equal(got, want) {
			t.Errorf("Discover(a module holding links) hid %q, want %q: none two links deep (m/a/c/x.go) or through a link out of every tree (m/up/m/d/x.go, m/shared/x.go)", got, want)
		}
	})
}

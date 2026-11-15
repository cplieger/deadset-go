//go:build unix

package load

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// treeKinds is every kind of directory the walk of a module's tree meets, as
// [TestTheWalkDecidesEveryDirectoryKindAtEveryReach] makes them: one holding a
// regular Go file, one holding beside it a pipe only another platform's build
// opens, one the run cannot stat, one it cannot list, a module root, and one on
// another device.
var treeKinds = []string{"gofile", "pipe", "unstatable", "unlistable", "module", "mount"}

// The walk gives every directory it meets its one outcome, for every kind of
// directory, reached as a directory entry or through a link, in a module inside
// the root or outside it, a declared consumer's ([treeWalk.outcome]): a directory
// holding a Go file is recorded, with each Go-named entry that is no regular file;
// one the run cannot stat or list is unread inside the root and unlistable outside
// it; a module root is handed on, however it is reached; and another device bounds
// a directory a link reaches only. A strict ancestor through a link is unbounded.
// The file system fails a stat or a listing, or names a device, through a seam.
func TestTheWalkDecidesEveryDirectoryKindAtEveryReach(t *testing.T) {
	for _, placement := range []string{"inside", "outside"} {
		for _, reach := range []string{"plain", "link"} {
			for _, kind := range treeKinds {
				t.Run(placement+"/"+reach+"/"+kind, func(t *testing.T) {
					root, app, path := walkFixture(t, reach, kind)
					target := app
					if placement == "outside" {
						target = filepath.Join(root, "elsewhere")
					}
					top := &Topology{Root: target, root: scope.RootOf(target)}
					tree, err := top.goTreeWith(t.Context(), app, app, seamFS())
					if err != nil {
						t.Fatalf("goTreeWith() = _, %v, want no error", err)
					}
					if !walkOutcomeHolds(tree, top, placement == "inside", reach, kind, path) {
						t.Errorf("the walk of %s reached %s %s the root gave tree %+v and unread %v, want its one outcome", kind, reach, placement, tree, top.Unread)
					}
				})
			}
		}
	}
	t.Run("link to an ancestor", func(t *testing.T) {
		_, app, _ := walkFixture(t, "plain", "gofile")
		if err := sandbox.Symlink("..", filepath.Join(app, "up")); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		top := &Topology{Root: app, root: scope.RootOf(app)}
		tree, err := top.goTreeWith(t.Context(), app, app, seamFS())
		if err != nil || !leftOutAs(tree.unloaded, filepath.Join(app, "up"), LeftOutUnbounded) {
			t.Errorf("goTreeWith(a link to the module's parent) = %+v, %v, want the link unbounded", tree, err)
		}
	})
}

// The walk records a directory once, under the first path that builds it, or not
// at all where no path builds it, by the go command's vendor rule
// ([vendorSpelling]): the module root's vendor directory itself builds, plain or
// as a link of that name, a directory strictly below it builds no package of the
// module by that path (at most a vendored dependency's, under -mod=vendor), and
// the same directory by any other path builds, met before the vendored path or
// after it. A link to an ancestor is left out only where a building path meets it.
func TestTheWalkRecordsADirectoryUnderTheFirstPathThatBuildsIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string          // the Go files below the fixture, app's main.go besides
		links map[string]string // each link below the fixture and where it leads
		want  []string          // the pattern of every directory recorded
		left  []string          // the path below app of every directory left out
	}{
		{name: "the vendor directory", files: []string{"app/vendor/v.go"}, want: []string{".", "./vendor"}},
		{name: "a link named vendor out of the module", files: []string{"out/v.go"}, links: map[string]string{"app/vendor": "../out"}, want: []string{".", "./vendor"}},
		{name: "a directory below the vendor directory", files: []string{"app/vendor/p/p.go"}, want: []string{"."}},
		{name: "a link below the vendor directory to one walked before", files: []string{"app/a/p.go"}, links: map[string]string{"app/vendor/l": "../a"}, want: []string{".", "./a"}},
		{name: "a link below the vendor directory to one walked after", files: []string{"app/w/p.go"}, links: map[string]string{"app/vendor/l": "../w"}, want: []string{".", "./w"}},
		{name: "a link to the vendor directory walked after it", files: []string{"app/vendor/e/v/v.go"}, links: map[string]string{"app/x": "vendor"}, want: []string{".", "./x/e/v"}},
		{name: "a link to the vendor directory walked before it", files: []string{"app/vendor/e/v/v.go"}, links: map[string]string{"app/a": "vendor"}, want: []string{".", "./a/e/v"}},
		{name: "a link from the vendor directory into itself", files: []string{"app/vendor/e/v/v.go"}, links: map[string]string{"app/vendor/f": "e"}, want: []string{"."}},
		{name: "a link below the vendor directory to an ancestor", links: map[string]string{"app/vendor/up": "../.."}, want: []string{"."}},
		{name: "a link to an ancestor below a directory a vendored path meets first", files: []string{"app/w/p.go"}, links: map[string]string{"app/vendor/l": "../w", "app/w/up": "../.."}, want: []string{".", "./w"}, left: []string{"w/up"}},
		{name: "a link to an ancestor below the vendor directory a link to it reaches", links: map[string]string{"app/x": "vendor", "app/vendor/up": "../.."}, want: []string{"."}, left: []string{"x/up"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"app/go.mod": "module example.com/app\n\ngo 1.27.1\n", "app/main.go": "package main\n\nfunc main() {}\n", "app/vendor/README": ""}
			for _, file := range tc.files {
				files[file] = "package p\n"
			}
			if _, linked := tc.links["app/vendor"]; linked {
				delete(files, "app/vendor/README")
			}
			root := writeTree(t, files)
			for link, to := range tc.links {
				if err := sandbox.Symlink(to, filepath.Join(root, filepath.FromSlash(link))); err != nil {
					t.Fatalf("Setup: %v", err)
				}
			}
			app := filepath.Join(root, "app")
			top := &Topology{Root: app, root: scope.RootOf(app)}
			tree, err := top.goTreeWith(t.Context(), app, app, hostFS)
			var got, left []string
			for _, d := range tree.dirs {
				got = append(got, d.pattern)
			}
			for _, d := range tree.unloaded {
				left = append(left, filepath.ToSlash(mustRel(t, app, d.Dir)))
			}
			if slices.Sort(got); err != nil || !slices.Equal(got, tc.want) || !slices.Equal(left, tc.left) {
				t.Errorf("goTreeWith() recorded %v and left out %v (%v), want %v and %v left out", got, left, err, tc.want, tc.left)
			}
		})
	}
}

// A link the listing gives as no directory, whose Stat fails otherwise than for a
// link to nothing, through a file or that loops, is a directory the walk cannot
// stat, as every walk classifies the error ([statKind]): unread inside the root,
// and unlistable outside it, below a directory ./... skips or not.
func TestTheWalkCannotStatALinkStatFailsOn(t *testing.T) {
	for _, placement := range []string{"inside", "outside"} {
		for _, below := range []string{".", "testdata"} {
			t.Run(placement+"/"+below, func(t *testing.T) {
				root := writeTree(t, map[string]string{"app/go.mod": "module example.com/app\n\ngo 1.27.1\n", "app/testdata/p.go": "package p\n", "elsewhere/x.txt": "", "dest/x.txt": ""})
				app := filepath.Join(root, "app")
				path := filepath.Join(app, below, "eio")
				if err := sandbox.Symlink(filepath.Join(root, "dest"), path); err != nil {
					t.Fatalf("Setup: %v", err)
				}
				target := app
				if placement == "outside" {
					target = filepath.Join(root, "elsewhere")
				}
				top := &Topology{Root: target, root: scope.RootOf(target)}
				tree, err := top.goTreeWith(t.Context(), app, app, seamFS())
				held := placement == "inside" && slices.Equal(top.Unread, []string{path}) && len(tree.unloaded) == 0 ||
					placement == "outside" && len(top.Unread) == 0 && leftOutAs(tree.unloaded, path, LeftOutUnlistable)
				if err != nil || !held {
					t.Errorf("goTreeWith(a link Stat fails on with EIO) = %+v, %v with unread %v, want it unread inside the root and unlistable outside", tree, err, top.Unread)
				}
			})
		}
	}
}

// walkOutcomeHolds reports whether tree and top's unread entries hold the one
// outcome of the directory of kind at path, reached as reach says, inside the root
// or not.
func walkOutcomeHolds(tree goTree, top *Topology, inside bool, reach, kind, path string) bool {
	at := slices.IndexFunc(tree.dirs, func(d treeDir) bool { return d.pattern == "./"+kind })
	switch kind {
	case "gofile":
		return at >= 0 && len(tree.unloaded) == 0
	case "pipe":
		return at >= 0 && slices.Equal(tree.dirs[at].irregular, []string{"x_windows.go"})
	case "unstatable", "unlistable":
		return at < 0 && (inside && slices.Contains(top.Unread, path) && len(tree.unloaded) == 0 ||
			!inside && len(top.Unread) == 0 && leftOutAs(tree.unloaded, path, LeftOutUnlistable))
	case "module":
		return at < 0 && slices.ContainsFunc(tree.roots, func(r linkedRoot) bool { return r.at == path })
	case "mount":
		return reach == "plain" && at >= 0 && len(tree.unloaded) == 0 ||
			reach == "link" && at < 0 && leftOutAs(tree.unloaded, path, LeftOutUnbounded)
	}
	return false
}

// walkFixture is a fixture root holding a module at app and the directory of
// kind, reached as reach says, at path below app: a link's directory sits below
// another module's root, where no walk of app's tree reaches it but the link.
func walkFixture(t *testing.T, reach, kind string) (root, app, path string) {
	t.Helper()
	files := map[string]string{
		"app/go.mod":       "module example.com/app\n\ngo 1.27.1\n",
		"app/main.go":      "package main\n\nfunc main() {}\n",
		"app/other/go.mod": "module example.com/other\n\ngo 1.27.1\n",
		"elsewhere/x.txt":  "",
	}
	at := map[string]string{"plain": "app/", "link": "app/other/"}[reach] + kind
	files[at+"/p.go"] = "package p\n"
	if kind == "module" {
		files[at+"/go.mod"] = "module example.com/m\n\ngo 1.27.1\n"
	}
	root = writeTree(t, files)
	app = filepath.Join(root, "app")
	path = filepath.Join(app, kind)
	if kind == "pipe" {
		sandbox.Fifo(t, filepath.Join(root, filepath.FromSlash(at), "x_windows.go"))
	}
	if reach == "link" {
		if err := sandbox.Symlink(filepath.Join(root, filepath.FromSlash(at)), path); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	return root, app, path
}

// seamFS is the host's file system, but that Stat fails for permission on a
// directory named unstatable and with EIO on an entry named eio, a listing fails
// on one named unlistable, wherever links lead, and a directory named mount is on
// a device of its own.
func seamFS() treeFS {
	named := func(path, name string) bool { return filepath.Base(path) == name }
	return treeFS{
		stat: func(path string) (fs.FileInfo, error) {
			if named(path, "unstatable") {
				return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrPermission}
			}
			if named(path, "eio") {
				return nil, &fs.PathError{Op: "stat", Path: path, Err: syscall.EIO}
			}
			return os.Stat(path)
		},
		readDir: func(path string) ([]fs.DirEntry, error) {
			if named(path, "unlistable") {
				return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
			}
			return os.ReadDir(path)
		},
		deviceOf: func(path string) any {
			if named(path, "mount") {
				return "another"
			}
			return deviceOf(path)
		},
	}
}

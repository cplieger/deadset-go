//go:build windows

package load

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// A directory junction out of the root, which Go 1.23 no longer reports as a link
// and filepath.EvalSymlinks no longer evaluates, is in the module's tree all the
// same ([goTree]): a build naming it compiles the program it leads to, so the load
// leaves that program out, with the target package it imports.
func TestTheLoadLeavesOutAProgramThroughAJunctionOutOfTheRoot(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"app/go.mod":       "module example.com/app\n\ngo 1.27.1\n",
		"app/main.go":      "package main\n\nfunc main() {}\n",
		"app/core/core.go": "// Package core is the target's.\npackage core\n\n// F is called only through the junction.\nfunc F() {}\n",
		"out/main.go":      "package main\n\nimport \"example.com/app/core\"\n\nfunc main() { core.F() }\n",
	})
	app := filepath.Join(root, "app")
	if err := sandbox.Junction(t.Context(), filepath.Join(root, "out"), filepath.Join(app, "tool")); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	top, err := Discover(t.Context(), scope.Document{Target: scope.Module{Path: app}})
	if err != nil {
		t.Fatalf("Discover(a module with a junction out of its root) = _, %v, want no error", err)
	}
	c := HostConfiguration()
	got, err := Load(t.Context(), top, &c)
	if err != nil {
		t.Fatalf("Load(a module with a junction out of its root) = _, %v, want no error", err)
	}
	left := slices.IndexFunc(got.LeftOut, func(p LeftOutPackage) bool { return p.Path == "example.com/app/tool" })
	if left < 0 || len(got.LeftOut[left].Imports) != 1 || got.LeftOut[left].Imports[0].Path != "example.com/app/core" {
		t.Errorf("Load().LeftOut = %+v, want example.com/app/tool, importing example.com/app/core, through the junction", got.LeftOut)
	}
}

// Another device bounds a directory a junction leads to and no plain directory,
// which ./... reads below ([treeWalk.outcome]): the junction is left out
// unbounded, and the plain directory is walked. The device is named through a
// seam, which the host cannot mount.
func TestAJunctionToAnotherDeviceIsUnboundedAndAPlainDirectoryThereIsWalked(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app/go.mod":     "module example.com/app\n\ngo 1.27.1\n",
		"app/mount/p.go": "package p\n",
		"out/mount/p.go": "package p\n",
		"app/main.go":    "package main\n\nfunc main() {}\n",
	})
	app := filepath.Join(root, "app")
	link := filepath.Join(app, "j", "mount")
	if err := sandbox.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Junction(t.Context(), filepath.Join(root, "out", "mount"), link); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	fsys := hostFS
	fsys.deviceOf = func(path string) any {
		if filepath.Base(path) == "mount" {
			return "another"
		}
		return deviceOf(path)
	}
	top := &Topology{Root: app, root: scope.RootOf(app)}
	tree, err := top.goTreeWith(t.Context(), app, app, fsys)
	if err != nil {
		t.Fatalf("goTreeWith() = _, %v, want no error", err)
	}
	walked := slices.ContainsFunc(tree.dirs, func(d treeDir) bool { return d.pattern == "./mount" })
	unbounded := slices.ContainsFunc(tree.unloaded, func(p LeftOutPackage) bool { return p.Dir == link && p.Reason == LeftOutUnbounded })
	if !walked || !unbounded || len(tree.unloaded) != 1 {
		t.Errorf("goTreeWith(a plain directory and a junction on another device) = %+v, want ./mount walked and only the junction unbounded", tree)
	}
}

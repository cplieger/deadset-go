// Package matrix derives a build matrix from a target tree: the platform and tag
// atoms its files name, plus the configuration of the host.
//
// Derivation never enumerates tag combinations. It emits one configuration per
// distinct operating-system atom, one per architecture atom, one per tag, and
// the host's own, so a tree naming three systems and two tags derives five
// configurations beside the host rather than the twelve their combinations would
// give.
//
// A derived matrix is incomplete by definition: the atoms satisfy a Boolean
// constraint only where they happen to, no configuration being invented to
// satisfy one, and a configuration no file in the tree names is never derived.
// The completeness of a matrix is the configuration document's assertion
// (analysis.matrix.complete), which derivation neither reads nor sets.
package matrix

import (
	"fmt"
	"go/build/constraint"
	"go/parser"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// idSeparator joins the parts of a configuration identifier.
const idSeparator = "-"

// atomSet is the build atoms one target tree names, each list sorted and without
// repetition. A tag the toolchain answers from its own environment rather than
// from a configuration's tag list is no atom and appears in none of them.
type atomSet struct {
	OS   []string // operating-system names
	Arch []string // architecture names
	Tags []string // every other tag a build expression or a file name names
}

// Derive reads the tree of every unit of top and returns the matrix their build
// constraints and the host configuration imply: the host first, then one
// configuration per operating-system atom, one per architecture atom and one per
// tag, deduplicated and in that order, each but the host's [load.Derived].
//
// A file whose build constraint does not parse, or which declares more than one,
// ends derivation with that file named: the toolchain refuses such a file too, so
// deriving a matrix from the rest would describe a tree that does not build.
func Derive(top *load.Topology) ([]load.Configuration, error) {
	host := load.HostConfiguration()
	return derive(top, &host)
}

// derive is Derive with the host configuration supplied, which is what lets a
// test state the identifiers it expects rather than compute them.
func derive(top *load.Topology, host *load.Configuration) ([]load.Configuration, error) {
	var files []constraint.Expr
	for i := range top.Units {
		unit := &top.Units[i]
		err := collect(top, unit.Dir, unit.Path, unit.File, func(_ string, expression constraint.Expr) {
			files = append(files, expression)
		})
		if err != nil {
			return nil, err
		}
	}
	return configurationsOf(host, atomsOf(files)), nil
}

// collector reads the Go files of one unit's module tree.
type collector struct {
	top    *load.Topology
	visit  func(path string, expression constraint.Expr)
	queued map[string]bool
	unit   scope.Root
	tree   load.ModuleTree
	module string
	queue  []string
}

// collect hands visit the path and build constraint, nil where none decides it, of
// every Go file of the tree of module file f below the directory unit of module, in
// walk order, then of every non-test file of each package outside the unit's
// packages ([load.ModuleTree.OutsideDir]) that a non-test file it read imports,
// under any constraint but the ignore tag alone. A refusal names the file relative
// to top's root. Reading the tree rather than a loaded package set sees a
// directory whose every file one configuration excludes, for which a load reports
// nothing.
func collect(top *load.Topology, unit, module string, f *load.ModuleFile, visit func(path string, expression constraint.Expr)) error {
	tree := load.TreeOf(f)
	c := &collector{top: top, unit: scope.RootOf(unit), module: module, tree: tree, visit: visit, queued: make(map[string]bool)}
	enters := func(dir string) bool { return !tree.Skips(dir) && !load.IsModuleRoot(dir) }
	walk := func(path string, kind load.Kind) error {
		if kind != load.File || !buildconstraint.IsGoFile(filepath.Base(path)) {
			return nil
		}
		return c.read(path)
	}
	if err := top.Walk(unit, enters, walk); err != nil {
		return fmt.Errorf("read the build constraints under %s: %w", unit, err)
	}
	for len(c.queue) > 0 {
		dir := c.queue[0]
		c.queue = c.queue[1:]
		if err := c.readPackage(dir); err != nil {
			return fmt.Errorf("read the build constraints under %s: %w", dir, err)
		}
	}
	return nil
}

// readPackage reads every Go file of the directory dir other than a test file. A
// path that holds no directory holds none: the load reports the import.
func (c *collector) readPackage(dir string) error {
	return c.top.Walk(dir, func(string) bool { return false }, func(path string, kind load.Kind) error {
		name := filepath.Base(path)
		if scope.SameName(path, dir) || kind != load.File || !buildconstraint.IsGoFile(name) || isTestFile(name) {
			return nil
		}
		return c.read(path)
	})
}

// read records the build constraint of the regular Go file at path and queues each
// package outside the unit's packages it imports. A file whose imports do not
// parse queues what parsed: the load reports that file. A file it cannot read is
// recorded as unread and left out.
func (c *collector) read(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		c.top.AddUnread(path)
		return nil
	}
	expression, err := buildconstraint.Of(filepath.Base(path), content)
	if err != nil {
		return fmt.Errorf("%s: %w", c.top.Rel(path), err)
	}
	c.visit(path, expression)
	if isTestFile(path) || buildconstraint.OnlyUnderIgnore(expression) {
		return nil
	}
	parsed, _ := scope.ParseText(content, parser.ImportsOnly)
	if parsed == nil {
		return nil
	}
	for _, spec := range parsed.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if dir, outside := c.tree.OutsideDir(c.module, c.unit, imported); outside && !c.queued[dir] {
			c.queued[dir] = true
			c.queue = append(c.queue, dir)
		}
	}
	return nil
}

// isTestFile reports whether the Go file at path is a test file, which only the
// test binary of its own package compiles.
func isTestFile(path string) bool { return strings.HasSuffix(path, "_test.go") }

// atomsOf classifies every tag the files name onto the axis it belongs to. A test
// file's tags count as any file's. A file built only under the ignore tag names no
// atom, and the ignore tag is never one, so no derived configuration sets it.
func atomsOf(files []constraint.Expr) atomSet {
	named := make(map[string]bool)
	for _, expression := range files {
		if expression == nil || buildconstraint.OnlyUnderIgnore(expression) {
			continue
		}
		for tag := range buildconstraint.Tags(expression) {
			named[tag] = true
		}
	}
	delete(named, buildconstraint.IgnoreTag)

	var atoms atomSet
	for name := range named {
		switch {
		case buildconstraint.IsOS(name):
			atoms.OS = append(atoms.OS, name)
		case buildconstraint.IsArch(name):
			atoms.Arch = append(atoms.Arch, name)
		case buildconstraint.IsToolchainTag(name):
		default:
			atoms.Tags = append(atoms.Tags, name)
		}
	}
	slices.Sort(atoms.OS)
	slices.Sort(atoms.Arch)
	slices.Sort(atoms.Tags)
	return atoms
}

// configurationsOf returns the matrix one atom set and one host imply: the host,
// then one configuration per atom of each axis, deduplicated by identifier. A
// platform atom pairs with the host's other axis where the toolchain builds that
// pair, else with a pair it builds, and an atom it builds nothing for derives
// nothing. Whether the target builds a pair only its load answers, so every
// configuration but the host's is [load.Derived]; an atom pairing to the host's own
// identifier keeps the host's entry.
func configurationsOf(host *load.Configuration, atoms atomSet) []load.Configuration {
	configurations := []load.Configuration{*host}
	named := map[string]bool{host.ID: true}
	add := func(c load.Configuration) {
		if !named[c.ID] {
			named[c.ID] = true
			configurations = append(configurations, c)
		}
	}

	for _, system := range atoms.OS {
		if arch, built := partner(buildconstraint.ArchitecturesOf(system), host.Arch); built {
			add(configurationFor(system, arch, nil))
		}
	}
	for _, arch := range atoms.Arch {
		if system, built := partner(buildconstraint.SystemsOf(arch), host.OS); built {
			add(configurationFor(system, arch, nil))
		}
	}
	for _, tag := range atoms.Tags {
		add(configurationFor(host.OS, host.Arch, []string{tag}))
	}
	return configurations
}

// configurationFor names one derived configuration the way an entry of the
// configured matrix names itself: the platform, then every tag in effect.
func configurationFor(system, arch string, tags []string) load.Configuration {
	id := system + idSeparator + arch
	if len(tags) > 0 {
		id += idSeparator + strings.Join(tags, idSeparator)
	}
	return load.Configuration{ID: id, OS: system, Arch: arch, Tags: tags, Origin: load.Derived}
}

// partner picks preferred out of candidates, falling back to the first of them,
// and reports false when there are none.
func partner(candidates []string, preferred string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	if slices.Contains(candidates, preferred) {
		return preferred, true
	}
	return candidates[0], true
}

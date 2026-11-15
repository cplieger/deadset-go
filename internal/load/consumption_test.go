package load

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// readsOfRequiresAndReplaces names every function of the module's production code
// that reads the require or replace list of a module file or a workspace file, or
// a module's replacement, with what it decides, none of which is whether a module
// builds against a unit: that is [Topology.consumes]'s answer alone, from the go
// command's module graph.
var readsOfRequiresAndReplaces = map[string]string{
	"internal/deps/deps.go Directives":          "the directives the requirement kinds report on",
	"internal/load/load.go expectedModules":     "the modules a failed load's setup failure may name",
	"internal/load/hide.go module":              "the trees the scan for pipes and devices walks",
	"internal/scope/workspace.go ReadWorkspace": "the trees the scan for pipes and devices walks",
	"internal/load/error.go cachedModule":       "the module cache entry a load error names",
	"internal/run/loads.go packageDigestOf":     "the digest that tells two loads of a package apart",
	"internal/load/consumption.go builtFrom":    "what a module of the graph builds from: the answer itself",
	"internal/load/consumption.go addBuilt":     "which units the graph builds from their directories: the answer itself",
}

// No function outside [readsOfRequiresAndReplaces] reads a require or replace
// list or a replacement, so no site decides whether a module builds against the
// target from the paths its module file names ([program.7], [program.8]).
func TestOnlyTheModuleGraphDecidesWhatAModuleBuildsAgainst(t *testing.T) {
	t.Parallel()
	module := moduleRootOf(t)
	err := filepath.WalkDir(module, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir() && path != module && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata"):
			return fs.SkipDir
		case entry.IsDir() || filepath.Ext(path) != goFileExt || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		rel := filepath.ToSlash(mustRelPath(t, module, path))
		for _, fn := range readsSelectors(t, path) {
			if _, named := readsOfRequiresAndReplaces[rel+" "+fn]; !named {
				t.Errorf("%s: %s reads a require or replace list or a replacement; ask Topology.consumes what the module builds against, or name what it decides in readsOfRequiresAndReplaces", rel, fn)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walk %s: %v", module, err)
	}
}

// readsSelectors is the name of every function of the file at path that reads a
// field named Require or Replace.
func readsSelectors(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		called := make(map[ast.Expr]bool)
		reads := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				called[n.Fun] = true
			case *ast.SelectorExpr:
				reads = reads || !called[n] && (n.Sel.Name == "Require" || n.Sel.Name == "Replace")
			}
			return true
		})
		if reads {
			names = append(names, fn.Name.Name)
		}
	}
	return names
}

// moduleRootOf is the directory of the module holding this package.
func moduleRootOf(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for !IsModuleRoot(dir) {
		if filepath.Dir(dir) == dir {
			t.Fatal("Setup: no module file above the package")
		}
		dir = filepath.Dir(dir)
	}
	return dir
}

// mustRelPath is path relative to base.
func mustRelPath(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatalf("Setup: %s relative to %s: %v", path, base, err)
	}
	return rel
}

// consumptionAnswers is every answer the go command's module graph gives of one
// module, by name.
var consumptionAnswers = map[string]answer{
	"none":    {},
	"units":   {consumption: consumption{units: []string{"example.com/app"}}},
	"renamed": {consumption: consumption{renamed: []string{"example.com/app"}}},
	"unknown": {unknown: true},
	"dropped": {unknown: true, dropped: true},
}

// A module whose graph the go command cannot list may consume the target, and one
// building a unit under another path counts no reference to it, whatever role the
// module plays, so each is left out ([program.9]); a module below the target or a
// reader building a unit under its own path loads, and a module root a link
// reaches doing so, which nothing loads, is left out. A module the run drops is
// left out by its dropped note alone.
func TestTheConsumptionRuleFailsSafeForEveryRole(t *testing.T) {
	t.Parallel()
	roles := map[moduleRole]string{roleUnit: "unit", roleNested: "nested", roleReader: "reader", roleConsumer: "consumer", roleProbe: "probe"}
	want := map[string]verdict{
		"unit none": {load: true}, "unit units": {load: true},
		"unit renamed": {load: true, left: []LeftOutReason{LeftOutRenamed}}, "unit unknown": {load: true, left: []LeftOutReason{LeftOutUnanswered}},
		"nested none": {}, "nested units": {load: true},
		"nested renamed": {left: []LeftOutReason{LeftOutRenamed}}, "nested unknown": {load: true, left: []LeftOutReason{LeftOutUnanswered}},
		"reader none": {}, "reader units": {load: true},
		"reader renamed": {left: []LeftOutReason{LeftOutRenamed}}, "reader unknown": {load: true, left: []LeftOutReason{LeftOutUnanswered}},
		"consumer none": {load: true}, "consumer units": {load: true},
		"consumer renamed": {load: true, left: []LeftOutReason{LeftOutRenamed}}, "consumer unknown": {load: true, left: []LeftOutReason{LeftOutUnanswered}},
		"probe none": {}, "probe units": {left: []LeftOutReason{LeftOutConsumer}},
		"probe renamed": {left: []LeftOutReason{LeftOutRenamed}}, "probe unknown": {left: []LeftOutReason{LeftOutUnanswered}},
		"unit dropped": {load: true}, "nested dropped": {load: true}, "reader dropped": {load: true},
		"consumer dropped": {load: true}, "probe dropped": {},
	}
	for role, roleName := range roles {
		for name, got := range consumptionAnswers {
			v := consumptionRule(role, got)
			if w := want[roleName+" "+name]; v.load != w.load || !slices.Equal(v.left, w.left) {
				t.Errorf("consumptionRule(%s, %s) = %+v, want %+v", roleName, name, v, w)
			}
		}
	}
}

// Every load of the run applies [consumptionRule] to the answer of each module it
// may load, whatever its role: what it leaves out, at the module's directory or a
// probe's link, whether a nested module or a reader stays, and that an unknown
// answer reaches the module, which a dropped unit counts. A module whose module
// file the run cannot read is one it drops, but for a probe, which nothing loads.
// No role skips the rule.
func TestEveryLoadOfTheRunAppliesTheConsumptionRule(t *testing.T) {
	t.Parallel()
	for role := roleUnit; role <= roleProbe; role++ {
		for name, got := range consumptionAnswers {
			top := &Topology{ConsumerWorkspace: workspaceOff}
			module := []discoveredModule{{Dir: "/m", linkedAt: "/link", Path: "example.com/m", Workspace: workspaceOff, File: &ModuleFile{}}}
			if got.dropped {
				module[0].File, module[0].FileErr = nil, errModuleFile
				got.dropped = role != roleProbe
			}
			held := map[moduleRole]*[]discoveredModule{roleUnit: &top.Units, roleNested: &top.Nested, roleReader: &top.Readers, roleConsumer: &top.Consumers, roleProbe: &top.probes}[role]
			*held = module
			asked := got
			asked.dropped = false
			a := &asker{ctx: t.Context(), top: top, answerOf: func(*discoveredModule, string) answer { return asked }}
			decide := a.decideLoaded
			if role == roleProbe {
				decide = a.decideProbes
			}
			if err := decide(); err != nil {
				t.Fatalf("role %d, answer %s: decide = %v", role, name, err)
			}
			v := consumptionRule(role, got)
			at := "/m"
			if role == roleProbe {
				at = "/link"
			}
			var left []LeftOutReason
			for _, out := range top.unloaded {
				if out.Dir != at {
					t.Errorf("role %d, answer %s: left out %s, want %s", role, name, out.Dir, at)
				}
				left = append(left, out.Reason)
			}
			if !slices.Equal(left, v.left) {
				t.Errorf("role %d, answer %s: left out %v, want the rule's %v", role, name, left, v.left)
			}
			if stays := len(*held) == 1; (role == roleNested || role == roleReader) && stays != v.load {
				t.Errorf("role %d, answer %s: kept = %v, want the rule's %v", role, name, stays, v.load)
			}
			if len(*held) == 1 && (*held)[0].buildsUnknown != got.unknown {
				t.Errorf("role %d, answer %s: buildsUnknown = %v, want %v", role, name, (*held)[0].buildsUnknown, got.unknown)
			}
		}
	}
}

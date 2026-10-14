package load

import (
	"go/types"
	"slices"
	"testing"
)

// A dropped nested module whose directory holds several files declaring main is a
// set of scripts, each checked alone against the target's packages, so what each
// names resolves to the target's own declarations. A lone main of a dropped module,
// a test file and a file of a nested module of its own are no script.
func TestLoadChecksTheScriptsOfADroppedModuleEachAlone(t *testing.T) {
	stableToolchain(t)
	root := writeTree(t, map[string]string{
		"go.mod":               "module example.com/b\n\ngo 1.27.1\n",
		"b.go":                 "package b\n\nfunc ByFirst() {}\n\nfunc BySecond() {}\n\nfunc ByLone() {}\n\nfunc ByTest() {}\n\nfunc ByInner() {}\n",
		"scripts/go.mod":       "module example.com/b/scripts\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../\n",
		"scripts/first.go":     "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.ByFirst() }\n",
		"scripts/second.go":    "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.BySecond() }\n",
		"scripts/x_test.go":    "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.ByTest() }\n",
		"scripts/inner/go.mod": "module example.com/b/scripts/inner\n\ngo 1.27.1\n",
		"scripts/inner/a.go":   "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.ByInner() }\n",
		"scripts/inner/b.go":   "package main\n\nfunc main() {}\n",
		"lone/go.mod":          "module example.com/b/lone\n\ngo 1.27.1\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../\n",
		"lone/main.go":         "package main\n\nimport \"example.com/b\"\n\nfunc main() { b.ByLone(); undefined() }\n",
	})

	got, err := loadTree(t, root, ".")
	if err != nil {
		t.Fatalf("Load(a module with a nested script module) = _, %v, want no error", err)
	}
	if len(got.Dropped) < 2 {
		t.Fatalf("Load() dropped %v, want the script module and the lone module", got.Dropped)
	}
	var target *types.Package
	for _, p := range got.Packages {
		if p.PkgPath == "example.com/b" && p.Types != nil {
			target = p.Types
		}
	}
	if target == nil {
		t.Fatal("Load() holds no package example.com/b")
	}
	var named []string
	for _, program := range got.Programs {
		for _, obj := range program.Info.Uses {
			if obj.Pkg() == target && target.Scope().Lookup(obj.Name()) == obj {
				named = append(named, obj.Name())
			}
		}
	}
	slices.Sort(named)
	if want := []string{"ByFirst", "BySecond"}; !slices.Equal(named, want) {
		t.Errorf("Load() programs name %v of the target, want %v", named, want)
	}
}

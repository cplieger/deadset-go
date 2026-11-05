package main

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestAPackageIsOneContainerWhateverFileAConfigurationAnchorsItAt(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		ref     string
		code    vocab.Code
		want    vocab.Class
	}{
		{"package-anchored-at-a-per-platform-file", "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Possible},
		{"package-anchored-at-a-shared-file", "go://example.com/app#helper", vocab.UnusedUnexported, vocab.Possible},
		{"const-type-of-a-package-anchored-at-a-per-platform-file", "go://example.com/app#mode", vocab.UnusedUnexported, vocab.Possible},
		{"const-type-of-a-package-anchored-at-a-per-platform-file", "go://example.com/app#modeA", vocab.UnusedUnexported, vocab.Possible},
		{"const-type-of-a-package-anchored-at-a-per-platform-file", "go://example.com/app#counter", vocab.WriteOnlySymbol, vocab.Certain},
	} {
		t.Run(tc.fixture+"/"+tc.ref, func(t *testing.T) {
			wantFinding(t, findingsAbout(t, archiveModule(t, "findings-ledger/"+tc.fixture+".txtar")), tc.ref, tc.code, tc.want)
		})
	}
}

// matrixWorld is one package whose files the two configurations compile in
// different combinations.
type matrixWorld struct {
	name  string
	files map[string]string
}

// matrixWorlds varies which file anchors the package (the first-sorting one), where
// a name literal's subjects are written, where a method's receiver type is, and
// whether an imported package has shared files, per-platform files only or both.
func matrixWorlds() []matrixWorld {
	anchors := map[string]map[string]string{
		"anchor-per-platform": {
			"a_linux.go":   "package main\n\nconst osName = \"linux\"\n",
			"a_windows.go": "package main\n\nconst osName = \"windows\"\n",
		},
		"anchor-shared": {"a.go": "package main\n\nconst osName = \"any\"\n"},
	}
	const subjects = "package main\n\nvar counter int\n\nfunc init() { counter = 1 }\n\nfunc helper() {}\n\ntype mode int\n\nconst modeA mode = 1\n"
	spelled := map[string]map[string]string{
		"spelled-shared":       {"z.go": subjects},
		"spelled-per-platform": {"z_linux.go": subjects, "z_windows.go": subjects},
		"spelled-on-one":       {"z_linux.go": subjects},
	}
	const method = "package main\n\nfunc (impl) work() {}\n"
	types := map[string]map[string]string{
		"type-shared":       {"t.go": "package main\n\ntype impl struct{}\n", "m.go": method},
		"type-per-platform": {"t_linux.go": "package main\n\ntype impl struct{}\n", "t_windows.go": "package main\n\ntype impl struct{}\n", "m.go": method},
	}
	const lib = "package lib\n\nvar spelled = \"unused\"\n\nfunc Use() { println(spelled) }\n\nfunc unused() {}\n"
	libs := map[string]map[string]string{
		"lib-shared":            {"lib/lib.go": lib},
		"lib-per-platform-only": {"lib/lib_linux.go": lib, "lib/lib_windows.go": lib},
		"lib-mixed":             {"lib/a_linux.go": "package lib\n\nconst os = 1\n", "lib/lib.go": lib},
	}
	common := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27\n",
		"main.go": "package main\n\nimport \"example.com/app/lib\"\n\n" +
			"var names = []string{\"helper\", \"mode\", \"work\"}\n\nfunc main() { lib.Use(); println(names[0], osName) }\n",
	}

	var worlds []matrixWorld
	for _, a := range slices.Sorted(maps.Keys(anchors)) {
		for _, s := range slices.Sorted(maps.Keys(spelled)) {
			for _, ty := range slices.Sorted(maps.Keys(types)) {
				for _, l := range slices.Sorted(maps.Keys(libs)) {
					files := maps.Clone(common)
					maps.Copy(files, anchors[a])
					maps.Copy(files, spelled[s])
					maps.Copy(files, types[ty])
					maps.Copy(files, libs[l])
					worlds = append(worlds, matrixWorld{name: a + "/" + s + "/" + ty + "/" + l, files: files})
				}
			}
		}
	}
	return worlds
}

// configured is what one run over a world holds about each declaration, keyed by
// [declarationKey]: its containers and the code and confidence of its finding.
type configured struct {
	containers map[string][]string
	found      map[string]string
	packages   map[string]int
}

// declarationKey names a declaration the same way in every run: its reference and
// the file that writes it, or for a package its reference and directory, since a
// package is written by every file it holds.
func declarationKey(s *inventory.Symbol) string {
	if s.Kind == inventory.KindPackage {
		return "package " + s.Ref + " " + path.Dir(s.File)
	}
	return s.Ref + " " + s.File
}

func runWorld(t *testing.T, files map[string]string, configurations ...string) configured {
	t.Helper()

	files = maps.Clone(files)
	files["deadset.json"] = `{"target": {"kind": "application"}, "analysis": {"configurations": [` +
		strings.Join(configurations, ", ") + `]}}`
	set := findingsOfDir(t, writeModule(t, files))
	byID := make(map[inventory.SymbolID]*inventory.Symbol, len(set.Merged.Symbols))
	for i := range set.Merged.Symbols {
		byID[set.Merged.Symbols[i].ID] = &set.Merged.Symbols[i]
	}
	held := configured{containers: map[string][]string{}, found: map[string]string{}, packages: map[string]int{}}
	for i := range set.Merged.Symbols {
		s := &set.Merged.Symbols[i]
		key := declarationKey(s)
		if s.Kind == inventory.KindPackage {
			held.packages[key]++
		}
		var containers []string
		for _, parent := range s.Parents {
			containers = append(containers, declarationKey(byID[parent]))
		}
		slices.Sort(containers)
		held.containers[key] = containers
	}
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		held.found[found.Symbol.Ref+" "+found.Position.Path] = string(found.Code) + " " + string(found.Confidence)
	}
	return held
}

// TestAMatrixGivesEachDeclarationTheContainersAndCapOfEveryConfigurationThatCompilesIt
// checks a two-configuration run against the runs of each configuration alone:
// a declaration's containers are the union of the containers each configuration
// that compiles it gives it, a package is one declaration, and a declaration is
// reported (matrix.4) with its single-configuration code and confidence exactly
// when every configuration compiling it reports it.
func TestAMatrixGivesEachDeclarationTheContainersAndCapOfEveryConfigurationThatCompilesIt(t *testing.T) {
	linux := `{"id": "linux-amd64", "os": "linux", "arch": "amd64"}`
	windows := `{"id": "windows-amd64", "os": "windows", "arch": "amd64"}`
	for _, world := range matrixWorlds() {
		t.Run(world.name, func(t *testing.T) {
			alone := []configured{runWorld(t, world.files, linux), runWorld(t, world.files, windows)}
			for _, order := range [][]string{{linux, windows}, {windows, linux}} {
				matrix := runWorld(t, world.files, order...)
				for _, disagreement := range againstOracle(matrix, alone) {
					t.Errorf("configurations %s first: %s", order[0][8:13], disagreement)
				}
			}
		})
	}
}

// againstOracle is every way a matrix's run differs from what the runs of its
// configurations alone say it holds.
func againstOracle(matrix configured, alone []configured) []string {
	var wrong []string
	for key, count := range matrix.packages {
		if count != 1 {
			wrong = append(wrong, fmt.Sprintf("%s is %d declarations, want 1", key, count))
		}
	}
	keys := make(map[string]bool)
	for _, one := range alone {
		for key := range one.containers {
			keys[key] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		var want []string
		for _, one := range alone {
			if containers, held := one.containers[key]; held {
				want = append(want, containers...)
			}
		}
		slices.Sort(want)
		want = slices.Compact(want)
		if got := matrix.containers[key]; !slices.Equal(got, want) {
			wrong = append(wrong, fmt.Sprintf("%s has containers %q, want %q", key, got, want))
		}
	}

	found := make(map[string]bool)
	for _, one := range alone {
		for key := range one.found {
			found[key] = true
		}
	}
	for key := range matrix.found {
		found[key] = true
	}
	for _, key := range slices.Sorted(maps.Keys(found)) {
		answers := make(map[string]bool)
		everywhere := true
		for _, one := range alone {
			ref, file, _ := strings.Cut(key, " ")
			if _, compiled := one.containers[ref+" "+file]; !compiled {
				continue
			}
			answer, reported := one.found[key]
			everywhere = everywhere && reported
			answers[answer] = true
		}
		want := ""
		if everywhere && len(answers) == 1 {
			want = slices.Collect(maps.Keys(answers))[0]
		}
		if everywhere && len(answers) > 1 {
			wrong = append(wrong, fmt.Sprintf("%s: the configurations disagree (%q), so the world defines no answer", key, slices.Sorted(maps.Keys(answers))))
			continue
		}
		if got := matrix.found[key]; got != want {
			wrong = append(wrong, fmt.Sprintf("%s is reported %q, want %q", key, got, want))
		}
	}
	return wrong
}

package draws

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"pgregory.net/rapid"
)

func TestCountMakesTheFullCountWhereTheVariableAsksForItAndAQuarterOfALoadingOneOtherwise(t *testing.T) {
	for _, row := range []struct {
		variable string
		full     int
		cost     Cost
		want     int
	}{
		{variable: "", full: 100, cost: InMemory, want: 100},
		{variable: "", full: 100, cost: Loading, want: 25},
		{variable: "", full: 32, cost: Loading, want: 8},
		{variable: "", full: 10, cost: Loading, want: 3},
		{variable: "", full: 1, cost: Loading, want: 1},
		{variable: "quarter", full: 24, cost: Loading, want: 6},
		{variable: Full, full: 24, cost: Loading, want: 24},
		{variable: Full, full: 100, cost: InMemory, want: 100},
	} {
		t.Run(fmt.Sprintf("%s_%d_%d", row.variable, row.full, row.cost), func(t *testing.T) {
			t.Setenv(Variable, row.variable)
			if got := Count(row.full, row.cost); got != row.want {
				t.Errorf("Count(%d, %d) with %s=%q = %d, want %d", row.full, row.cost, Variable, row.variable, got, row.want)
			}
			if got := Seeds(row.full, row.cost); got != uint64(row.want) {
				t.Errorf("Seeds(%d, %d) with %s=%q = %d, want %d", row.full, row.cost, Variable, row.variable, got, row.want)
			}
		})
	}
}

func TestCheckMakesEveryDrawOfTheCountInParallelShards(t *testing.T) {
	// sharded's own answer, not flag.Visit: its flag.Set of -rapid.checks marks the
	// flag as given to every later Visit.
	shard, err := sharded()
	if err != nil {
		t.Fatalf("Setup: set rapid's flags: %v", err)
	}
	if !shard {
		t.Skip("-rapid.checks runs one rapid.Check of the count the invocation gives")
	}
	var draws, running, overlapped atomic.Int64
	t.Run("property", func(t *testing.T) {
		Check(t, 12, InMemory, func(rt *rapid.T) {
			draws.Add(1)
			if running.Add(1) > 1 {
				overlapped.Store(1)
			}
			_ = rapid.Int().Draw(rt, "a value")
			running.Add(-1)
		})
	})
	per := int64(shardChecks)
	if testing.Short() {
		per /= 5
	}
	if want := int64(Shards(12, InMemory)) * per; draws.Load() != want {
		t.Errorf("Check(12, InMemory) made %d draws, want %d: %d shards of %d", draws.Load(), want, Shards(12, InMemory), per)
	}
	if Shards(12, InMemory) != 3 {
		t.Errorf("Shards(12, InMemory) = %d, want 3", Shards(12, InMemory))
	}
	if v := flag.Lookup("rapid.nofailfile").Value.String(); v != "true" {
		t.Errorf("-rapid.nofailfile = %s after Check, want true: a fail file lands in the package directory", v)
	}
}

// rapidRunners are rapid's functions that run a property, every one of which takes
// its count from -rapid.checks rather than from [Count].
var rapidRunners = map[string]bool{"Check": true, "MakeCheck": true, "MakeFuzz": true}

func TestEveryPropertyOfTheModuleDrawsThroughThisPackage(t *testing.T) {
	root := moduleRoot(t)
	own, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var sites []string
	read := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || filepath.Dir(path) == own {
			return nil
		}
		read++
		found, err := undrawn(root, path)
		sites = append(sites, found...)
		return err
	})
	if err != nil {
		t.Fatalf("Setup: walk %s: %v", root, err)
	}
	if read == 0 {
		t.Fatal("the walk read no Go file, so this test pins nothing")
	}
	for _, site := range sites {
		t.Errorf("%s: no run sets its count; run the property with draws.Check and range a seed loop over draws.Seeds", site)
	}
}

// undrawn is every site of one file that runs rapid other than through [Check], or
// ranges a seed loop over anything but [Seeds].
func undrawn(root, path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = importPath
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	var sites []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if pkg, fn := called(n, names); pkg == "pgregory.net/rapid" && rapidRunners[fn] {
				sites = append(sites, fmt.Sprintf("%s:%d: rapid.%s", rel, fset.Position(n.Pos()).Line, fn))
			}
		case *ast.RangeStmt:
			key, isIdent := n.Key.(*ast.Ident)
			if !isIdent || key.Name != "seed" {
				return true
			}
			if call, isCall := n.X.(*ast.CallExpr); isCall {
				if pkg, fn := called(call, names); strings.HasSuffix(pkg, "/internal/testsupport/draws") && fn == "Seeds" {
					return true
				}
			}
			sites = append(sites, fmt.Sprintf("%s:%d: a seed loop", rel, fset.Position(n.Pos()).Line))
		}
		return true
	})
	return sites, nil
}

// called is the import path and name of the package-level function call names,
// where call is a selector on an imported package's name.
func called(call *ast.CallExpr, names map[string]string) (pkg, fn string) {
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return "", ""
	}
	qualifier, isIdent := selector.X.(*ast.Ident)
	if !isIdent {
		return "", ""
	}
	return names[qualifier.Name], selector.Sel.Name
}

// moduleRoot is the directory of the go.mod above this package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Setup: %v", err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("Setup: no go.mod above the package directory")
		}
		dir = parent
	}
}

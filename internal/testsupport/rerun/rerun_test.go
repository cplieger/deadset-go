package rerun_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/rerun"
)

// A rerun keeps the race options this process was given, after which the exit
// sleep is off, and every other entry stays as given.
func TestEnvironmentTurnsTheExitSleepOffAfterTheInheritedOptions(t *testing.T) {
	t.Setenv("GORACE", "halt_on_error=1")
	got := rerun.Environment([]string{"A=1"})
	want := []string{"A=1", "GORACE=halt_on_error=1 atexit_sleep_ms=0"}
	if !slices.Equal(got, want) {
		t.Errorf("Environment([A=1]) under GORACE=halt_on_error=1 = %q, want %q", got, want)
	}
	t.Setenv("GORACE", "")
	if got := rerun.Environment(nil); !slices.Equal(got, []string{"GORACE=atexit_sleep_ms=0"}) {
		t.Errorf("Environment(nil) with no GORACE = %q, want [GORACE=atexit_sleep_ms=0]", got)
	}
}

// TestEveryRerunOfTheTestBinaryTurnsTheExitSleepOff holds every function of the
// module's test code that names the running test binary, os.Args[0] or
// os.Executable(), to building its child's environment with Environment: a
// race-built child otherwise sleeps a second on every exit.
func TestEveryRerunOfTheTestBinaryTurnsTheExitSleepOff(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var missing, found []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		testCode := strings.HasSuffix(path, "_test.go") || strings.HasPrefix(filepath.ToSlash(rel), "internal/testsupport/")
		if d.IsDir() || !strings.HasSuffix(path, ".go") || !testCode {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, held := decl.(*ast.FuncDecl)
			if !held || fn.Body == nil {
				continue
			}
			names, sets := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch e := n.(type) {
				case *ast.IndexExpr:
					if sel, held := e.X.(*ast.SelectorExpr); held && isOS(sel.X) && sel.Sel.Name == "Args" {
						if lit, held := e.Index.(*ast.BasicLit); held && lit.Value == "0" {
							names = true
						}
					}
				case *ast.CallExpr:
					if sel, held := e.Fun.(*ast.SelectorExpr); held {
						names = names || isOS(sel.X) && sel.Sel.Name == "Executable"
						sets = sets || sel.Sel.Name == "Environment" && isIdent(sel.X, "rerun")
					}
				}
				return true
			})
			if names {
				site := filepath.ToSlash(rel) + " " + fn.Name.Name
				found = append(found, site)
				if !sets {
					missing = append(missing, site)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walk the module: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("Setup: no test code names the running test binary, so this test pins nothing")
	}
	if len(missing) > 0 {
		t.Errorf("these functions re-run the test binary without rerun.Environment: %q", missing)
	}
}

func isOS(x ast.Expr) bool { return isIdent(x, "os") }

func isIdent(x ast.Expr, name string) bool {
	id, held := x.(*ast.Ident)
	return held && id.Name == name
}

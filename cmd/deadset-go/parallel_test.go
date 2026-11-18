package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// serialTests are the package's top-level tests that do not run in parallel, each
// with what of the process it owns. Every other test runs in parallel: a serial one
// holds every parallel test of the package until it ends.
var serialTests = map[string]string{
	"TestAnInvocationReadsItsPathsFromItsDirectoryAsAProcessThere":         "the working directory, which it shows invoke never reads",
	"TestARunCancelledWhileTheToolchainRunsEndsWithTheInterruptedRunClass": "PATH, which names the go command an in-process load runs",
	"TestARunThatCannotFitExitsWithTheMemoryLine":                          "half a gigabyte of live heap, which stands for a run's",
}

func TestEveryTopLevelTestRunsInParallelOrOwnsWhatItsSerialEntryNames(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("Setup: list the test files: %v", err)
	}
	fset := token.NewFileSet()
	serial := map[string]bool{}
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, held := decl.(*ast.FuncDecl)
			if !held || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			if !callsParallel(fn.Body) {
				serial[fn.Name.Name] = true
			}
		}
	}
	for name := range serial {
		if _, named := serialTests[name]; !named {
			t.Errorf("%s does not run in parallel and serialTests names nothing it owns", name)
		}
	}
	for name := range serialTests {
		if !serial[name] {
			t.Errorf("serialTests names %s, which runs in parallel or is gone", name)
		}
	}
}

// callsParallel reports whether one of body's own statements is a call of
// t.Parallel or deadline.Parallel.
func callsParallel(body *ast.BlockStmt) bool {
	for _, statement := range body.List {
		expression, held := statement.(*ast.ExprStmt)
		if !held {
			continue
		}
		if call, held := expression.X.(*ast.CallExpr); held {
			if selector, held := call.Fun.(*ast.SelectorExpr); held && selector.Sel.Name == "Parallel" {
				return true
			}
		}
	}
	return false
}

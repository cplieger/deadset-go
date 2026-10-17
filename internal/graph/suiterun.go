package graph

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// suiteRunner is one function that calls, by reflection, the methods of a value it is
// passed: each whose name opens with one of prefixes, and each that names lists.
type suiteRunner struct {
	pkg, name string
	prefixes  []string
	names     []string
	arg       int // the position of the suite among the call's arguments
}

// suiteRunners are the test suite runners whose suite's methods no reference reaches.
var suiteRunners = [...]suiteRunner{
	{
		pkg: "github.com/stretchr/testify/suite", name: "Run", arg: 1,
		prefixes: []string{testPrefix},
	},
	{
		pkg: "gopkg.in/check.v1", name: "Suite", arg: 0,
		prefixes: []string{testPrefix, benchmarkPrefix},
		names:    []string{"SetUpSuite", "SetUpTest", "TearDownTest", "TearDownSuite"},
	},
}

// suiteMethods roots, as tests, the methods a suite runner calls on each value of a
// static type other than an interface that a call in f passes it as the suite.
func (d *rootDetection) suiteMethods(p *packages.Package, f *ast.File) {
	if p.TypesInfo == nil {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			d.suiteCall(p.TypesInfo, call)
		}
		return true
	})
}

// suiteCall roots the methods one call's suite runner calls on its suite.
func (d *rootDetection) suiteCall(info *types.Info, call *ast.CallExpr) {
	runner := runnerOf(info, call)
	if runner == nil || runner.arg >= len(call.Args) {
		return
	}
	suite := info.TypeOf(call.Args[runner.arg])
	if suite == nil || types.IsInterface(suite) {
		return
	}
	for selection := range types.NewMethodSet(suite).Methods() {
		method := selection.Obj()
		if method.Exported() && runner.calls(method.Name()) {
			d.addObject(method, RootTest)
		}
	}
}

// runnerOf is the suite runner a call calls, nil for any other callee.
func runnerOf(info *types.Info, call *ast.CallExpr) *suiteRunner {
	var name *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.SelectorExpr:
		name = fun.Sel
	case *ast.Ident:
		name = fun
	default:
		return nil
	}
	fn, ok := info.Uses[name].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil
	}
	for i := range suiteRunners {
		if runner := &suiteRunners[i]; runner.pkg == fn.Pkg().Path() && runner.name == fn.Name() {
			return runner
		}
	}
	return nil
}

// calls reports whether the runner calls a method of that name.
func (r *suiteRunner) calls(name string) bool {
	for _, prefix := range r.prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return slices.Contains(r.names, name)
}

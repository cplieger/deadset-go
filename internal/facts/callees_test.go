package facts

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"golang.org/x/tools/go/packages"
)

// calleeSource is one package whose function main calls through every shape a
// function value takes, each call's callee expression naming the shape.
const calleeSource = `package lib

import "fmt"

func encode(v any) error { return nil }
func keep(v any) error   { return nil }

type writer struct{}

func (writer) write(v any) error { return nil }

type sinks struct{ write func(any) error }

type handler func(any) error

var (
	viaVar    = encode
	either    = keep
	literal   = keep
	addressed = keep
	byName    = map[string]func(any) error{"json": encode}
	escaped   = map[string]func(any) error{"json": encode}
	listed    = []func(any) error{encode}
	Exported  = encode
	declared  func(any) error
	hooked    = encode
	tested    = keep
)

func apply(f func(any) error, v any) error { return f(v) }

func hold(m map[string]func(any) error) {}

func main() {
	either = encode
	literal = func(v any) error { return nil }
	p := &addressed
	_ = p
	hold(escaped)
	listed = append(listed, keep)
	local := encode
	s := sinks{write: keep}
	method := writer{}.write
	expr := writer.write
	converted := handler(keep)
	_ = viaVar(1)
	_ = either(2)
	_ = literal(3)
	_ = addressed(4)
	_ = byName["json"](5)
	_ = escaped["json"](6)
	for _, each := range listed {
		_ = each(7)
	}
	_ = Exported(8)
	_ = local(9)
	_ = s.write(10)
	_ = method(11)
	_ = expr(writer{}, 12)
	_ = converted(13)
	_ = declared(14)
	_ = encode(15)
	_ = apply(keep, 16)
	_ = fmt.Sprint(len(listed))
	_ = func(v any) error { return nil }(17)
	_ = hooked(18)
	_ = tested(19)
}
`

// calleeTestSource is a test file of the package that writes one of its function
// variables and takes the address of another.
const calleeTestSource = `package lib

func testOnly(v any) error { return nil }

func init() {
	hooked = testOnly
	p := &tested
	_ = p
	_ = hooked(20)
	_ = tested(21)
}
`

// calleesFixture checks calleeSource and calleeTestSource as the package at path and
// reads their calls.
func calleesFixture(t *testing.T, path string) (c *Callees, info *types.Info, file, testFile *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "/m/lib/lib.go", calleeSource, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse: %v", err)
	}
	testFile, err = parser.ParseFile(fset, "/m/lib/lib_test.go", calleeTestSource, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parse: %v", err)
	}
	info = &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object), Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Instances: make(map[*ast.Ident]types.Instance),
	}
	files := []*ast.File{file, testFile}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check(path, fset, files, info)
	if err != nil {
		t.Fatalf("Setup: check: %v", err)
	}
	p := &packages.Package{PkgPath: path, Name: "lib", Types: pkg, TypesInfo: info, Syntax: files}
	return calleesOf([]*packages.Package{p}, inventory.NewPositions(fset, "/m", nil)), info, file, testFile
}

// callOf is the call in file whose last argument is the constant n.
func callOf(t *testing.T, file *ast.File, n string) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if call, isCall := node.(*ast.CallExpr); isCall && len(call.Args) > 0 {
			if lit, isLit := call.Args[len(call.Args)-1].(*ast.BasicLit); isLit && lit.Value == n {
				found = call
			}
		}
		return found == nil
	})
	if found == nil {
		t.Fatalf("Setup: no call passes %s", n)
	}
	return found
}

func calleeNames(found []Callee) []string {
	var names []string
	for _, c := range found {
		name := c.Fn.Name()
		if c.Shift != 0 {
			name += "+receiver"
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// A call reaches each function a function value can hold where the analysis reads
// every write of the place, and a function it does not know beside them where the
// place is a parameter, takes a function literal, escapes through its address or as
// an argument, or is an exported variable of a published package. A place nothing
// writes holds no function at all.
func TestCalleesFollowEveryShapeOfAFunctionValue(t *testing.T) {
	t.Parallel()
	c, info, file, _ := calleesFixture(t, "example.com/lib")
	tests := []struct {
		arg     string
		want    []string
		dynamic bool
	}{
		{"1", []string{"encode"}, false},
		{"2", []string{"encode", "keep"}, false},
		{"3", []string{"keep"}, true},
		{"4", []string{"keep"}, true},
		{"5", []string{"encode"}, false},
		{"6", []string{"encode"}, true},
		{"7", []string{"encode", "keep"}, false},
		{"8", []string{"encode"}, true},
		{"9", []string{"encode"}, false},
		{"10", []string{"keep"}, false},
		{"11", []string{"write"}, false},
		{"12", []string{"write+receiver"}, false},
		{"13", []string{"keep"}, false},
		{"14", nil, false},
		{"15", []string{"encode"}, false},
		{"16", []string{"apply"}, false},
		{"17", nil, true},
		{"18", []string{"encode"}, false},
		{"19", []string{"keep"}, false},
	}
	for _, tt := range tests {
		found, dynamic := c.Of(info, callOf(t, file, tt.arg))
		if got := calleeNames(found); !slices.Equal(got, tt.want) || dynamic != tt.dynamic {
			t.Errorf("Of(call passing %s) = %v, dynamic %t; want %v, dynamic %t", tt.arg, got, dynamic, tt.want, tt.dynamic)
		}
	}
	var inner *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if call, isCall := node.(*ast.CallExpr); isCall {
			if id, named := call.Fun.(*ast.Ident); named && id.Name == "f" {
				inner = call
			}
		}
		return inner == nil
	})
	if found, dynamic := c.Of(info, inner); len(found) != 0 || !dynamic {
		t.Errorf("Of(f(v)) through a parameter = %v, dynamic %t; want none, dynamic", calleeNames(found), dynamic)
	}
}

// A builtin and a conversion are no calls of a function, and a call in a package
// read apart from the program, whose positions no place is keyed in, follows no
// place.
func TestCalleesReachNoFunctionThroughABuiltinAConversionOrAForeignReading(t *testing.T) {
	t.Parallel()
	c, info, file, _ := calleesFixture(t, "example.com/lib/internal/lib")
	var builtin, conversion *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		switch id, _ := call.Fun.(*ast.Ident); {
		case id != nil && id.Name == "append":
			builtin = call
		case id != nil && id.Name == "handler":
			conversion = call
		}
		return true
	})
	for name, call := range map[string]*ast.CallExpr{"append": builtin, "handler(keep)": conversion} {
		if found, dynamic := c.Of(info, call); len(found) != 0 || dynamic {
			t.Errorf("Of(%s) = %v, dynamic %t; want none", name, calleeNames(found), dynamic)
		}
	}
	if found, dynamic := c.Of(info, callOf(t, file, "8")); !slices.Equal(calleeNames(found), []string{"encode"}) || dynamic {
		t.Errorf("Of(Exported(8)) in an internal package = %v, dynamic %t; want encode alone", calleeNames(found), dynamic)
	}
	_, foreign, foreignFile, _ := calleesFixture(t, "example.com/lib")
	found, dynamic := c.Of(foreign, callOf(t, foreignFile, "1"))
	if len(found) != 0 || !dynamic {
		t.Errorf("Of(viaVar(1)) of another reading = %v, dynamic %t; want none, dynamic", calleeNames(found), dynamic)
	}
	if found, _ := c.Of(foreign, callOf(t, foreignFile, "15")); !slices.Equal(calleeNames(found), []string{"encode"}) {
		t.Errorf("Of(encode(15)) of another reading = %v, want encode: a direct call names its function", calleeNames(found))
	}
}

// A write or an escape in a test file reaches the calls of test files alone, so a
// production call through a variable a test reassigns holds what production code
// writes, as a test reference makes nothing live for production.
func TestCalleesKeepATestFilesWritesToTheCallsOfTestFiles(t *testing.T) {
	t.Parallel()
	c, info, _, testFile := calleesFixture(t, "example.com/lib")
	tests := []struct {
		arg     string
		want    []string
		dynamic bool
	}{
		{"20", []string{"encode", "testOnly"}, false},
		{"21", []string{"keep"}, true},
	}
	for _, tt := range tests {
		found, dynamic := c.Of(info, callOf(t, testFile, tt.arg))
		if got := calleeNames(found); !slices.Equal(got, tt.want) || dynamic != tt.dynamic {
			t.Errorf("Of(test call passing %s) = %v, dynamic %t; want %v, dynamic %t", tt.arg, got, dynamic, tt.want, tt.dynamic)
		}
	}
}

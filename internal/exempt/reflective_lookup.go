package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
)

// lookup is one call that resolves a declaration by name at run time: the callee
// a maintainer is sent to, the kinds of declaration a name given to it reaches,
// and whether the run time reaches an unexported one.
type lookup struct {
	callee   string
	kinds    []graph.SymbolKind
	exported bool
}

// lookups is the closed set of callees this class recognises, keyed by the
// receiver's type and the method's name.
//
// A method is reached only when it is exported, because the method set reflection
// exposes on a concrete type holds the exported methods alone; a field is reached
// whatever its visibility, because a field is found by name in the struct type
// either way; and a plugin symbol is an exported package-level function or
// variable, which is the only thing a plugin lookup can resolve.
//
// A method named Lookup on any other type is not in the set. The rule is a name
// resolved at run time, and a lookup into a value the program itself holds
// resolves nothing the reference graph cannot already see, so a map or a registry
// helper spelled Lookup does not fire the class.
var lookups = map[string]lookup{
	"reflect.Value.MethodByName": {
		callee:   "reflect.Value.MethodByName",
		kinds:    []graph.SymbolKind{graph.KindMethod, graph.KindInterfaceMethod},
		exported: true,
	},
	"reflect.Type.MethodByName": {
		callee:   "reflect.Type.MethodByName",
		kinds:    []graph.SymbolKind{graph.KindMethod, graph.KindInterfaceMethod},
		exported: true,
	},
	"reflect.Value.FieldByName": {
		callee: "reflect.Value.FieldByName",
		kinds:  []graph.SymbolKind{graph.KindField},
	},
	"reflect.Type.FieldByName": {
		callee: "reflect.Type.FieldByName",
		kinds:  []graph.SymbolKind{graph.KindField},
	},
	"plugin.Plugin.Lookup": {
		callee:   "plugin.Plugin.Lookup",
		kinds:    []graph.SymbolKind{graph.KindFunc, graph.KindVar},
		exported: true,
	},
}

// ReflectiveLookupDetector retains the declarations a constant string names at a
// reflective lookup call, on any type, and records the call site.
//
// The evidence is a string equal to an identifier beside a call rather than a
// type relation, so the class is the weakest of the Contract's Go classes and
// every exemption it records names a site a maintainer can go and read. A call
// whose name argument is not a constant retains nothing: a computed name is the
// case no exact rule can serve, and the class declines to guess rather than
// retaining every declaration a lookup might reach.
func ReflectiveLookupDetector(in *Input) ([]graph.Exemption, error) {
	calls, err := allLookupCalls(in)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, nil
	}

	index := newNameIndex(in)
	seen := make(map[string]struct{})
	var out []graph.Exemption
	for i := range calls {
		out = append(out, calls[i].retains(index, seen)...)
	}
	slices.SortFunc(out, byEvidence)
	return out, nil
}

// allLookupCalls returns every recognised lookup the target makes with a constant
// name, in the order the load reports its files.
func allLookupCalls(in *Input) ([]lookupCall, error) {
	var calls []lookupCall
	for _, p := range in.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			found, err := lookupCalls(in, p.TypesInfo, f)
			if err != nil {
				return nil, err
			}
			calls = append(calls, found...)
		}
	}
	return calls, nil
}

// lookupCall is one recognised lookup and the name it was given.
type lookupCall struct {
	lookup lookup
	name   string
	site   token.Position
}

// retains is the exemptions one call records: every member of the inventory the
// name reaches through this callee. seen holds the pairs already recorded, so one
// call site type-checked by several package variants records each member once.
func (c *lookupCall) retains(index nameIndex, seen map[string]struct{}) []graph.Exemption {
	var out []graph.Exemption
	for _, m := range index[c.name] {
		if !slices.Contains(c.lookup.kinds, m.kind) {
			continue
		}
		if c.lookup.exported && !m.exported {
			continue
		}
		key := string(m.id) + " " + c.site.String()
		if _, held := seen[key]; held {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, graph.Exemption{
			ID:     m.id,
			Class:  string(ReflectiveLookup),
			Site:   c.site,
			Detail: "looked up by " + c.lookup.callee,
		})
	}
	return out
}

// lookupCalls returns every recognised lookup one file makes with a constant
// name, in the order the file writes them.
func lookupCalls(in *Input, info *types.Info, f *ast.File) ([]lookupCall, error) {
	var calls []lookupCall
	var failure error
	ast.Inspect(f, func(n ast.Node) bool {
		if failure != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		l, ok := lookups[calleeKey(info, call)]
		if !ok {
			return true
		}
		name, ok := constantName(info, call)
		if !ok {
			return true
		}
		site, err := in.Resolve.Render(call.Pos())
		if err != nil {
			failure = err
			return false
		}
		calls = append(calls, lookupCall{lookup: l, name: name, site: site})
		return true
	})
	return calls, failure
}

// calleeKey names the method a call selects as its receiver's type and the
// method's own name, and is empty for a call that selects no method. The method
// is resolved from the type information, so a local name for the reflect package
// or a value whose type is an alias of one names the same method, and the
// receiver is the one the method declares rather than the type written at the
// call.
func calleeKey(info *types.Info, call *ast.CallExpr) string {
	fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc || fn.Pkg() == nil {
		return ""
	}
	declared := fn.Signature().Recv()
	if declared == nil {
		return ""
	}
	recv := receiverName(declared.Type())
	if recv == "" {
		return ""
	}
	return fn.Pkg().Path() + "." + recv + "." + fn.Name()
}

// receiverName is the name of the defined type a method is selected on, reached
// through a pointer, and empty for a receiver that is not a defined type.
func receiverName(t types.Type) string {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return ""
	}
	return named.Obj().Name()
}

// constantName returns the string a call's first argument is, when the type
// checker resolved that argument to a string constant. A literal, a named
// constant and a constant expression all qualify; anything computed at run time
// does not.
func constantName(info *types.Info, call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	value := info.Types[ast.Unparen(call.Args[0])].Value
	if value == nil || value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(value), true
}

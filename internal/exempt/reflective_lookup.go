package exempt

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// lookup is one call that resolves a declaration by name at run time: the callee
// a maintainer is sent to, the kinds of declaration a name given to it reaches,
// and whether the run time reaches an unexported one.
type lookup struct {
	callee   string
	kinds    []inventory.SymbolKind
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
		kinds:    []inventory.SymbolKind{inventory.KindMethod, inventory.KindInterfaceMethod},
		exported: true,
	},
	"reflect.Type.MethodByName": {
		callee:   "reflect.Type.MethodByName",
		kinds:    []inventory.SymbolKind{inventory.KindMethod, inventory.KindInterfaceMethod},
		exported: true,
	},
	"reflect.Value.FieldByName": {
		callee: "reflect.Value.FieldByName",
		kinds:  []inventory.SymbolKind{inventory.KindField},
	},
	"reflect.Type.FieldByName": {
		callee: "reflect.Type.FieldByName",
		kinds:  []inventory.SymbolKind{inventory.KindField},
	},
	"plugin.Plugin.Lookup": {
		callee:   "plugin.Plugin.Lookup",
		kinds:    []inventory.SymbolKind{inventory.KindFunc, inventory.KindVar},
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
	out, err := registeredMethods(in)
	if err != nil {
		return nil, err
	}
	if len(calls) > 0 {
		index := newNameIndex(in)
		seen := make(map[string]struct{})
		for i := range calls {
			out = append(out, calls[i].retains(index, seen)...)
		}
	}
	slices.SortFunc(out, byEvidence)
	return out, nil
}

// registeredMethods is the class's second rule, for a router that registers a handler
// by its method name: it retains every exported method of a defined type T of the
// program whose name a constant string argument spells, in a call of a function or method
// outside the analysed program that also passes a value of T or *T. Each item of the
// string separated by ";" spells one name, after an optional prefix ending in ":", the
// verbs a route maps to the method.
func registeredMethods(in *Input) ([]graph.Exemption, error) {
	own := programPaths(in)
	var out []graph.Exemption
	for _, p := range in.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			var failed error
			ast.Inspect(file, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if failed != nil || !isCall {
					return failed == nil
				}
				var found []graph.Exemption
				found, failed = registrationOf(in, p.TypesInfo, call, own)
				out = append(out, found...)
				return failed == nil
			})
			if failed != nil {
				return nil, failed
			}
		}
	}
	return out, nil
}

// registrationOf is what one call retains under [registeredMethods].
func registrationOf(in *Input, info *types.Info, call *ast.CallExpr, own map[string]bool) ([]graph.Exemption, error) {
	callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc || callee.Pkg() == nil || own[callee.Pkg().Path()] {
		return nil, nil
	}
	names, receivers := registrationArguments(info, call, own)
	if len(names) == 0 || len(receivers) == 0 {
		return nil, nil
	}
	site, err := in.Resolve.Render(call.Pos())
	if err != nil {
		return nil, err
	}
	var out []graph.Exemption
	for _, named := range receivers {
		set := types.NewMethodSet(types.NewPointer(named))
		for _, name := range names {
			selection := set.Lookup(nil, name)
			if selection == nil {
				continue
			}
			if id, held := in.Resolve.Object(selection.Obj()); held {
				out = append(out, graph.Exemption{
					ID: id, Class: string(vocab.ReflectiveLookup), Site: site,
					Detail: "named by a string passed with " + named.Obj().Name() + " to " + callee.FullName(),
				})
			}
		}
	}
	return out, nil
}

// registrationArguments is the method names the constant string arguments of one call
// spell, and the defined types of the program its other arguments are values of.
func registrationArguments(info *types.Info, call *ast.CallExpr, own map[string]bool) (names []string, receivers []*types.Named) {
	for _, arg := range call.Args {
		tv := info.Types[ast.Unparen(arg)]
		if tv.Value != nil && tv.Value.Kind() == constant.String {
			names = append(names, spelledMethods(constant.StringVal(tv.Value))...)
			continue
		}
		if named, defined := definedReceiver(tv.Type); defined && own[named.Obj().Pkg().Path()] {
			receivers = append(receivers, named)
		}
	}
	return names, receivers
}

// spelledMethods is every exported identifier one string spells under
// [registeredMethods].
func spelledMethods(value string) []string {
	var names []string
	for item := range strings.SplitSeq(value, ";") {
		name := strings.TrimSpace(item[strings.LastIndex(item, ":")+1:])
		if token.IsIdentifier(name) && token.IsExported(name) {
			names = append(names, name)
		}
	}
	return names
}

// definedReceiver is the defined type a value of t, or the value a pointer of t points
// to, has, where it is no interface.
func definedReceiver(t types.Type) (*types.Named, bool) {
	if t == nil {
		return nil, false
	}
	if p, isPointer := types.Unalias(t).(*types.Pointer); isPointer {
		t = p.Elem()
	}
	named, isNamed := types.Unalias(t).(*types.Named)
	if !isNamed || named.Obj().Pkg() == nil || types.IsInterface(named) {
		return nil, false
	}
	return named, true
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
			Class:  string(vocab.ReflectiveLookup),
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

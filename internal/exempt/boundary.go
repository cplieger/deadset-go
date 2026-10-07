package exempt

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// The boundary of the analysed program: a struct value handed to an empty-interface
// parameter of a function the program does not declare has left the analysis, and
// evidence a test file carries does not hold under a production run. A site renders
// against the target root alone, so the call walks read the target's packages.

// boundary is the edge of the analysed program over one loaded configuration: the
// declarations the program holds, and the parameters of its own functions a value
// crosses out through. The declaration decides the edge rather than the package path,
// because a module both a consumer and the target's build list hold is loaded twice,
// and the copy the target calls is then a body the program does not hold.
type boundary struct {
	sites    *declarationSites
	held     map[token.Position]bool
	forwards destinationParameters
}

// destinationParameters are the parameters of the analysed program's own functions
// that a value crosses out through, each declaration keyed by the position it is
// written at.
type destinationParameters map[token.Position][]sinkParameter

// sinkParameter is one parameter at which a value leaves the analysed program: the
// position of the parameter, and what reads the value there reads of its methods
// beside its fields.
type sinkParameter struct {
	reach reach
	at    int
}

// destinationTest reports whether one parameter of one function is a destination the
// calling class names by itself, and where it is, what that destination reads of the
// methods of a value it is given. It is the base case the forwarding fixpoint starts
// from and the source of every retained set the fixpoint carries.
type destinationTest func(fn *types.Func, at int) (reach, bool)

// crossingOut is what one argument's crossing out of the analysed program retains:
// the callee the value leaves through, which is what an exemption's detail names, and
// what the code behind that callee reads of the methods of the value beside its
// fields.
type crossingOut struct {
	callee *types.Func
	reach  reach
}

// declarationSites answers the key one declaration of the program is kept under, and
// remembers each answer, because a walk asks once per argument of every call.
//
// The key is the toolchain's position: a file two modules' loads both read is parsed
// twice into one file set, so token.Pos differs between the readings, and a consumer's
// file renders nowhere against the target root.
type declarationSites struct {
	fset  *token.FileSet
	known map[token.Pos]token.Position
}

// newDeclarationSites answers against one configuration's file set.
func newDeclarationSites(fset *token.FileSet) *declarationSites {
	return &declarationSites{fset: fset, known: make(map[token.Pos]token.Position)}
}

// of is the key one declaration is kept under.
func (d *declarationSites) of(object types.Object) token.Position {
	return d.position(object.Pos())
}

// position is the key the declaration or literal written at pos is kept under.
func (d *declarationSites) position(pos token.Pos) token.Position {
	if held, known := d.known[pos]; known {
		return held
	}
	at := d.fset.Position(pos)
	d.known[pos] = at
	return at
}

// newBoundary reads the edge of the program one input describes.
//
// destination is the base case of the forwarding fixpoint: a function of the program
// that hands one of its own empty-interface parameters to a destination the calling
// class names, or to a parameter a value already crosses out through, carries the
// crossing at that parameter of its own, retaining what the parameter it forwards to
// retains, and is then itself something a further function can forward to. The set
// grows until no function joins it and no retained set widens.
func newBoundary(in *Input, destination destinationTest) *boundary {
	declared := programFunctions(in)
	b := &boundary{
		sites:    newDeclarationSites(in.Result.Fset),
		held:     make(map[token.Position]bool, len(declared)),
		forwards: make(destinationParameters),
	}
	for _, one := range declared {
		b.held[b.sites.of(one.fn)] = true
	}

	candidates := b.forwardingParameters(declared)
	for joined := true; joined; {
		joined = false
		for i := range candidates {
			for _, forwarded := range candidates[i].forwards {
				reads, reaches := b.reaches(forwarded.to, forwarded.at, destination)
				if !reaches {
					continue
				}
				joined = b.hold(candidates[i].at, sinkParameter{at: forwarded.own, reach: reads}) || joined
			}
		}
	}
	for pos := range b.forwards {
		slices.SortFunc(b.forwards[pos], func(a, b sinkParameter) int {
			return cmp.Compare(a.at, b.at)
		})
	}
	return b
}

// crossing reports whether the argument at position arg of one call carries a value
// out of the analysed program, and names the callee it leaves through: the callee
// does not belong to the program or forwards the parameter on, the parameter is typed
// as the empty interface, the variadic one included, and the value is a struct or a
// pointer to one, or a slice, an array or a map of such values.
func (b *boundary) crossing(info *types.Info, call *ast.CallExpr, arg int) (crossingOut, bool) {
	if arg >= len(call.Args) {
		return crossingOut{}, false
	}
	fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc {
		return crossingOut{}, false
	}
	sinks, reaches := b.sinks(fn)
	if !reaches {
		return crossingOut{}, false
	}
	at, supplies := parameterAt(fn.Signature(), arg)
	if !supplies {
		return crossingOut{}, false
	}
	sink, crosses := sinkAt(sinks, at)
	if !crosses {
		return crossingOut{}, false
	}
	flows := info.TypeOf(call.Args[arg])
	if flows == nil || !carriesStruct(flows) {
		return crossingOut{}, false
	}
	return crossingOut{callee: fn, reach: sink.reach}, true
}

// sinks are the parameters of one function at which a value leaves the analysis, each
// with what reads the value there, and false where none does. A function the program
// does not declare carries one at every empty-interface parameter; one it declares
// carries the ones the forwarding set holds. A package a destination rule names one by
// one carries none, so one fact is recorded once.
func (b *boundary) sinks(fn *types.Func) ([]sinkParameter, bool) {
	if fn.Pkg() == nil {
		return nil, false
	}
	at := b.sites.of(fn)
	if b.held[at] {
		held, forwards := b.forwards[at]
		return held, forwards
	}
	if namedDestinations[fn.Pkg().Path()] {
		return nil, false
	}
	sinks := opaqueParameters(fn)
	return sinks, len(sinks) > 0
}

// declares reports whether the analysed program declares one function.
func (b *boundary) declares(fn *types.Func) bool {
	return fn.Pkg() != nil && b.held[b.sites.of(fn)]
}

// reaches reports whether one parameter of one function is somewhere a value leaves
// the program through, a destination the calling class names or a parameter this
// boundary already carries, and what is read of the value there.
func (b *boundary) reaches(fn *types.Func, at int, destination destinationTest) (reach, bool) {
	if destination != nil {
		if reads, named := destination(fn, at); named {
			return reads, true
		}
	}
	sinks, carries := b.sinks(fn)
	if !carries {
		return reach{}, false
	}
	sink, crosses := sinkAt(sinks, at)
	return sink.reach, crosses
}

// hold records one sink of the declaration written at declared, and reports whether
// that widened what the declaration carries. A parameter that reaches two destinations
// keeps the union of what they read, so a wrapper one of whose destinations resolves a
// method by name resolves it too.
func (b *boundary) hold(declared token.Position, sink sinkParameter) bool {
	held := b.forwards[declared]
	for i := range held {
		if held[i].at != sink.at {
			continue
		}
		if held[i].reach.covers(sink.reach) {
			return false
		}
		held[i].reach = held[i].reach.union(sink.reach)
		return true
	}
	b.forwards[declared] = append(held, sink)
	return true
}

// sinkAt is the sink one parameter position carries, and false where that position is
// no sink.
func sinkAt(sinks []sinkParameter, at int) (sinkParameter, bool) {
	for _, sink := range sinks {
		if sink.at == at {
			return sink, true
		}
	}
	return sinkParameter{}, false
}

// forwardingFunc is one function of the analysed program that may carry a crossing:
// where it is declared, and every call in its body that hands one of its own
// parameters typed as the empty interface to another function.
type forwardingFunc struct {
	forwards []forwardedParameter
	at       token.Position
}

// forwardedParameter is one parameter a function hands on: the function it goes to,
// the forwarding function's own parameter position, and the position it arrives at.
type forwardedParameter struct {
	to  *types.Func
	own int
	at  int
}

// forwardingParameters returns every function of the program that hands a parameter
// of its own typed as the empty interface to another function.
func (b *boundary) forwardingParameters(declared []programFunction) []forwardingFunc {
	var found []forwardingFunc
	for _, one := range declared {
		if one.decl.Body == nil {
			continue
		}
		if w, forwards := forwardedParametersOf(b.sites, one); forwards {
			found = append(found, w)
		}
	}
	return found
}

// forwardedParametersOf reports whether one declaration hands a parameter of its own
// typed as the empty interface to another function, and returns the calls that do. A
// parameter passed as itself and a variadic list spread whole are both hands-on:
// what the callee receives is the value the declaration's own caller wrote.
func forwardedParametersOf(sites *declarationSites, declared programFunction) (forwardingFunc, bool) {
	sig := declared.fn.Signature()
	own := make(map[*types.Var]int)
	for _, at := range erasedParameters(sig) {
		own[sig.Params().At(at)] = at
	}
	if len(own) == 0 {
		return forwardingFunc{}, false
	}

	w := forwardingFunc{at: sites.of(declared.fn)}
	info := declared.info
	ast.Inspect(declared.decl.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		to, isFunc := resolveObject(info, call.Fun).(*types.Func)
		if !isFunc {
			return true
		}
		for i, arg := range call.Args {
			held, names := own[parameterNamed(info, arg)]
			if !names {
				continue
			}
			at, supplies := parameterAt(to.Signature(), i)
			if !supplies {
				continue
			}
			w.forwards = append(w.forwards, forwardedParameter{to: to, own: held, at: at})
		}
		return true
	})
	return w, len(w.forwards) > 0
}

// programFunction is one function the analysed program declares: the declaration,
// the object it defines, and the type information of the variant that checked it.
type programFunction struct {
	info *types.Info
	decl *ast.FuncDecl
	fn   *types.Func
}

// programFunctions returns every function the analysed program declares, the
// target's and every loaded consumer's, in one order. It is the one enumeration the
// boundary and the forwarding walks read, so a consumer's wrapper is found for every
// class at once. A declaration with no body is here and forwards nothing; a function
// literal is not, because no call resolves to it by name.
func programFunctions(in *Input) []programFunction {
	var found []programFunction
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range graph.SortedSyntax(p, in.Result.Fset) {
			found = append(found, fileFunctions(p.TypesInfo, file)...)
		}
	}
	return found
}

// fileFunctions returns the functions one source file declares, in the order the file
// writes them.
func fileFunctions(info *types.Info, file *ast.File) []programFunction {
	var found []programFunction
	for _, decl := range file.Decls {
		fd, declares := decl.(*ast.FuncDecl)
		if !declares {
			continue
		}
		fn, defines := info.Defs[fd.Name].(*types.Func)
		if !defines {
			continue
		}
		found = append(found, programFunction{info: info, decl: fd, fn: fn})
	}
	return found
}

// programPackages are the packages of the analysed program: the target's own, then
// every loaded consumer's in the order the scope declared them, each set ordered.
func programPackages(r *load.Result) []*packages.Package {
	found := graph.SortedPackages(r.Packages)
	for _, one := range r.Consumers {
		found = append(found, graph.SortedPackages(one.Packages)...)
	}
	return found
}

// opaqueParameters are the sinks of a function the program does not declare: every
// parameter typed as the empty interface. The callee's body is not in the program, so
// each reads the value's fields, resolves the encoding and decoding methods of both
// directions, and retains what the callee's package can name ([reach]).
func opaqueParameters(fn *types.Func) []sinkParameter {
	erased := erasedParameters(fn.Signature())
	found := make([]sinkParameter, 0, len(erased))
	outside := []*types.Package{fn.Pkg()}
	for _, at := range erased {
		found = append(found, sinkParameter{at: at, reach: reach{
			methods: reachEncoding | reachDecoding,
			fields:  true,
			outside: outside,
		}})
	}
	return found
}

// erasedParameters are the parameter positions of one signature typed as the empty
// interface, the variadic parameter among them where its elements are.
func erasedParameters(sig *types.Signature) []int {
	params := sig.Params()
	var found []int
	for i := range params.Len() {
		t := params.At(i).Type()
		if sig.Variadic() && i == params.Len()-1 {
			if list, isSlice := types.Unalias(t).(*types.Slice); isSlice && erasesTheType(list.Elem()) {
				found = append(found, i)
			}
			continue
		}
		if erasesTheType(t) {
			found = append(found, i)
		}
	}
	return found
}

// erasesTheType reports whether a parameter of type t keeps nothing of the value it
// is given, which is the empty interface alone. A type parameter keeps everything: it
// stands for the type the caller instantiates the declaration with, so the value does
// not lose its own type across the call, whatever the constraint admits.
func erasesTheType(t types.Type) bool {
	if _, parameterised := types.Unalias(t).(*types.TypeParam); parameterised {
		return false
	}
	iface, isInterface := types.Unalias(t).Underlying().(*types.Interface)
	return isInterface && iface.Empty()
}

// carriesStruct reports whether a value of t is a struct or a pointer to one, which
// is the shape a consumer reading a value by name reads the members of, or a slice,
// an array or a map whose element, key or value is one.
func carriesStruct(t types.Type) bool {
	switch collection := types.Unalias(t).Underlying().(type) {
	case *types.Slice:
		return isStructValue(collection.Elem())
	case *types.Array:
		return isStructValue(collection.Elem())
	case *types.Map:
		return isStructValue(collection.Key()) || isStructValue(collection.Elem())
	}
	return isStructValue(t)
}

// isStructValue reports whether a value of t is a struct or a pointer to one.
func isStructValue(t types.Type) bool {
	u := types.Unalias(t)
	if p, pointer := u.(*types.Pointer); pointer {
		u = types.Unalias(p.Elem())
	}
	if _, parameterised := u.(*types.TypeParam); parameterised {
		return false
	}
	_, isStruct := u.Underlying().(*types.Struct)
	return isStruct
}

// parameterAt is the parameter of one signature the argument at position arg
// supplies, and false where the signature accounts for no such argument: a call that
// spreads the several results of another call names no parameter per argument.
func parameterAt(sig *types.Signature, arg int) (int, bool) {
	last := sig.Params().Len() - 1
	switch {
	case last < 0:
		return 0, false
	case sig.Variadic() && arg >= last:
		return last, true
	case arg > last:
		return 0, false
	default:
		return arg, true
	}
}

// parameterNamed is the variable one argument expression names, and nil where the
// argument is anything but a plain name.
func parameterNamed(info *types.Info, arg ast.Expr) *types.Var {
	ident, named := ast.Unparen(arg).(*ast.Ident)
	if !named {
		return nil
	}
	used, isVar := info.Uses[ident].(*types.Var)
	if !isVar {
		return nil
	}
	return used
}

// holdsInMode reports whether the evidence one exemption found at site holds under
// the run's mode: every exemption in the plain mode, and in a production one only an
// exemption whose evidence a test file does not carry. A site is a file of the target,
// so Mode.ConsumerTestsProduction, which classifies a consumer's test references,
// reaches no exemption.
func holdsInMode(site token.Position, m graph.Mode) bool {
	if !m.Production {
		return true
	}
	_, test := graph.IsTestFile(site.Filename)
	return !test
}

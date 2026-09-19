package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// The boundary of the analysed program is one rule: what leaves the program is
// fully reachable, and what the analysis cannot read ends the run rather than being
// passed over. A value or a position leaves in five ways, and this file answers the
// two an exemption class meets.
//
// A value crosses out when a struct, or a pointer to one, is handed to a parameter
// typed as the EMPTY interface of a function or method of a package outside the
// target and the consumers the run loaded. The callee's body is not in the program
// and the parameter keeps nothing of the value's type, so whatever the callee does
// with it, encode it, render it, log it or reflect over it, reads its fields and may
// call its exported methods; the value therefore flows into encoding-reflection with
// that class's full retained set, recorded at the call with the immediate callee
// named. A function of the program that hands one of its own such parameters on
// inherits the crossing at that parameter, to a fixpoint, so a wrapper of a wrapper
// carries the rule of the call it forwards to. A consumer's function is walked for
// that exactly as the target's is: a consumer is inside the program, so a consumer's
// WriteJSON is not an opaque callee but a wrapper, and a target value handed to it
// reaches the encoder through it.
//
// The empty interface is the one parameter type the crossing reads. A value handed
// to any other interface is a conversion the conversion set records, and the methods
// that interface requires are what interface-satisfaction retains for it, which is
// everything the callee can reach through the parameter's own type. A type parameter
// keeps everything too: it stands for the type the caller instantiates the
// declaration with, whatever the constraint admits.
//
// Evidence crosses out the other way. An exemption whose evidence a test file
// carries does not hold under a production run: a test that marshals a value or
// compares one makes no member of it live for production, exactly as a test's
// reference is no reference there.
//
// The remaining three crossings are answered where they arise, and a stage that
// meets a value at the edge of the program reads this list rather than adding a
// sixth policy. A compiled file outside the target root is dropped at the load. A
// consumer's reference into the target is an ordinary reference of the graph, live
// under both relations. And an interface-typed field is where a class's reach stops,
// which is the one stated limit of that reach: the dynamic type behind such a field
// is not in the type information, so a member reached only through one is retained by
// nothing and nothing infers it.
//
// One consequence of the second and third crossings together: a class records the
// site its evidence was found at, and a site is rendered relative to the target root,
// so every site a class publishes is a file of the target. That is why the call walks
// read the target's packages while the forwarding walk reads the whole program: a
// consumer's own call site has no rendering, and recording one would end the run.

// boundary is the edge of the analysed program over one loaded configuration: the
// declarations the program holds, and the parameters of its own functions a value
// crosses out through.
//
// What decides the edge is the declaration and not the package path, because two
// modules of one program can hold two copies of one package. The target's load reads
// its own module file and never a workspace, so a module the scope declares as a
// consumer and the target also depends on is read twice, at the consumer's directory
// and at whatever the target's build list resolves; the copy the target calls is then
// a function whose body the program does not hold, whatever its import path says, and
// the crossing is what that is.
type boundary struct {
	sites    *declarationSites
	held     map[token.Position]bool
	forwards destinationParameters
}

// destinationParameters are the parameters of the analysed program's own functions
// that a value crosses out through, each declaration keyed by the position it is
// written at.
type destinationParameters map[token.Position][]int

// declarationSites answers the key one declaration of the program is kept under, and
// remembers each answer.
//
// The key is the position the toolchain reported for the declaration, because that is
// the one spelling every load of one file agrees on. A package and its test variant
// share a token.Pos within one load, but a file two modules' loads both read is parsed
// twice into the configuration's file set and the two readings carry different token.Pos
// values for the same declaration, so a consumer's wrapper found in the consumer's own
// load would never match the callee a target's call resolves to. The rendered site of a
// report cannot serve here: a consumer's file is outside the target root and renders
// nowhere.
//
// The answers are remembered because a walk asks for the same callee once per argument
// of every call it makes, and the file set answers by searching its files under a lock.
type declarationSites struct {
	fset  *token.FileSet
	known map[token.Pos]token.Position
}

// newDeclarationSites answers against one configuration's file set.
func newDeclarationSites(fset *token.FileSet) *declarationSites {
	return &declarationSites{fset: fset, known: make(map[token.Pos]token.Position)}
}

// of is the key fn's declaration is kept under.
func (d *declarationSites) of(fn *types.Func) token.Position {
	if held, known := d.known[fn.Pos()]; known {
		return held
	}
	at := d.fset.Position(fn.Pos())
	d.known[fn.Pos()] = at
	return at
}

// newBoundary reads the edge of the program one input describes.
//
// destination reports whether one parameter of one function is a destination the
// calling class names by itself, which is the base case of the forwarding fixpoint:
// a function of the program that hands one of its own empty-interface parameters to
// such a parameter, or to a parameter a value already crosses out through, carries
// the crossing at that parameter of its own, and is then itself something a further
// function can forward to. The set grows until no function joins it.
func newBoundary(in *Input, destination func(fn *types.Func, at int) bool) *boundary {
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
				if !b.reaches(forwarded.to, forwarded.at, destination) {
					continue
				}
				joined = b.hold(candidates[i].at, forwarded.own) || joined
			}
		}
	}
	for pos := range b.forwards {
		slices.Sort(b.forwards[pos])
	}
	return b
}

// crossing reports whether the argument at position arg of one call carries a value
// out of the analysed program, and names the callee it leaves through. It is the
// whole of the crossing test: the callee is a function or method the program does not
// declare, or one of its own that forwards the parameter on; the parameter the
// argument supplies is typed as the empty interface, the variadic parameter included;
// and the value is a struct or a pointer to one, which is the shape a consumer
// reading a value by name reads the members of.
//
// A slice, a map or a channel of structs is not one, because what crosses is the
// value the argument's own type describes.
func (b *boundary) crossing(info *types.Info, call *ast.CallExpr, arg int) (*types.Func, bool) {
	if arg >= len(call.Args) {
		return nil, false
	}
	fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc {
		return nil, false
	}
	sinks, reaches := b.sinks(fn)
	if !reaches {
		return nil, false
	}
	at, supplies := parameterAt(fn.Signature(), arg)
	if !supplies || !slices.Contains(sinks, at) {
		return nil, false
	}
	flows := info.TypeOf(call.Args[arg])
	if flows == nil || !carriesStruct(flows) {
		return nil, false
	}
	return fn, true
}

// sinks are the parameter positions of one function at which a value leaves the
// analysis, and false where none does.
//
// A function the program does not declare carries them at every parameter typed as
// the empty interface, because its body is not in the program. A function the program
// declares carries the ones the forwarding set holds for it, which is where it hands a
// parameter of its own on. A package whose destinations the vocabulary names one by one
// carries none: the rule that names it records the call, and two spellings of one
// destination are two records of one fact.
func (b *boundary) sinks(fn *types.Func) ([]int, bool) {
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
	sinks := erasedParameters(fn.Signature())
	return sinks, len(sinks) > 0
}

// reaches reports whether one parameter of one function is somewhere a value leaves
// the program through: a destination the calling class names, or a parameter this
// boundary already carries.
func (b *boundary) reaches(fn *types.Func, at int, destination func(*types.Func, int) bool) bool {
	if destination != nil && destination(fn, at) {
		return true
	}
	sinks, held := b.sinks(fn)
	return held && slices.Contains(sinks, at)
}

// hold records that one parameter of the declaration written at declared is a
// destination, and reports whether that was not already known.
func (b *boundary) hold(declared token.Position, at int) bool {
	if slices.Contains(b.forwards[declared], at) {
		return false
	}
	b.forwards[declared] = append(b.forwards[declared], at)
	return true
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
// target's and every loaded consumer's, in one order so that two runs over one load
// read the same set. It is the one enumeration the boundary and the forwarding walks
// of the classes read, so what the program holds and what forwards a parameter are
// answered from one walk and a consumer's wrapper is found for every class at once.
//
// A declaration with no body is here: a function the program declares and implements
// elsewhere, in assembly or through a directive, is the program's own declaration, and
// it forwards nothing because there is no body to forward in.
//
// A function literal is not one: nothing names it, so no call to it resolves to a
// function a class can recognise.
func programFunctions(in *Input) []programFunction {
	var found []programFunction
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range sortedFiles(p, in.Result.Fset) {
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
	found := sortedPackages(r.Packages)
	for _, one := range r.Consumers {
		found = append(found, sortedPackages(one.Packages)...)
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
// is the shape a consumer reading a value by name reads the members of.
func carriesStruct(t types.Type) bool {
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
// exemption whose evidence a test file does not carry.
//
// The site is a file of the target, because a class records its evidence where the
// analysis can render it and the renderer answers for the target root alone. So
// Mode.ConsumerTestsProduction reaches no exemption: it classifies the references a
// loaded consumer's test file MAKES, and a consumer's test file is no site an
// exemption can carry. A production run therefore drops the evidence of the target's
// own test files under both classifications.
func holdsInMode(site token.Position, m graph.Mode) bool {
	if !m.Production {
		return true
	}
	_, test := graph.IsTestFile(site.Filename)
	return !test
}

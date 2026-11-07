package exempt

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/memory"
)

// decoders answers which parameters of a function only decode into the value they
// are given: the body uses the parameter at least once, and every use is an
// argument, unchanged or converted to an interface type, of a decoding entry point
// or of a parameter that only decodes, applied until no further function joins. A
// function the program does not declare is read from the source its package was
// compiled from, for this question alone. Answers are keyed by the loaded
// program's functions only, so they name nothing of a reading.
type decoders struct {
	flow     *encodingFlow
	answered map[*types.Func]map[int]reach
	pending  map[*types.Func]bool
	declared map[*types.Func]programFunction // the program's own

	// sources is, per package read, its functions by [declKey]: every package
	// outside memory pressure, the last one read under it, none once pressure
	// begins ([memory.OnPressureOf]).
	sources map[*types.Package]map[string]programFunction
	pressed func() bool
	mu      sync.Mutex
}

// newDecoders starts the answer over one configuration.
func newDecoders(f *encodingFlow) *decoders {
	d := &decoders{
		flow:     f,
		answered: make(map[*types.Func]map[int]reach),
		pending:  make(map[*types.Func]bool),
		declared: make(map[*types.Func]programFunction),
		sources:  make(map[*types.Package]map[string]programFunction),
		pressed:  memory.Pressed,
	}
	for _, one := range f.kept.in.functions {
		d.declared[one.fn.Origin()] = one
	}
	memory.OnPressureOf(d, (*decoders).drop)
	return d
}

// drop forgets every source d read.
func (d *decoders) drop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	clear(d.sources)
}

// at reports whether the parameter at one position of fn only decodes, and what the
// decoding entry points it reaches retain.
func (d *decoders) at(fn *types.Func, param int) (reach, bool) {
	key := fn.Origin()
	if held, known := d.answered[key]; known {
		r, decodes := held[param]
		return r, decodes
	}
	if d.pending[key] {
		return reach{}, false
	}
	d.pending[key] = true
	held := d.compute(fn)
	delete(d.pending, key)
	d.answered[key] = held
	r, decodes := held[param]
	return r, decodes
}

// compute answers every parameter of one function.
func (d *decoders) compute(fn *types.Func) map[int]reach {
	decl, info := d.body(fn)
	if decl == nil || decl.Body == nil || info == nil {
		return nil
	}
	defined, ok := info.Defs[decl.Name].(*types.Func)
	if !ok {
		return nil
	}
	sig := defined.Signature()
	if sig.Params().Len() != fn.Signature().Params().Len() {
		return nil
	}
	held := make(map[int]reach)
	for i := range sig.Params().Len() {
		if r, decodes := d.decodesInto(fn.Pkg(), info, decl.Body, sig.Params().At(i)); decodes {
			held[i] = r
		}
	}
	return held
}

// decodesInto reports whether every use of one parameter in a body of home is an
// argument a decoding entry point or a decoding parameter receives, and there is at
// least one.
func (d *decoders) decodesInto(home *types.Package, info *types.Info, body *ast.BlockStmt, param *types.Var) (reach, bool) {
	uses := 0
	for _, obj := range info.Uses {
		if obj == param {
			uses++
		}
	}
	if uses == 0 {
		return reach{}, false
	}
	decoded := 0
	var held reach
	ast.Inspect(body, func(n ast.Node) bool {
		if call, isCall := n.(*ast.CallExpr); isCall {
			count, r := d.decodedArguments(home, info, body, call, param)
			decoded += count
			held = held.union(r)
		}
		return true
	})
	return held, decoded == uses
}

// decodedArguments counts the arguments of one call that pass the parameter to a
// callee parameter that decodes into it, and what those callee parameters retain.
func (d *decoders) decodedArguments(home *types.Package, info *types.Info, body ast.Node, call *ast.CallExpr, param *types.Var) (int, reach) {
	callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
	if !isFunc {
		return 0, reach{}
	}
	callee, isFunc = loadedFunc(home, callee)
	if !isFunc {
		return 0, reach{}
	}
	decoded := 0
	var held reach
	for i, arg := range call.Args {
		if usedParameter(info, arg) != param {
			continue
		}
		at, supplies := parameterAt(callee.Signature(), i)
		if !supplies {
			continue
		}
		if r, decodes := d.receives(callSite{info: info, decl: body, call: call}, callee, at); decodes {
			decoded++
			held = held.union(r)
		}
	}
	return decoded, held
}

// receives reports whether one parameter of a callee, called at site, decodes into
// its argument.
func (d *decoders) receives(site callSite, callee *types.Func, at int) (reach, bool) {
	if dest, named := d.flow.encodingDestination(callee); named {
		d.flow.narrowStrict(callee, site.info, site.decl, site.call, &dest.reach)
		if decodingOnly(dest.reach) {
			return dest.reach, true
		}
		return reach{}, false
	}
	return d.at(callee, at)
}

// decodingOnly reports whether a destination fills a value's fields without reading
// them and resolves only decoding methods.
func decodingOnly(r reach) bool {
	return r.methods&^reachDecoding == 0 && !r.fields && len(r.outside) == 0 && len(r.contained) == 0
}

// readsFields reports whether the body of fn hands the parameter at one position, unchanged
// or converted to an interface type, to a destination that reads fields: a destination
// of this class that does, or a function of a package the format-verb class reads. A
// body the analysis cannot read hands it to none.
func (d *decoders) readsFields(fn *types.Func, at int) bool {
	decl, info := d.body(fn)
	if decl == nil || decl.Body == nil || info == nil {
		return false
	}
	defined, ok := info.Defs[decl.Name].(*types.Func)
	if !ok || at >= defined.Signature().Params().Len() {
		return false
	}
	param := defined.Signature().Params().At(at)
	reads := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if reads || !isCall {
			return !reads
		}
		callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
		if !isFunc || callee.Pkg() == nil || !slices.ContainsFunc(call.Args, func(arg ast.Expr) bool {
			return usedParameter(info, arg) == param
		}) {
			return true
		}
		dest, named := d.flow.encodingDestination(callee)
		d.flow.narrowStrict(callee, info, decl, call, &dest.reach)
		reads = (named && dest.reach.fields) || destinations[callee.Pkg().Path()].prints != nil
		return !reads
	})
	return reads
}

// loadedFunc is fn as the loaded home declares it, where fn is a function of home
// that a reading of home's source checked anew, and fn itself where fn is no such
// function. False where the loaded home declares no counterpart: an answer keyed by
// a reading's function would keep the reading alive.
func loadedFunc(home *types.Package, fn *types.Func) (*types.Func, bool) {
	pkg := fn.Pkg()
	if home == nil || pkg == nil || pkg == home || pkg.Path() != home.Path() {
		return fn, true
	}
	fn = fn.Origin()
	recv := fn.Signature().Recv()
	if recv == nil {
		loaded, declared := home.Scope().Lookup(fn.Name()).(*types.Func)
		return loaded, declared
	}
	base := recv.Type()
	if ptr, isPtr := base.(*types.Pointer); isPtr {
		base = ptr.Elem()
	}
	named, isNamed := types.Unalias(base).(*types.Named)
	if !isNamed {
		return nil, false
	}
	loaded, declared := home.Scope().Lookup(named.Obj().Name()).(*types.TypeName)
	if !declared {
		return nil, false
	}
	method, _, _ := types.LookupFieldOrMethod(loaded.Type(), true, home, fn.Name())
	m, isFunc := method.(*types.Func)
	if !isFunc {
		return nil, false
	}
	return m.Origin(), true
}

// usedParameter is the variable an argument names, unchanged or converted to an
// interface type, and nil for any other argument.
func usedParameter(info *types.Info, arg ast.Expr) *types.Var {
	arg = ast.Unparen(arg)
	if call, isCall := arg.(*ast.CallExpr); isCall && len(call.Args) == 1 {
		if tv, held := info.Types[call.Fun]; held && tv.IsType() && types.IsInterface(tv.Type) {
			arg = ast.Unparen(call.Args[0])
		}
	}
	ident, isIdent := arg.(*ast.Ident)
	if !isIdent {
		return nil
	}
	v, _ := info.Uses[ident].(*types.Var)
	return v
}

// body is the declaration and type information of one function: the program's own
// where it declares the function, and otherwise the source its package was compiled
// from ([load.Sources.Read]).
func (d *decoders) body(fn *types.Func) (*ast.FuncDecl, *types.Info) {
	if one, held := d.declared[fn.Origin()]; held {
		return one.decl, one.info
	}
	if fn.Pkg() == nil {
		return nil, nil
	}
	at := d.flow.kept.in.Result.Fset.PositionFor(fn.Pos(), false)
	if !at.IsValid() || at.Filename == "" {
		return nil, nil
	}
	one, held := d.source(fn.Pkg())[declKey(filepath.Base(at.Filename), at.Offset)]
	if !held {
		return nil, nil
	}
	return one.decl, one.info
}

// source is every function declaration of one package's source by [declKey].
func (d *decoders) source(pkg *types.Package) map[string]programFunction {
	d.mu.Lock()
	held, known := d.sources[pkg]
	d.mu.Unlock()
	if known {
		return held
	}
	held = make(map[string]programFunction)
	defer func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.pressed() {
			clear(d.sources)
		}
		d.sources[pkg] = held
	}()
	source, ok := d.flow.kept.in.sources.Read(pkg)
	if !ok {
		return held
	}
	for _, file := range source.Files {
		for _, decl := range file.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc {
				continue
			}
			fn, defines := source.Info.Defs[fd.Name].(*types.Func)
			if !defines {
				continue
			}
			one := programFunction{info: source.Info, decl: fd, fn: fn}
			at := source.Fset.PositionFor(fd.Name.Pos(), false)
			held[declKey(filepath.Base(at.Filename), at.Offset)] = one
		}
	}
	return held
}

// declKey names a declaration by the file and byte its name is written at, before
// any line directive: an object the load checked for its declarations alone is of
// another parse, and two declarations a line directive renders at one line are two.
func declKey(file string, offset int) string {
	return file + ":" + strconv.Itoa(offset)
}

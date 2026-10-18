package exempt

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
)

// FilledFields is every field the last Compute over in found that a function or
// method outside the analysed program fills from a document and may read, and nothing
// else retains under that class, each with the site of the call. It retains nothing:
// a finding about such a field is reported at the class possible.
func FilledFields(in *Input) []Exemption {
	return in.filled
}

// decoders answers which parameters of a function only decode into the value they
// are given: the body uses the parameter at least once, and every use is an
// argument, unchanged or converted to an interface type, of a decoding entry point
// or of a parameter that only decodes, applied until no further function joins. A
// function the program does not declare is read from the source its package was
// compiled from, for this question alone.
type decoders struct {
	flow     *encodingFlow
	answered map[*types.Func]map[int]reach
	pending  map[*types.Func]bool
	declared map[*types.Func]programFunction               // the program's and every read source's
	sources  map[*types.Package]map[string]programFunction // per source read, its functions by [declKey]
}

// newDecoders starts the answer over one configuration.
func newDecoders(f *encodingFlow) *decoders {
	d := &decoders{
		flow:     f,
		answered: make(map[*types.Func]map[int]reach),
		pending:  make(map[*types.Func]bool),
		declared: make(map[*types.Func]programFunction),
		sources:  make(map[*types.Package]map[string]programFunction),
	}
	for _, one := range programFunctions(f.kept.in) {
		d.declared[one.fn.Origin()] = one
	}
	return d
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
		if r, decodes := d.decodesInto(info, decl.Body, sig.Params().At(i)); decodes {
			held[i] = r
		}
	}
	return held
}

// decodesInto reports whether every use of one parameter in a body is an argument a
// decoding entry point or a decoding parameter receives, and there is at least one.
func (d *decoders) decodesInto(info *types.Info, body *ast.BlockStmt, param *types.Var) (reach, bool) {
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
			count, r := d.decodedArguments(info, call, param)
			decoded += count
			held = held.union(r)
		}
		return true
	})
	return held, decoded == uses
}

// decodedArguments counts the arguments of one call that pass the parameter to a
// callee parameter that decodes into it, and what those callee parameters retain.
func (d *decoders) decodedArguments(info *types.Info, call *ast.CallExpr, param *types.Var) (int, reach) {
	callee, isFunc := resolveObject(info, call.Fun).(*types.Func)
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
		if r, decodes := d.receives(callee, at); decodes {
			decoded++
			held = held.union(r)
		}
	}
	return decoded, held
}

// receives reports whether one parameter of a callee decodes into its argument.
func (d *decoders) receives(callee *types.Func, at int) (reach, bool) {
	if dest, named := d.flow.encodingDestination(callee); named {
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
		reads = (named && dest.reach.fields) || formattingPackages[callee.Pkg().Path()]
		return !reads
	})
	return reads
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
// from ([load.Result.Source]).
func (d *decoders) body(fn *types.Func) (*ast.FuncDecl, *types.Info) {
	if one, held := d.declared[fn.Origin()]; held {
		return one.decl, one.info
	}
	if fn.Pkg() == nil {
		return nil, nil
	}
	at := d.flow.kept.in.Result.Fset.Position(fn.Pos())
	if !at.IsValid() || at.Filename == "" {
		return nil, nil
	}
	one, held := d.source(fn.Pkg())[declKey(filepath.Base(at.Filename), at.Line)]
	if !held {
		return nil, nil
	}
	return one.decl, one.info
}

// source is every function declaration of one package's source, indexed on first
// use by its file and the line of its name.
func (d *decoders) source(pkg *types.Package) map[string]programFunction {
	if held, known := d.sources[pkg]; known {
		return held
	}
	held := make(map[string]programFunction)
	d.sources[pkg] = held
	source, ok := d.flow.kept.in.Result.Source(pkg)
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
			d.declared[fn] = one
			at := source.Fset.Position(fd.Name.Pos())
			held[declKey(filepath.Base(at.Filename), at.Line)] = one
		}
	}
	return held
}

// declKey names a declaration by its file and the line of its name, which is what
// an object the load checked for its declarations alone carries.
func declKey(file string, line int) string {
	return file + ":" + strconv.Itoa(line)
}

// formattingPackages are the packages whose functions the format-verb class reads,
// which print the fields of a value they are given.
var formattingPackages = map[string]bool{formatPackage: true, logPackage: true, testingPackage: true, slogPackage: true}

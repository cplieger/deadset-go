package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// methodReach is what a destination reads of the methods of a value it is given. A
// value that reaches two destinations keeps what both read, so each destination is
// one bit.
type methodReach uint16

// What a destination reads of a value's methods: nothing, every exported method the
// type declares, or the methods one destination resolves by name.
const (
	reachExportedMethods methodReach = 1 << 0
	reachJSONEncode      methodReach = 1 << 1
	reachJSONDecode      methodReach = 1 << 2
	reachXMLEncode       methodReach = 1 << 3
	reachXMLDecode       methodReach = 1 << 4
	reachGobEncode       methodReach = 1 << 5
	reachGobDecode       methodReach = 1 << 6
	reachLogValue        methodReach = 1 << 7

	// The two directions, so one entry point of a destination keeps its own.
	reachEncoding methodReach = reachJSONEncode | reachXMLEncode | reachGobEncode
	reachDecoding methodReach = reachJSONDecode | reachXMLDecode | reachGobDecode
)

// reach is what a destination retains of a value it is given: the methods it
// resolves by name, whether it reads fields, the packages outside the analysed
// program the value reaches, each of which retains the methods by which the value
// satisfies an interface that package can name, and the standard containers that
// store it, each of which retains only the methods by which it satisfies an
// interface of the container's own package.
type reach struct {
	outside   []*types.Package // ordered by path, each once
	contained []*types.Package // ordered by path, each once
	methods   methodReach
	fields    bool

	// keys are the tag keys of the encoders the fields are read through, where
	// every reader of the fields is such an encoder, which skips a field its key
	// tags exactly "-". Zero, the fields are read whatever their tags say.
	keys tagKeys

	// encoders are the encoding directions of the standard encoders the fields are
	// read through, where every reader of the fields is one, which walks no field of
	// a type that encodes itself ([selfEncoders]). Zero, the fields are read whatever
	// methods the type has.
	encoders methodReach

	// fills is whether a decoder outside the analysed program fills the fields
	// and may read them ([fillingNames]); a field it fills that production code never
	// writes is a filled field even where the decoder reads the fields.
	fills bool

	// xmlName is whether an XML encoder or decoder reads the value's XMLName field.
	xmlName bool

	// holders is whether a decoder reads a field of a pointer, interface or map
	// type the program writes, to decode into the value the field holds.
	holders bool

	// results is whether the destination also reads what each method it calls
	// returns, as a template engine evaluating a chain does.
	results bool

	// strict is whether a decoder refusing unknown members reads the fields
	// ([strictDecoders]), and layout whether a positional codec or a reflective field
	// walk does, which visits every field, exported or not: each reads a field it
	// visits whether or not anything writes it.
	strict bool
	layout bool

	// emits is whether an encoder, or a destination that may hand the value to one,
	// writes the fields to its output, where a field nothing writes is kept at the
	// class possible unless its tag leaves the zero value out ([tagKeys.omitsZero]).
	emits bool

	// opaque is whether a callee whose body the program does not hold reads the value,
	// which no destination row names and which may hand it to any encoder, so it
	// emits the fields and may call their IsZero methods ([encodingFlow.keepZeroMethod]).
	// emits is set with it.
	opaque bool
}

// tagKeys is a set of the struct tag keys an encoder names a field by.
type tagKeys uint8

// The tag keys an encoder skips a field by, each one bit. encoding/json/v2 has a bit
// of its own beside jsonKey, the key of encoding/json and its kin, because the two
// leave different zero values out by the same key ([tagKeys.omitsZero]).
const (
	jsonKey tagKeys = 1 << iota
	jsonV2Key
	xmlKey
	yamlKey
	tomlKey
)

// tagNames are the tag keys by name.
var tagNames = map[tagKeys]string{jsonKey: "json", jsonV2Key: "json", xmlKey: "xml", yamlKey: "yaml", tomlKey: "toml"}

// skips reports whether every encoder the fields are read through skips one field,
// its tag naming the key "-" for each of them. A tag of "-," names the key "-".
func (k tagKeys) skips(tag string) bool {
	if k == 0 {
		return false
	}
	st := reflect.StructTag(tag)
	for key, name := range tagNames {
		if k&key != 0 && st.Get(name) != "-" {
			return false
		}
	}
	return true
}

// union is what a value reaching both destinations retains.
func (r reach) union(other reach) reach {
	joined := reach{
		methods: r.methods | other.methods,
		fields:  r.fields || other.fields,
		xmlName: r.xmlName || other.xmlName,
		holders: r.holders || other.holders,
		fills:   r.fills || other.fills,
		results: r.results || other.results,
		strict:  r.strict || other.strict,
		layout:  r.layout || other.layout,
		emits:   r.emits || other.emits,
		opaque:  r.opaque || other.opaque,
	}
	joined.keys, joined.encoders = r.joinReaders(other)
	joined.outside = joinPackages(r.outside, other.outside)
	joined.contained = joinPackages(r.contained, other.contained)
	return joined
}

// joinReaders is the tag keys and the standard encoders of a value both reaches read
// the fields of: a reach that reads no field adds none, and one of zero, which names
// no encoder by its tag or reads the fields whatever methods the type has, zeroes it.
func (r reach) joinReaders(other reach) (tagKeys, methodReach) {
	switch {
	case !r.fields:
		return other.keys, other.encoders
	case !other.fields:
		return r.keys, r.encoders
	}
	var keys tagKeys
	var encoders methodReach
	if r.keys != 0 && other.keys != 0 {
		keys = r.keys | other.keys
	}
	if r.encoders != 0 && other.encoders != 0 {
		encoders = r.encoders | other.encoders
	}
	return keys, encoders
}

// joinPackages is the packages of two lists ordered by path, each once.
func joinPackages(held, other []*types.Package) []*types.Package {
	joined := slices.Clone(held)
	for _, pkg := range other {
		at, found := slices.BinarySearchFunc(joined, pkg.Path(), func(p *types.Package, pkgPath string) int {
			return strings.Compare(p.Path(), pkgPath)
		})
		if !found {
			joined = slices.Insert(joined, at, pkg)
		}
	}
	return joined
}

// covers reports whether r retains everything other does.
func (r reach) covers(other reach) bool {
	widened := r.union(other)
	return widened.methods == r.methods && widened.fields == r.fields && len(widened.outside) == len(r.outside) &&
		len(widened.contained) == len(r.contained) && widened.keys == r.keys && widened.xmlName == r.xmlName &&
		widened.holders == r.holders && widened.encoders == r.encoders && widened.fills == r.fills &&
		widened.results == r.results && widened.strict == r.strict && widened.layout == r.layout &&
		widened.emits == r.emits && widened.opaque == r.opaque
}

// storesOnly reports whether every destination of this reach is a standard
// container, which hands the value back unchanged and so reads nothing beneath it.
func (r reach) storesOnly() bool {
	return len(r.contained) > 0 && r.methods == 0 && !r.fields && !r.xmlName && !r.holders && !r.fills &&
		len(r.outside) == 0
}

// reads reports whether a destination of this reach reads one method of a defined
// type it walks.
func (r methodReach) reads(m *types.Func) bool {
	if r&reachExportedMethods != 0 && m.Exported() {
		return true
	}
	return r&methodsResolvedByName[m.Name()] != 0
}

// EncodingReflectionDetector records the encoding-reflection class: a type whose
// values reach a destination that reads them by name at run time keeps the members
// that destination reads, on the type and on every type reached from its fields,
// because the reference graph holds no edge to them. What a destination retains is
// [destination]'s, and every argument of a destination function flows. The walk
// reaches through an interface-typed field to the types the program stores in it
// ([storedTypes]), and a promoted method is retained where the embedded type is.
func EncodingReflectionDetector(in *Program) ([]Exemption, error) {
	f := &encodingFlow{
		conversions:  in.programConversions,
		readings:     in.Facts.Readings(),
		kept:         newRetention(in, vocab.EncodingReflection),
		filled:       newRetention(in, vocab.EncodingReflection),
		own:          programPaths(in),
		strict:       enabledSwitches(in),
		findsMethods: len(namingPackages(in, func(path string) map[string]bool { return destinations[path].finders })) > 0,
		walksFields:  len(namingPackages(in, func(path string) map[string]bool { return destinations[path].walkers })) > 0,
		assert:       in.assert,
		satisfying:   make(map[satisfactionKey]map[string]bool),
		methodIDs:    make(map[*types.Named]map[string]bool),
		decoded:      make(map[token.Position][]evidence),
		typeArgs:     in.Facts.Instantiations(),
		reflectWalks: make(map[*ast.FuncDecl]*reflectWalk),
	}
	f.templates = newTemplateOrigins(in)
	f.decode = newDecoders(f)
	f.edge = newBoundary(in, f.namedDestinationParameter, f.opaqueParameters, f.dynamicReach())
	f.readFlow()
	if err := f.solve(); err != nil {
		return nil, err
	}
	filled := f.filled.exemptions()
	for i := range filled {
		filled[i].Confidence = vocab.Possible
	}
	return append(f.kept.exemptions(), filled...), nil
}

// encodingFlow is the class over one loaded configuration: the facts it reads, the
// flow graph its one walk reads ([flowGraph]), and what solving the graph records.
type encodingFlow struct {
	typeArgs  *facts.Instantiations
	kept      *retention
	filled    *retention      // the fields kept only at the class possible ([unwrittenRead])
	own       map[string]bool // the program's package paths
	edge      *boundary
	assert    *assertable
	decode    *decoders
	templates *templateOrigins // which templates an execution's receiver holds
	readings  *facts.Readings

	// The tables [encodingFlow.index] fills from every arrival before any is
	// retained: the import closure of each outside package an arrival reaches, by
	// path, and what a walk of each type an arrival carries reads.
	closures    map[*types.Package]*importClosure
	walked      map[types.Type][]*types.Named
	selfEncoded map[selfEncodingKey]selfEncodedTypes

	satisfying map[satisfactionKey]map[string]bool // the method ids retained
	methodIDs  map[*types.Named]map[string]bool    // [declaredMethodIDs], by type

	// strict is the decoder packages a switch of which the program enables
	// ([enabledSwitches]).
	strict map[string]bool

	decoded map[token.Position][]evidence // the sites of the decoders that fill each field

	reflectWalks map[*ast.FuncDecl]*reflectWalk // each body's reflect.Value reading, built once
	flow         *valueFlow                     // the program's value flow, built on first use

	conversions []facts.Conversion // the program's, consumers' included
	unwritten   []unwrittenRead    // the fields a destination reads that no code writes
	executions  []typedExecution   // the executions the typed walk reads

	graph flowGraph

	// findsMethods is whether the program names a reflection method finder, and
	// walksFields whether it names a reflective field walker.
	findsMethods bool
	walksFields  bool
}

// solve retains what every arrival of the graph reaches, in the graph's order, then
// the fields a destination reads that only a decoder writes.
func (f *encodingFlow) solve() error {
	arrivals := make([]arrival, 0, len(f.graph.arguments)+len(f.graph.returns)+len(f.graph.typeArgs))
	for i := range f.graph.arguments {
		arrivals = append(arrivals, f.carried(&f.graph.arguments[i]))
	}
	arrivals = append(append(arrivals, f.graph.returns...), f.graph.typeArgs...)
	for i := range arrivals {
		arrivals[i].types = f.bound(arrivals[i].types)
	}
	f.index(arrivals)
	for i := range arrivals {
		if err := f.retain(&arrivals[i]); err != nil {
			return err
		}
	}
	for i := range f.graph.assertions {
		if err := f.fieldAssertion(&f.graph.assertions[i]); err != nil {
			return err
		}
	}
	if err := f.typeExecutions(); err != nil {
		return err
	}
	f.keepDecoded()
	return nil
}

// bound is every type a list of types stands for, each followed by the types its
// type parameters are instantiated with, because a value of a type parameter's type
// is a value of each type argument the program instantiates the parameter with.
func (f *encodingFlow) bound(carried []types.Type) []types.Type {
	found := make([]types.Type, 0, len(carried))
	for _, t := range carried {
		if t != nil {
			found = append(append(found, t), bindings(f.typeArgs, t)...)
		}
	}
	return found
}

// index fills the flow's tables from every arrival: the import closure of each
// outside package one reaches, the types a walk of each type one carries reads, and
// for an arrival read by standard encoders, the types that walk reads and enters,
// which is read from the first.
func (f *encodingFlow) index(arrivals []arrival) {
	f.closures = make(map[*types.Package]*importClosure)
	f.walked = make(map[types.Type][]*types.Named)
	f.selfEncoded = make(map[selfEncodingKey]selfEncodedTypes)
	for i := range arrivals {
		for _, pkg := range arrivals[i].d.reach.outside {
			f.closure(pkg)
		}
		for _, t := range arrivals[i].types {
			if _, known := f.walked[t]; !known {
				f.walked[t] = f.encoderReach(t)
			}
		}
	}
	for i := range arrivals {
		f.indexSelfEncoded(&arrivals[i])
	}
}

// indexSelfEncoded fills [encodingFlow.selfEncoded] for one arrival read by standard
// encoders.
func (f *encodingFlow) indexSelfEncoded(a *arrival) {
	directions := a.d.reach.encoders
	if directions == 0 {
		return
	}
	for _, t := range a.types {
		key := selfEncodingKey{t: t, directions: directions}
		if _, known := f.selfEncoded[key]; !known {
			f.selfEncoded[key] = f.selfEncodedReach(t, directions)
		}
	}
}

// namedDestinationParameter reports whether one parameter of one function is a
// destination this class names by itself, which every parameter of one is, and what
// that destination retains. It is the base case of the boundary's forwarding
// fixpoint.
func (f *encodingFlow) namedDestinationParameter(fn *types.Func, at int, site callSite) (reach, bool) {
	if d, found := f.encodingDestination(fn); found {
		narrowReflection(fn, site, &d)
		f.narrowStrict(fn, site.info, site.decl, site.call, &d.reach)
		return d.reach, true
	}
	if decoded, decodes := f.decode.at(fn, at); decodes {
		return decoded, true
	}
	if f.fills(fn, at) {
		filled := f.opaqueParameters(fn)[0].reach
		f.narrowStrict(fn, site.info, site.decl, site.call, &filled)
		return filled.filling(), true
	}
	return reach{}, false
}

// fills reports whether the empty-interface parameter at one position of fn fills its
// argument: fn is a function or method outside the analysed program, of a package no
// destination rule names, whose name says it fills its argument, and whose own body
// does not hand the parameter to a destination that reads fields
// ([decoders.readsFields]).
func (f *encodingFlow) fills(fn *types.Func, at int) bool {
	return f.fillsByName(fn, at) && !f.decode.readsFields(fn, at)
}

// fillsByName reports whether fn is a function or method outside the analysed
// program, of a package no destination rule names, whose name says it fills the
// empty-interface parameter at one position, whatever its body reads.
func (f *encodingFlow) fillsByName(fn *types.Func, at int) bool {
	pkg := fn.Pkg()
	return pkg != nil && !f.own[pkg.Path()] && !namedDestination(pkg.Path()) && hasAnyPrefix(fn.Name(), fillingNames[:]) &&
		slices.Contains(erasedParameters(fn.Signature()), at)
}

// filling is what a filling callee retains in place of the fields an opaque callee
// reads.
func (r reach) filling() reach {
	r.fields, r.fills, r.encoders, r.emits, r.opaque = r.strict || r.layout, true, 0, false, false
	if !r.strict {
		r.keys = 0
	}
	return r
}

// programPaths is the import path of every package of the analysed program.
func programPaths(in *Program) map[string]bool {
	held := make(map[string]bool)
	for _, p := range in.Facts.Program() {
		held[p.PkgPath] = true
	}
	return held
}

// destination is what one call is to the class: the clause an exemption records,
// what it reads of the methods of the value it is given beside its fields, and
// whether only a pointer argument flows into it.
type destination struct {
	// named is, for the execution of a template set the analysis reads whole
	// ([templateOrigins.executed]), the identifiers its templates name, the only
	// fields and, beside [printingMethods], methods it reads. Nil, the members are
	// read whatever their names.
	named map[string]bool

	detail   string
	reach    reach
	pointers bool
}

// calledResults is reached with every type the result of a method the destination
// calls on a reached type reaches, until no type joins.
func (f *encodingFlow) calledResults(reached []*types.Named, d *destination) []*types.Named {
	seen := make(map[*types.Named]bool, len(reached))
	for _, named := range reached {
		seen[named] = true
	}
	for i := 0; i < len(reached); i++ {
		for _, next := range f.resultsCalled(reached[i], d) {
			if !seen[next] {
				seen[next] = true
				reached = append(reached, next)
			}
		}
	}
	return reached
}

// resultsCalled is every type the results reach of the methods of one type the
// destination calls.
func (f *encodingFlow) resultsCalled(named *types.Named, d *destination) []*types.Named {
	var found []*types.Named
	for m := range named.Origin().Methods() {
		if !m.Exported() || (d.named != nil && !d.named[m.Name()]) {
			continue
		}
		for result := range m.Signature().Results().Variables() {
			found = append(found, f.encoderReach(result.Type())...)
		}
	}
	return found
}

// templateNames is [templateOrigins.executed]'s names for a call of fn in a file of
// dir, nil where the call is no template execution or its set is not read whole.
func (f *encodingFlow) templateNames(info *types.Info, dir string, fn *types.Func, call *ast.CallExpr) map[string]bool {
	if !destinations[fn.Pkg().Path()].executions[fn.Name()] {
		return nil
	}
	return f.templates.executed(info, dir, call)
}

// narrowReflection drops the methods a reflect destination reads where what the call
// returns reaches no method finder, and its walk of every field where that reaches no
// field walker ([reachesFrom]).
func narrowReflection(fn *types.Func, site callSite, d *destination) {
	row := destinations[fn.Pkg().Path()]
	if row.finders == nil || site.call == nil {
		return
	}
	if d.reach.methods != 0 && !reachesFrom(site.info, site.decl, site.call, row.finders) {
		d.reach.methods = 0
	}
	if d.reach.layout && !reachesFrom(site.info, site.decl, site.call, row.walkers) {
		d.reach.layout = false
	}
}

// reachesFrom reports whether what one reflect call returns may reach a call of one
// of the names: false only where the call heads a chain of reflect method calls, none
// of them named, whose last result is neither a reflect.Value nor a reflect.Type, or
// is one a local variable of decl takes whose every use heads such a chain.
func reachesFrom(info *types.Info, decl ast.Node, call *ast.CallExpr, names map[string]bool) bool {
	return chainReaches(info, decl, call, names, map[types.Object]bool{})
}

// chainReaches is [reachesFrom] for the chain start heads, seen holding the local
// variables already followed.
func chainReaches(info *types.Info, decl ast.Node, start ast.Expr, names map[string]bool, seen map[types.Object]bool) bool {
	path := pathTo(decl, start)
	at, end, finds := chainEnd(info, path, start, names)
	if finds || !isReflectHandle(info.TypeOf(at)) {
		return finds
	}
	local := assignedLocal(info, path[:end], at)
	if local == nil {
		return true
	}
	if seen[local] {
		return false
	}
	seen[local] = true
	found := false
	ast.Inspect(decl, func(n ast.Node) bool {
		if id, isIdent := n.(*ast.Ident); isIdent && !found && info.Uses[id] == local {
			found = chainReaches(info, decl, id, names, seen)
		}
		return !found
	})
	return found
}

// chainEnd follows the chain of reflect method calls start heads along path, the
// nodes from the enclosing declaration down to start: it returns the chain's last
// expression and its index in path, or finds true where a call of it is named.
func chainEnd(info *types.Info, path []ast.Node, start ast.Expr, names map[string]bool) (at ast.Expr, end int, finds bool) {
	at, end = start, len(path)-1
	for i := len(path) - 2; i >= 1; i-- {
		if _, paren := path[i].(*ast.ParenExpr); paren {
			continue
		}
		sel, selects := path[i].(*ast.SelectorExpr)
		next, calls := path[i-1].(*ast.CallExpr)
		if !selects || !calls || ast.Unparen(sel.X) != at || ast.Unparen(next.Fun) != sel {
			break
		}
		if names[sel.Sel.Name] {
			return at, end, true
		}
		at, end, i = next, i-1, i-1
		if !isReflectHandle(info.TypeOf(at)) {
			break
		}
	}
	return at, end, false
}

// assignedLocal is the local variable an assignment or a variable declaration, the
// nearest of above outside parentheses, gives value to, nil where it gives it to no
// one local variable.
func assignedLocal(info *types.Info, above []ast.Node, value ast.Expr) types.Object {
	at := len(above) - 1
	for at >= 0 {
		if _, paren := above[at].(*ast.ParenExpr); !paren {
			break
		}
		at--
	}
	if at < 0 {
		return nil
	}
	name := assignedName(above[at], value)
	if name == nil {
		return nil
	}
	obj := info.Defs[name]
	if obj == nil {
		obj = info.Uses[name]
	}
	v, isVar := obj.(*types.Var)
	if !isVar || v.Pkg() == nil || v.Parent() == v.Pkg().Scope() || v.IsField() {
		return nil
	}
	return v
}

// assignedName is the identifier stmt, an assignment or a value specification,
// gives value to, nil where it gives it to none.
func assignedName(stmt ast.Node, value ast.Expr) *ast.Ident {
	is := func(e ast.Expr) bool { return ast.Unparen(e) == value }
	switch n := stmt.(type) {
	case *ast.AssignStmt:
		if k := slices.IndexFunc(n.Rhs, is); k >= 0 && len(n.Lhs) == len(n.Rhs) {
			name, _ := n.Lhs[k].(*ast.Ident)
			return name
		}
	case *ast.ValueSpec:
		if k := slices.IndexFunc(n.Values, is); k >= 0 && len(n.Names) == len(n.Values) {
			return n.Names[k]
		}
	}
	return nil
}

// isReflectHandle reports whether t is reflect.Value or reflect.Type.
func isReflectHandle(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == reflectPackage &&
		(named.Obj().Name() == "Value" || named.Obj().Name() == "Type")
}

// pathTo is the chain of nodes from root down to target, both included, or nil
// where root does not hold target.
func pathTo(root, target ast.Node) []ast.Node {
	var stack, found []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		if n == target {
			found = slices.Clone(stack)
		}
		return true
	})
	return found
}

// readCall reads the arguments one call of the program hands to something that
// reads them, for each function the call reaches ([facts.Callees.Of]): a
// destination, and otherwise a parameter that only decodes or one a value leaves
// the analysis through, which every argument of a call that may reach a function
// the analysis does not know is.
func (f *encodingFlow) readCall(info *types.Info, decl ast.Decl, enclosing *types.Func, dir string, call *ast.CallExpr) {
	callees, dynamic := f.kept.in.callees(info, call)
	for _, callee := range callees {
		f.readCallee(info, decl, enclosing, dir, call, callee)
	}
	if dynamic {
		f.readDynamic(info, enclosing, call)
	}
}

// readCallee is [encodingFlow.readCall] for one function the call reaches.
func (f *encodingFlow) readCallee(info *types.Info, decl ast.Decl, enclosing *types.Func, dir string, call *ast.CallExpr, callee facts.Callee) {
	fn := callee.Fn
	if d, reaches := f.encodingDestination(fn); reaches {
		narrowReflection(fn, callSite{info: info, decl: decl, call: call}, &d)
		f.narrowStrict(fn, info, decl, call, &d.reach)
		d.named = f.templateNames(info, dir, fn, call)
		f.noteExecution(info, dir, fn, call, callee)
		f.readArguments(info, enclosing, calledSignature(info, fn, call), call.Args, &d)
		return
	}
	for i, arg := range call.Args {
		if at, supplies := argumentParameter(callee, i); supplies && !f.edge.declares(fn) {
			if decoded, decodes := f.decode.at(fn, at); decodes {
				d := destination{detail: "decoded by " + fn.FullName(), reach: decoded}
				f.readArgument(info, enclosing, arg, &d, decodedArgument)
				continue
			}
		}
		f.readCrossing(info, decl, enclosing, call, callee, i)
	}
}

// readDynamic reads each argument a call hands to an empty-interface parameter of
// a function the analysis does not know, where the value leaves the analysis as at
// a callee outside the program, read by every package outside it.
func (f *encodingFlow) readDynamic(info *types.Info, enclosing *types.Func, call *ast.CallExpr) {
	for i, arg := range call.Args {
		if !dynamicallyErased(info, call, i) {
			continue
		}
		d := destination{detail: "passed to " + types.ExprString(call.Fun), reach: f.edge.dynamic}
		d.reach.fills = isPointer(info.TypeOf(arg))
		f.readArgument(info, enclosing, arg, &d, crossingArgument)
	}
}

// readArgument adds one argument a callee reads to the graph.
func (f *encodingFlow) readArgument(info *types.Info, enclosing *types.Func, arg ast.Expr, d *destination, rule argumentRule) {
	f.graph.arguments = append(f.graph.arguments, argumentSite{info: info, enclosing: enclosing, arg: arg, d: *d, rule: rule})
}

// readCrossing reads the argument at position i of one call where it carries a
// value out of the analysed program, which the boundary's crossing test decides.
// What is retained is what the boundary carries for the callee, and the detail names
// the immediate callee, so a wrapper's caller reads the wrapper's name.
func (f *encodingFlow) readCrossing(info *types.Info, decl ast.Decl, enclosing *types.Func, call *ast.CallExpr, callee facts.Callee, i int) {
	out, sinks := f.edge.sinkOf(callee, i)
	if !sinks {
		return
	}
	f.narrowStrict(out.callee, info, decl, call, &out.reach)
	arg := call.Args[i]
	d := destination{detail: "passed to " + out.callee.FullName(), reach: out.reach}
	if at, supplies := argumentParameter(callee, i); supplies && !f.edge.declares(out.callee) {
		switch {
		case f.fills(out.callee, at):
			d = destination{detail: "decoded by " + out.callee.FullName(), reach: out.reach.filling()}
		case f.fillsByName(out.callee, at), isPointer(info.TypeOf(arg)):
			// A callee outside the program handed a pointer may fill the value, as a
			// row scanner or a binder of any name does.
			d.reach.fills = true
		}
	}
	f.readArgument(info, enclosing, arg, &d, crossingArgument)
}

// carried is the arrival one argument is, with the types its rule carries: for a
// destination, its own type, the types the program stores in a container it names
// ([storedTypes.argument], [storedTypes.value]) and the ones it reaches of its own
// ([encodingFlow.ownTypes]); for a decoding parameter, its own type; for a crossing,
// its own struct value, the types it reaches of its own, each struct value an
// interface-typed local or call result holds ([storedTypes.held]), an empty-interface
// parameter's values crossing at its callers' sites, and what a container of interface
// values it names holds, a type parameter's type counting where its argument is one.
func (f *encodingFlow) carried(site *argumentSite) arrival {
	info, enclosing, arg := site.info, site.enclosing, site.arg
	a := arrival{d: site.d, at: arg.Pos()}
	own := info.TypeOf(arg)
	stored := f.graph.stored
	switch site.rule {
	case readArgument:
		a.types = slices.Concat([]types.Type{own}, stored.argument(info, enclosing, arg), stored.value(info, enclosing, arg),
			f.ownTypes(info, arg))
	case decodedArgument:
		a.types = []types.Type{own}
	case crossingArgument:
		a.types = f.ownTypes(info, arg)
		for _, held := range slices.Concat([]types.Type{own}, stored.held(info, enclosing, arg), stored.argument(info, enclosing, arg)) {
			if held != nil && slices.ContainsFunc(f.bound([]types.Type{held}), carriesStruct) {
				a.types = append(a.types, held)
			}
		}
	}
	return a
}

// isPointer reports whether t is a pointer type.
func isPointer(t types.Type) bool {
	_, pointer := types.Unalias(t).(*types.Pointer)
	return pointer
}

// ownTypes is what one argument of a destination reaches beyond its static type:
// the types of the values its own composite literals write into interface-typed
// positions ([literalValues]), and for an argument of a defined interface type every
// type the program converts to that interface. A conversion to an interface type
// with no name, any among them, adds nothing, and neither does one to error, which
// is predeclared rather than defined (Go specification, "Types").
func (f *encodingFlow) ownTypes(info *types.Info, arg ast.Expr) []types.Type {
	found := literalValues(info, arg)
	named, defined := types.Unalias(info.TypeOf(arg)).(*types.Named)
	if defined && named.Obj().Pkg() != nil && types.IsInterface(named) {
		found = append(found, f.convertedTo(named)...)
	}
	return found
}

// convertedTo is every concrete type the program converts to one defined interface
// type, at the sites the program and its loaded consumers convert ([Program]), the
// interface of another module's reading of its package included.
func (f *encodingFlow) convertedTo(iface *types.Named) []types.Type {
	var found []types.Type
	for i := range f.conversions {
		if c := &f.conversions[i]; f.readings.Same(c.Interface, iface) {
			found = append(found, c.From)
		}
	}
	return found
}

// literalValues is the type of each value one expression's composite literal writes
// into an interface-typed position, a struct field, a slice or array element, or a
// map key or value, read through parentheses, conversions and the address operator,
// and so on through each composite literal it nests at any position.
func literalValues(info *types.Info, expr ast.Expr) []types.Type {
	lit, isLiteral := literalOf(info, expr).(*ast.CompositeLit)
	if !isLiteral {
		return nil
	}
	t := info.TypeOf(lit)
	if t == nil {
		return nil
	}
	var found []types.Type
	write := func(position types.Type, value ast.Expr) {
		if held := info.TypeOf(value); position != nil && types.IsInterface(position) && held != nil && !types.IsInterface(held) {
			found = append(found, held)
		}
		found = append(found, literalValues(info, value)...)
	}
	under := types.Unalias(t).Underlying()
	if p, pointer := under.(*types.Pointer); pointer {
		under = p.Elem().Underlying()
	}
	for i, elt := range lit.Elts {
		key, value := ast.Expr(nil), elt
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			key, value = kv.Key, kv.Value
		}
		switch u := under.(type) {
		case *types.Struct:
			var set types.Type
			if field := literalField(info, u, elt, i); field != nil {
				set = field.Type()
			}
			write(set, value)
		case *types.Map:
			write(u.Key(), key)
			write(u.Elem(), value)
		case *types.Slice:
			write(u.Elem(), value)
		case *types.Array:
			write(u.Elem(), value)
		}
	}
	return found
}

// literalOf is the expression one argument holds, read through parentheses, type
// conversions and the address operator.
func literalOf(info *types.Info, expr ast.Expr) ast.Expr {
	for {
		switch e := ast.Unparen(expr).(type) {
		case *ast.UnaryExpr:
			if e.Op != token.AND {
				return e
			}
			expr = e.X
		case *ast.CallExpr:
			tv, held := info.Types[e.Fun]
			if !held || !tv.IsType() || len(e.Args) != 1 {
				return e
			}
			expr = e.Args[0]
		default:
			return e
		}
	}
}

// readArguments reads the arguments of one destination call of signature sig in
// the body of enclosing. An argument for a context parameter carries nothing
// ([isContext]).
func (f *encodingFlow) readArguments(info *types.Info, enclosing *types.Func, sig *types.Signature, args []ast.Expr, d *destination) {
	for i, arg := range args {
		if contextParameter(sig, i) {
			continue
		}
		// The argument's own type is what flows, which for a parameter typed as
		// an interface is the type written at the call rather than the interface
		// the value is converted to on the way in.
		at := info.TypeOf(arg)
		if at == nil {
			continue
		}
		if _, pointer := types.Unalias(at).(*types.Pointer); d.pointers && !pointer {
			continue
		}
		f.readArgument(info, enclosing, arg, d, readArgument)
	}
}

// calledSignature is the signature a call's arguments bind to: the callee
// expression's own, which for a method expression lists the receiver first, else
// fn's.
func calledSignature(info *types.Info, fn *types.Func, call *ast.CallExpr) *types.Signature {
	if sig, ok := types.Unalias(info.TypeOf(call.Fun)).(*types.Signature); ok {
		return sig
	}
	return fn.Signature()
}

// contextParameter reports whether the argument at one position of a call of sig is
// passed for a context.Context parameter ([isContext]).
func contextParameter(sig *types.Signature, at int) bool {
	return at < sig.Params().Len() && isContext(sig.Params().At(at).Type())
}

// isContext reports whether t is context.Context, which a destination hands to a
// handler that reads it by key and never walks by reflection.
func isContext(t types.Type) bool {
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}

// returnDestination is what reads the values one declaration returns at an
// interface-typed result where it is a method that implements a method of an
// exported interface an outside package declares: code outside the analysis calls
// the method through that interface and receives the value, so the value reaches
// that package.
func (f *encodingFlow) returnDestination(decl ast.Decl, fn *types.Func) (destination, bool) {
	fd, isFunc := decl.(*ast.FuncDecl)
	if fn == nil || !isFunc || fd.Recv == nil || fd.Body == nil {
		return destination{}, false
	}
	answered, implements := f.outsideMethod(fn)
	if !implements {
		return destination{}, false
	}
	return destination{
		detail: "returned through " + answered.FullName(),
		reach: reach{
			methods: reachEncoding | reachDecoding, fields: true, outside: []*types.Package{answered.Pkg()},
			emits: true, opaque: true,
		},
	}, true
}

// outsideMethod is the method of an exported interface declared outside the program
// that one method of the program implements, where its results hold an interface.
func (f *encodingFlow) outsideMethod(fn *types.Func) (*types.Func, bool) {
	sig := fn.Signature()
	if !slices.ContainsFunc(slices.Collect(sig.Results().Variables()), func(v *types.Var) bool { return types.IsInterface(v.Type()) }) {
		return nil, false
	}
	recv := sig.Recv().Type()
	for _, candidate := range f.kept.in.Facts.OutsideMethods(fn.Name()) {
		if !candidate.Consumer && facts.HoldsValues(candidate.Interface) && f.readings.Implements(recv, candidate.Interface) {
			return candidate.Method, true
		}
	}
	return nil, false
}

// returnedValues reads the values one return statement of a method hands back at
// an interface-typed result.
func (f *encodingFlow) returnedValues(info *types.Info, fn *types.Func, ret *ast.ReturnStmt, d *destination) {
	results := fn.Signature().Results()
	if len(ret.Results) != results.Len() {
		return
	}
	for i, value := range ret.Results {
		at := info.TypeOf(value)
		if types.IsInterface(results.At(i).Type()) && at != nil && carriesStruct(at) {
			f.graph.returns = append(f.graph.returns, arrival{d: *d, types: []types.Type{at}, at: value.Pos()})
		}
	}
}

// retain records what every defined type a destination walking each type one
// arrival carries reads keeps, and the fields of each struct type with no name the
// arrival's type is composed of, which are symbols of the type that holds it.
func (f *encodingFlow) retain(a *arrival) error {
	if len(a.types) == 0 {
		return nil
	}
	site, err := f.kept.site(a.at)
	if err != nil {
		return err
	}
	d := &a.d
	for _, one := range a.types {
		reached, encoded := f.walked[one], map[string]bool(nil)
		if d.reach.encoders != 0 {
			held := f.selfEncoded[selfEncodingKey{t: one, directions: d.reach.encoders}]
			reached, encoded = held.found, held.encoded
		}
		if d.reach.storesOnly() {
			reached = concreteTypes(namedTypesReached(one))
		} else {
			_, unnamed := typesReached(one)
			for _, st := range unnamed {
				f.fields(st, &site, d)
			}
		}
		if d.reach.results {
			reached = f.calledResults(slices.Clone(reached), d)
		}
		for _, named := range reached {
			f.members(named, &site, d, encoded[resolve.Spell(named, nil)])
		}
	}
	return nil
}

// members records what one destination reads of one defined type: the methods it
// resolves by name, the methods an outside package can name, and, where it reads
// fields and the type does not encode itself, the exported fields and every field
// carrying a tag, because a tagged field is named by its tag and not by its visibility.
func (f *encodingFlow) members(named *types.Named, site *evidence, d *destination, encodesItself bool) {
	origin := named.Origin()
	every, satisfying := f.outsideMethods(named, d.reach)
	for m := range origin.Methods() {
		resolved := d.reach.methods.reads(m) && (d.named == nil || d.named[m.Name()] || printingMethods[m.Name()])
		if resolved || (every && m.Exported()) || satisfying[m.Id()] {
			f.kept.record(m, site, d.detail)
		}
	}
	st, isStruct := origin.Underlying().(*types.Struct)
	switch {
	case !isStruct:
	case encodesItself:
		f.keepPromoting(origin, site, d)
	default:
		f.fields(st, site, d)
	}
}

// fields records the fields of one struct a destination reads, and those of each
// struct type with no name a field it visits holds, whose fields are symbols of the
// type that holds the struct. An exported, a tagged and an embedded struct field are
// visited (https://pkg.go.dev/encoding/json#Marshal), and every field where the
// destination reads the struct's layout.
func (f *encodingFlow) fields(st *types.Struct, site *evidence, d *destination) {
	for i := range st.NumFields() {
		field := st.Field(i)
		visited := d.reach.layout ||
			(field.Exported() || st.Tag(i) != "" || embedsStruct(field)) && !d.reach.keys.skips(st.Tag(i))
		f.noteDecoded(field, visited, site, d)
		emits := d.reach.emits && !d.reach.keys.omitsZero(field.Type(), st.Tag(i))
		unwritten := unwrittenRead{field: field, site: *site, detail: d.detail, emits: emits}
		switch {
		case f.reads(field, st.Tag(i), visited, site, d):
			f.kept.record(field, site, d.detail)
		case d.reach.fills && visited:
			f.filled.record(field, site, d.detail)
			if f.readsAny(field, visited, d) {
				unwritten.fills = true
				f.unwritten = append(f.unwritten, unwritten)
			}
		case f.readsAny(field, visited, d):
			f.unwritten = append(f.unwritten, unwritten)
		}
		if !visited {
			continue
		}
		f.keepZeroMethod(field.Type(), st.Tag(i), site, d)
		_, inner := typesReached(field.Type())
		for _, held := range inner {
			f.fields(held, site, d)
		}
	}
}

// reads reports whether one destination reads one field: a field it visits where it
// reads fields and, for a template engine, a template names, an XML entry point's
// XMLName, a field whose tag an outside package it reaches reads by ([taggedReads]),
// or a written holder a decoder fills through. A field it visits is read only where
// production code writes it ([fieldWrites.forSite]), the field is embedded, which a
// write of a promoted field writes through, or the destination reads the set of fields
// itself ([reach.strict], [reach.layout]): any other reader of an unwritten field
// reads a value no code of the program gave it.
func (f *encodingFlow) reads(field *types.Var, tag string, visited bool, site *evidence, d *destination) bool {
	switch {
	case f.readsAny(field, visited, d) && (field.Embedded() || d.reach.strict || d.reach.layout ||
		f.graph.writes.forSite(site, f.kept.in.Mode.Production)[f.edge.sites.of(field)]):
		return true
	case d.reach.xmlName && field.Name() == "XMLName" && isXMLName(field.Type()):
		return true
	case readsByTag(d.reach.outside, tag):
		return true
	default:
		return d.reach.holders && visited && holdsValue(field.Type()) && f.graph.writes.assigned[f.edge.sites.of(field)]
	}
}

// readsAny reports whether one destination reads one field it visits, written or
// not.
func (*encodingFlow) readsAny(field *types.Var, visited bool, d *destination) bool {
	return d.reach.fields && visited && (d.named == nil || d.named[field.Name()] || field.Embedded())
}

// unwrittenRead is one field a destination reads that no code the destination's site
// stands for writes, kept where a decoder fills it ([encodingFlow.keepDecoded]). fills
// is whether the reader may fill the field itself, as an outside callee handed a
// pointer may, and emits whether the reader writes the field, zero value and all, to
// its output, where deleting the field drops a key some reader of that output may need.
type unwrittenRead struct {
	field  *types.Var
	detail string
	site   evidence
	fills  bool
	emits  bool
}

// noteDecoded records the site of a decoding destination that fills a field: a
// decoder, a function that only decodes into its argument, or a filling callee.
func (f *encodingFlow) noteDecoded(field *types.Var, visited bool, site *evidence, d *destination) {
	if !visited || !d.reach.holders && !d.reach.fills {
		return
	}
	at := f.edge.sites.of(field)
	if !slices.Contains(f.decoded[at], *site) {
		f.decoded[at] = append(f.decoded[at], *site)
	}
}

// keepDecoded keeps each field a destination reads that no code writes where a decoder
// fills it, because the value then carries what the decoded document held: a program
// that decodes a value and encodes it again sends its fields on. A field no decoder
// fills that an encoder emits is kept at the class possible. It runs once every
// destination has been walked, since a decoder may be met after the encoder.
func (f *encodingFlow) keepDecoded() {
	for i := range f.unwritten {
		switch u := &f.unwritten[i]; {
		case u.filled(f.decoded[f.edge.sites.of(u.field)]):
			f.kept.record(u.field, &u.site, u.detail)
		case u.emits:
			f.filled.record(u.field, &u.site, u.detail)
		}
	}
}

// filledBy reports whether the decoder at one site fills the field for this read: a
// decoder of production code fills it for any reader and one of a test file for a
// reader in a test file, and a reader that may fill the field needs another decoder.
func (u *unwrittenRead) filledBy(decoder *evidence) bool {
	if u.fills && *decoder == u.site {
		return false
	}
	return !decoder.testCode() || u.site.testCode()
}

// filled reports whether one of the decoders at the given sites fills the field for
// this read ([unwrittenRead.filledBy]).
func (u *unwrittenRead) filled(decoders []evidence) bool {
	for i := range decoders {
		if u.filledBy(&decoders[i]) {
			return true
		}
	}
	return false
}

// embedsStruct reports whether one field is an embedded struct or pointer to one,
// whose fields an encoder promotes whether or not the embedded type is exported.
func embedsStruct(field *types.Var) bool {
	return field.Embedded() && isStructValue(field.Type())
}

// isXMLName reports whether a type is encoding/xml's Name.
func isXMLName(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == xmlPackage && named.Obj().Name() == "Name"
}

// importClosure is what one package outside the analysed program can name: every
// interface it declares or writes as a literal, the exported interfaces the packages
// it imports at any depth and the outside packages of the program's import closure
// that import it declare, and whether a template engine is among the packages it
// imports.
type importClosure struct {
	interfaces []*types.Interface
	templates  bool
}

// closure is the import closure of one reading of a package, read once: two
// readings of one path can import two versions of a package.
func (f *encodingFlow) closure(pkg *types.Package) *importClosure {
	held, known := f.closures[pkg]
	if !known {
		held = f.closureOf(pkg)
		f.closures[pkg] = held
	}
	return held
}

// closureOf reads one package's import closure.
func (f *encodingFlow) closureOf(pkg *types.Package) *importClosure {
	imported := f.assert.importedBy(pkg)
	closure := &importClosure{templates: imported.templates}
	closure.interfaces = append(slices.Clone(f.assert.ownInterfaces(pkg)), imported.interfaces...)
	for _, importer := range f.outsidePackages() {
		if slices.ContainsFunc(importer.Imports(), func(p *types.Package) bool { return p.Path() == pkg.Path() }) {
			closure.interfaces = append(closure.interfaces, declaredInterfaces(importer, false)...)
		}
	}
	return closure
}

// dynamicReach is what a function the analysis does not know reads of a value its
// empty-interface parameter is given: what a callee outside the program reads, for
// every package outside it, so a positional codec among them reads every field.
func (f *encodingFlow) dynamicReach() reach {
	outside := joinPackages(nil, f.outsidePackages())
	return reach{
		methods: reachEncoding | reachDecoding,
		fields:  true,
		outside: outside,
		layout:  slices.ContainsFunc(outside, func(pkg *types.Package) bool { return positionalCodecs[pkg.Path()] }),
		emits:   true,
		opaque:  true,
	}
}

// outsidePackages is the packages outside the flow's program ([Program]).
func (f *encodingFlow) outsidePackages() []*types.Package {
	return f.kept.in.outside
}

// declaredInterfaces is the interfaces that hold values ([facts.HoldsValues]) among
// the ones one package declares ([facts.DeclaredInterfaces]), the unexported ones
// only where every is set.
func declaredInterfaces(pkg *types.Package, every bool) []*types.Interface {
	var found []*types.Interface
	for _, named := range facts.DeclaredInterfaces(pkg) {
		iface, _ := named.Underlying().(*types.Interface)
		if (every || named.Obj().Exported()) && facts.HoldsValues(iface) {
			found = append(found, iface)
		}
	}
	return found
}

// outsideMethods is what the packages outside the program a value reaches retain of
// one type's methods: every exported method where an import closure holds a template
// engine, and otherwise the methods by which the type, or a pointer to it,
// implements an interface a closure declares, or an interface a container that
// stores the value declares or writes.
func (f *encodingFlow) outsideMethods(named *types.Named, r reach) (every bool, ids map[string]bool) {
	if len(r.outside) == 0 && len(r.contained) == 0 {
		return false, nil
	}
	ids = make(map[string]bool)
	for _, pkg := range r.outside {
		closure := f.closure(pkg)
		if closure.templates {
			return true, nil
		}
		maps.Copy(ids, f.satisfied(satisfactionKey{named: named, pkg: pkg}, closure.interfaces))
	}
	for _, pkg := range r.contained {
		key := satisfactionKey{named: named, pkg: pkg, contained: true}
		maps.Copy(ids, f.satisfied(key, f.assert.ownInterfaces(pkg)))
	}
	return false, ids
}

// satisfied is the [types.Object.Id] of each method by which one type satisfies one
// package's interfaces, computed once per type and package.
func (f *encodingFlow) satisfied(key satisfactionKey, interfaces []*types.Interface) map[string]bool {
	held, known := f.satisfying[key]
	if !known {
		held = satisfiedMethods(f.readings, key.named, f.declaredIDs(key.named), interfaces)
		f.satisfying[key] = held
	}
	return held
}

// satisfactionKey is one type and one package outside the program it reaches, as
// a callee or as a container that stores it.
type satisfactionKey struct {
	named     *types.Named
	pkg       *types.Package
	contained bool
}

// declaredIDs is [declaredMethodIDs] of one type, computed once per type.
func (f *encodingFlow) declaredIDs(named *types.Named) map[string]bool {
	if held, known := f.methodIDs[named]; known {
		return held
	}
	held := declaredMethodIDs(named)
	f.methodIDs[named] = held
	return held
}

// declaredMethodIDs is the [types.Object.Id] of every method in the method set of a
// pointer to the type, which holds the type's own. A method set can hold two
// unexported methods of one name from two packages, which the name alone conflates.
func declaredMethodIDs(named *types.Named) map[string]bool {
	set := types.NewMethodSet(types.NewPointer(named))
	held := make(map[string]bool, set.Len())
	for selection := range set.Methods() {
		held[selection.Obj().Id()] = true
	}
	return held
}

// satisfiedMethods is the [types.Object.Id] of each method of every interface the
// type, or a pointer to it, implements. An interface with a method the type's set
// lacks is one neither implements, so [facts.Readings.Implements] is asked only of
// the rest.
func satisfiedMethods(readings *facts.Readings, named *types.Named, declared map[string]bool, interfaces []*types.Interface) map[string]bool {
	ids := make(map[string]bool)
	pointer := types.NewPointer(named)
	for _, iface := range interfaces {
		if !declaresEvery(declared, iface) {
			continue
		}
		if !readings.Implements(named, iface) && !readings.Implements(pointer, iface) {
			continue
		}
		for method := range iface.Methods() {
			ids[method.Id()] = true
		}
	}
	return ids
}

// declaresEvery reports whether every method of the interface has its
// [types.Object.Id] among ids.
func declaresEvery(ids map[string]bool, iface *types.Interface) bool {
	for method := range iface.Methods() {
		if !ids[method.Id()] {
			return false
		}
	}
	return true
}

// namedTypesReached returns every defined type a value of t is composed of
// ([typesReached]).
func namedTypesReached(t types.Type) []*types.Named {
	found, _ := typesReached(t)
	return found
}

// typesReached returns every defined type and every struct type with no name a
// value of t is composed of, reaching through a pointer, a slice, an array and a map
// key or value, and stopping at each one it finds. A channel is not one of them:
// nothing reads the value a channel carries out of the value it is a member of, and
// the standard encoders refuse a channel outright.
func typesReached(t types.Type) ([]*types.Named, []*types.Struct) {
	var found []*types.Named
	var structs []*types.Struct
	seen := make(map[types.Type]bool)
	stack := []types.Type{t}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == nil || seen[cur] {
			continue
		}
		seen[cur] = true
		switch u := types.Unalias(cur).(type) {
		case *types.Named:
			found = append(found, u)
		case *types.Pointer:
			stack = append(stack, u.Elem())
		case *types.Slice:
			stack = append(stack, u.Elem())
		case *types.Array:
			stack = append(stack, u.Elem())
		case *types.Map:
			stack = append(stack, u.Key(), u.Elem())
		case *types.Struct:
			structs = append(structs, u)
		}
	}
	return found, structs
}

// encoderReach returns every defined type an encoder walking a value of t reads:
// the types the value is composed of, then the types the members of each of those
// are composed of, until no further type joins. A type reached by several paths
// joins once, and a generic type instantiated twice joins once per instantiation,
// because the members of the two carry different types.
func (f *encodingFlow) encoderReach(t types.Type) []*types.Named {
	found := f.encodedTypes(t)
	seen := make(map[string]bool, len(found))
	for _, named := range found {
		seen[resolve.Spell(named, nil)] = true
	}
	for at := 0; at < len(found); at++ {
		for _, next := range f.membersReached(found[at]) {
			key := resolve.Spell(next, nil)
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, next)
		}
	}
	return found
}

// membersReached returns the defined types the members of one reached type are
// composed of: the types of its fields, an embedded field among them, or the
// elements of what it is built from where it is no struct ([encodingFlow.encodedTypes]).
func (f *encodingFlow) membersReached(named *types.Named) []*types.Named {
	return concreteTypes(f.encodedTypes(named.Underlying()))
}

// encodedTypes returns the defined types a value of t is composed of, and the ones
// the fields of each struct type with no name it is composed of reach, because an
// encoder walks those fields as it walks a defined struct's. A field that is, or
// holds as its element or map value, an interface reaches the types the program
// stores in it ([storedTypes]).
func (f *encodingFlow) encodedTypes(t types.Type) []*types.Named {
	found, structs := typesReached(t)
	seen := make(map[*types.Struct]bool, len(structs))
	join := func(reached types.Type) {
		named, inner := typesReached(reached)
		found = append(found, concreteTypes(named)...)
		structs = append(structs, inner...)
	}
	for len(structs) > 0 {
		st := structs[len(structs)-1]
		structs = structs[:len(structs)-1]
		if seen[st] {
			continue
		}
		seen[st] = true
		for field := range st.Fields() {
			for _, stored := range f.graph.stored.of(field) {
				join(stored)
			}
			if !types.IsInterface(field.Type()) {
				join(field.Type())
			}
		}
	}
	return found
}

// concreteTypes keeps the defined types whose values carry members of their own,
// dropping the ones declared as an interface: what the value behind an interface is
// the analysis does not see, so a member reached through one is where the walk
// stops.
func concreteTypes(reached []*types.Named) []*types.Named {
	kept := reached[:0]
	for _, named := range reached {
		if !types.IsInterface(named) {
			kept = append(kept, named)
		}
	}
	return kept
}

// retention accumulates one class's exemptions. A class records a symbol the run
// reasons about at most once per site;
// every class file of this package builds one of these rather than its own map.
type retention struct {
	in    *Program
	seen  map[exemptionKey]bool
	class vocab.Exemption
	found []Exemption
}

// exemptionKey identifies one symbol held back at one site.
type exemptionKey struct {
	id   inventory.SymbolID
	site evidence
}

// newRetention prepares the accumulation of one class over one program.
func newRetention(in *Program, class vocab.Exemption) *retention {
	return &retention{in: in, class: class, seen: make(map[exemptionKey]bool)}
}

// record keeps one exemption, unless the symbol is not one the run reasons about
// or this class already recorded the symbol at this site.
func (r *retention) record(obj types.Object, site *evidence, detail string) {
	id, held := r.in.Resolve.Object(obj)
	if !held {
		return
	}
	key := exemptionKey{id: id, site: *site}
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.found = append(r.found, Exemption{
		ID:       id,
		Class:    string(r.class),
		Consumer: site.consumer,
		Build:    site.build,
		File:     site.file,
		Site:     site.at,
		Detail:   detail,
	})
}

// evidence is where one piece of evidence is written: a position of the target, or
// of the loaded consumer consumer names, relative to that consumer's root.
type evidence struct {
	consumer string
	file     string // [Exemption.File]
	at       token.Position
	build    load.Build
}

// testCode reports whether the evidence is in test code ([load.Build.TestCode]).
func (e *evidence) testCode() bool {
	_, testFile := inventory.IsTestFile(e.file)
	return e.build.TestCode(testFile)
}

// site renders one position. Every position the load compiled is a file of the
// target or of a loaded consumer, so one the resolver cannot render is a failure of
// the run rather than a site to pass over.
func (r *retention) site(pos token.Pos) (evidence, error) {
	consumer, at, err := r.in.Resolve.Site(pos)
	return evidence{consumer: consumer, file: r.in.Resolve.File(pos), at: at, build: r.in.Resolve.Build(pos)}, err
}

// exemptions returns what the class recorded, ordered by site and then by symbol.
func (r *retention) exemptions() []Exemption {
	slices.SortFunc(r.found, byEvidence)
	return r.found
}

// holdsValue reports whether a field of type t holds the value a decoder decodes
// into: a pointer, an interface or a map.
func holdsValue(t types.Type) bool {
	switch types.Unalias(t).Underlying().(type) {
	case *types.Pointer, *types.Interface, *types.Map:
		return true
	default:
		return false
	}
}

// structFields adds every field of the struct t is, and of each struct one of them
// holds by value.
func structFields(t types.Type, found []*types.Var, seen map[*types.Struct]bool) []*types.Var {
	st, isStruct := types.Unalias(t).Underlying().(*types.Struct)
	if !isStruct || seen[st] {
		return found
	}
	seen[st] = true
	for field := range st.Fields() {
		found = structFields(field.Type(), append(found, field), seen)
	}
	return found
}

// literalFields is the field each element of a struct literal sets. An element of a
// slice, array or map of pointers may elide &T, and the literal then has the
// pointer type.
func literalFields(info *types.Info, lit *ast.CompositeLit) []*types.Var {
	at := types.Unalias(info.TypeOf(lit))
	if pointer, isPointer := at.(*types.Pointer); isPointer {
		at = pointer.Elem()
	}
	st, isStruct := underlyingStruct(at)
	if !isStruct {
		return nil
	}
	found := make([]*types.Var, 0, len(lit.Elts))
	for i, elt := range lit.Elts {
		found = append(found, literalField(info, st, elt, i))
	}
	return found
}

// underlyingStruct is the struct type t is defined as, and false where t is none.
func underlyingStruct(t types.Type) (*types.Struct, bool) {
	if t == nil {
		return nil, false
	}
	st, isStruct := t.Underlying().(*types.Struct)
	return st, isStruct
}

// literalField is the field one element of a struct literal sets.
func literalField(info *types.Info, st *types.Struct, elt ast.Expr, at int) *types.Var {
	kv, keyed := elt.(*ast.KeyValueExpr)
	if !keyed {
		if at < st.NumFields() {
			return st.Field(at)
		}
		return nil
	}
	ident, named := kv.Key.(*ast.Ident)
	if !named {
		return nil
	}
	field, isVar := info.Uses[ident].(*types.Var)
	if !isVar || !field.IsField() {
		return nil
	}
	return field
}

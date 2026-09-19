package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
	"golang.org/x/tools/go/packages"
)

// destinationPackages are the packages whose every function and method may name
// the members of a value it is given at run time, so a value reaching any of them
// keeps the members no reference points at. The value is whether the destination
// reaches METHODS as well as fields: the three standard encoders read fields and
// call no method of the value, while a template engine selects a method by the same
// syntax it selects a field with.
var destinationPackages = map[string]bool{
	"encoding/gob":  false,
	"encoding/json": false,
	"encoding/xml":  false,
	"html/template": true,
	"reflect":       true,
	"text/template": true,
}

// The reflection package, and the one function of it that reads a value's fields
// and reaches no method: DeepEqual compares field by field and calls nothing the
// type declares. Every other entry point hands out a Value or a Type, from which a
// method is reachable by name, and what a program does with one is not in the type
// information, so the conservative answer there is that methods are reached.
const (
	reflectPackage = "reflect"
	deepEqual      = "DeepEqual"
)

// sortPackage is the package whose one interface a conversion reaches the class
// through.
const sortPackage = "sort"

// destinationInterfaces are the interfaces a conversion into which reaches the
// class, each named by its package path and its name.
var destinationInterfaces = [...]struct{ pkg, name string }{
	{sortPackage, "Interface"},
}

// namedDestinations are the packages whose destinations a rule of the vocabulary
// names one by one: the encoders, the template engines and the reflection package
// this class reads above, the database package whose scan target it reads, the
// sorting package whose interface the conversion set reads, and the formatting,
// logging, testing and structured-logging packages the format-verb class reads.
//
// A call into one of them is recorded by the rule that names it, and the rule for a
// callee the analysis cannot read passes over it. Both rules would otherwise fire on
// the same call and spell the destination differently, and two spellings of one
// detail are two records of one fact.
var namedDestinations = namedDestinationPackages()

// namedDestinationPackages joins the two destination tables, so the set of named
// packages has the same owner as the rules it is derived from.
func namedDestinationPackages() map[string]bool {
	named := map[string]bool{
		sqlPackage:     true,
		sortPackage:    true,
		formatPackage:  true,
		logPackage:     true,
		testingPackage: true,
		slogPackage:    true,
	}
	for path := range destinationPackages {
		named[path] = true
	}
	return named
}

// The one method of the one package whose argument flows only when it is a
// pointer, and the two receivers that declare it.
const (
	sqlPackage    = "database/sql"
	sqlScanMethod = "Scan"
	sqlRows       = "Rows"
	sqlRow        = "Row"
)

// EncodingReflectionDetector records the encoding-reflection class: a type whose
// values reach reflection, a standard encoder, a template engine, a database scan
// target, a sort interface or a structured-logging call keeps its exported fields
// and every field of it that carries a struct tag, because that consumer names them
// by string and the reference graph holds no edge to them.
//
// What is retained is per destination, because the destinations do not read the same
// thing. The three standard encoders, a database scan and the comparison of two
// values by reflection read fields and call no method of the value, so those retain
// fields alone. A template engine selects a method by the syntax it selects a field
// with, a structured-logging handler renders a value through the method it answers
// with, a sort interface is three methods, and every other entry point of the
// reflection package hands out a value from which a method is reachable by name, so
// those retain the exported methods as well.
//
// A struct value that leaves the analysed program is the class's widest
// destination, and the one no list of packages can complete. A struct, or a pointer
// to one, handed to a parameter typed as the empty interface of a function or method
// the load did not read has left the analysis: the callee's body is not in the
// program, the parameter's type keeps nothing of the value, and whatever the callee
// does with it, encode it, render it, log it or reflect over it, reads its fields and
// may call its exported methods. Such a value therefore flows with the full retained
// set. A function of the program that hands one of its own such parameters to that
// call, or to any other destination of the class, is a destination for its callers'
// arguments in turn, to a fixpoint, so a wrapper of a wrapper carries the rule of the
// call it forwards to.
//
// The empty interface is the one parameter type this rule reads, because it is the
// one an interface conversion answers nothing for: a value handed to any other
// interface is a conversion the conversion set records, and the methods that
// interface requires are what interface-satisfaction retains for it, which is
// everything the callee can reach through the parameter's own type.
//
// Three rules the class applies where the case is not spelled out. Every argument
// of a function or method of a destination package flows, the writer of a
// template execution included, because what is named is the argument position and
// not the parameter's meaning. A value reaches through a pointer, a slice, an
// array and a map, and from every type so reached through the fields of that type
// again, until no further type joins, because an encoder walks the whole value and
// not only its outermost type; the walk stops at an interface-typed field, whose
// dynamic type the analysis does not see. A method is retained when the defined
// type declares it, so a method promoted from an embedded type is retained where
// the embedded type is reached, which the field walk does.
func EncodingReflectionDetector(in *Input) ([]graph.Exemption, error) {
	f := &encodingFlow{
		kept:     newRetention(in, EncodingReflection),
		targets:  conversionTargets(in.Result.Packages),
		inside:   programPackages(in.Result),
		wrappers: make(destinationParameters),
	}
	f.forwardingDestinations(in)
	if err := f.walkCalls(); err != nil {
		return nil, err
	}
	if err := f.walkConversions(); err != nil {
		return nil, err
	}
	return f.kept.exemptions(), nil
}

// encodingFlow accumulates the class over one loaded configuration.
type encodingFlow struct {
	kept     *retention
	inside   map[string]bool
	wrappers destinationParameters
	targets  []interfaceTarget
}

// interfaceTarget is one interface the class treats as a destination, resolved
// from the loaded program so a conversion is matched on the declared type rather
// than on a method-set shape a local interface could imitate.
type interfaceTarget struct {
	iface *types.Interface
	name  string
}

// destination is what one call is to the class: the clause an exemption records,
// whether only a pointer argument flows into it, and whether it reaches the methods
// of the value it is given as well as its fields.
type destination struct {
	detail   string
	pointers bool
	methods  bool
}

// walkCalls records every value a call hands to a destination package.
func (f *encodingFlow) walkCalls() error {
	for _, p := range sortedPackages(f.kept.in.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			if err := f.walkFile(p.TypesInfo, file); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkFile records the destination calls of one source file. A file several
// package variants type-check is walked once per variant, and every variant
// resolves the same positions, so the repeated records collapse on the site.
func (f *encodingFlow) walkFile(info *types.Info, file *ast.File) error {
	var failed error
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || failed != nil {
			return failed == nil
		}
		fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
		if !isFunc {
			return true
		}
		if d, reaches := encodingDestination(fn); reaches {
			failed = f.arguments(info, call.Args, d)
			return failed == nil
		}
		failed = f.opaqueArguments(info, call, fn)
		return failed == nil
	})
	return failed
}

// opaqueArguments records the struct values one call hands to a destination the
// analysis cannot read: a parameter typed as the empty interface, of a function or
// method outside the analysed program or of a function of the program that passes
// such a parameter on.
//
// A value of a struct type or a pointer to one is what flows; a slice, a map or a
// channel of structs is not, because what crosses the boundary is the value the
// argument's own type describes. The retained set is the full one, methods included,
// since the callee's body decides what it reads and the analysis does not hold it.
func (f *encodingFlow) opaqueArguments(info *types.Info, call *ast.CallExpr, fn *types.Func) error {
	sinks, reaches := f.opaqueSinks(fn)
	if !reaches {
		return nil
	}
	d := destination{detail: "passed to " + fn.FullName(), methods: true}
	sig := fn.Signature()
	for i, arg := range call.Args {
		at, supplies := parameterAt(sig, i)
		if !supplies || !slices.Contains(sinks, at) {
			continue
		}
		flows := info.TypeOf(arg)
		if flows == nil || !carriesStruct(flows) {
			continue
		}
		if err := f.retain(flows, arg.Pos(), d); err != nil {
			return err
		}
	}
	return nil
}

// opaqueSinks are the parameter positions of one function at which a value crosses
// out of the analysis, and false where none does.
//
// A function or method of a package the load did not read carries them at every
// parameter typed as the empty interface, because its body is not in the program. A
// function of the program carries the ones the wrapper set holds for it, which is
// where it hands a parameter of its own on. A package whose destinations the
// vocabulary names one by one carries none: the rule that names it records the call.
func (f *encodingFlow) opaqueSinks(fn *types.Func) ([]int, bool) {
	if fn.Pkg() == nil {
		return nil, false
	}
	if f.inside[fn.Pkg().Path()] {
		held, forwards := f.wrappers[fn.Pos()]
		return held, forwards
	}
	if namedDestinations[fn.Pkg().Path()] {
		return nil, false
	}
	sinks := erasedParameters(fn.Signature())
	return sinks, len(sinks) > 0
}

// destinationParameters are the parameters of the analysed program's own functions
// that are destinations of the class, each declaration keyed by the position it is
// written at, which is the one position every package variant that type-checks the
// declaration shares.
type destinationParameters map[token.Pos][]int

// forwardingDestinations fills the wrapper set to a fixpoint: a function of the
// program that hands one of its own interface-typed parameters to a destination of
// the class is a destination at that parameter, and is then itself something a
// further function can forward to, so the set grows until no function joins it.
func (f *encodingFlow) forwardingDestinations(in *Input) {
	candidates := interfaceWrappers(in)
	for joined := true; joined; {
		joined = false
		for i := range candidates {
			for _, forwarded := range candidates[i].forwards {
				if !f.sink(forwarded.to, forwarded.at) {
					continue
				}
				joined = f.hold(candidates[i].pos, forwarded.own) || joined
			}
		}
	}
	for pos := range f.wrappers {
		slices.Sort(f.wrappers[pos])
	}
}

// sink reports whether one parameter of one function is a destination of the class.
// Every parameter of a named destination is one, per the rule that what a
// destination call names is the argument position.
func (f *encodingFlow) sink(fn *types.Func, at int) bool {
	if _, named := encodingDestination(fn); named {
		return true
	}
	sinks, reaches := f.opaqueSinks(fn)
	return reaches && slices.Contains(sinks, at)
}

// hold records that one parameter of one declaration of the program is a
// destination, and reports whether that was not already known.
func (f *encodingFlow) hold(pos token.Pos, at int) bool {
	if slices.Contains(f.wrappers[pos], at) {
		return false
	}
	f.wrappers[pos] = append(f.wrappers[pos], at)
	return true
}

// interfaceWrapper is one function of the analysed program that may be a
// destination of the class: where it is declared, and every call in its body that
// hands one of its own parameters typed as the empty interface to another function.
type interfaceWrapper struct {
	forwards []forwardedParameter
	pos      token.Pos
}

// forwardedParameter is one parameter a wrapper hands on: the function it goes to,
// the wrapper's own parameter position, and the position it arrives at.
type forwardedParameter struct {
	to  *types.Func
	own int
	at  int
}

// interfaceWrappers returns every function of the loaded configuration that hands a
// parameter of its own typed as the empty interface to another function, in one order
// so that two runs over one load read the same set. A function literal is not one:
// nothing names it, so no call to it resolves to a function this class can recognise.
func interfaceWrappers(in *Input) []interfaceWrapper {
	var found []interfaceWrapper
	for _, p := range sortedPackages(in.Result.Packages) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range sortedFiles(p, in.Result.Fset) {
			found = append(found, fileInterfaceWrappers(p.TypesInfo, file)...)
		}
	}
	return found
}

// fileInterfaceWrappers returns the candidates one source file declares, in the
// order the file writes them.
func fileInterfaceWrappers(info *types.Info, file *ast.File) []interfaceWrapper {
	var found []interfaceWrapper
	for _, decl := range file.Decls {
		fd, declares := decl.(*ast.FuncDecl)
		if !declares || fd.Body == nil {
			continue
		}
		if w, forwards := forwardedInterfaces(info, fd); forwards {
			found = append(found, w)
		}
	}
	return found
}

// forwardedInterfaces reports whether one declaration hands a parameter of its own
// typed as the empty interface to another function, and returns the calls that do. A
// parameter passed as itself and a variadic list spread whole are both hands-on: what
// the callee receives is the value the declaration's own caller wrote.
func forwardedInterfaces(info *types.Info, fd *ast.FuncDecl) (interfaceWrapper, bool) {
	fn, declares := info.Defs[fd.Name].(*types.Func)
	if !declares {
		return interfaceWrapper{}, false
	}
	sig := fn.Signature()
	own := make(map[*types.Var]int)
	for _, at := range erasedParameters(sig) {
		own[sig.Params().At(at)] = at
	}
	if len(own) == 0 {
		return interfaceWrapper{}, false
	}

	w := interfaceWrapper{pos: fn.Pos()}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
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
	return isAnyType(t)
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

// programPackages are the import paths of the analysed program: the target's own
// packages and the packages of every consumer the run loaded. A function declared
// outside them is one whose body the analysis does not hold.
func programPackages(r *load.Result) map[string]bool {
	inside := make(map[string]bool, len(r.Packages))
	for _, p := range r.Packages {
		inside[p.PkgPath] = true
	}
	for _, one := range r.Consumers {
		for _, p := range one.Packages {
			inside[p.PkgPath] = true
		}
	}
	return inside
}

// arguments records the types the arguments of one destination call carry.
func (f *encodingFlow) arguments(info *types.Info, args []ast.Expr, d destination) error {
	for _, arg := range args {
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
		if err := f.retain(at, arg.Pos(), d); err != nil {
			return err
		}
	}
	return nil
}

// walkConversions records every value converted to a destination interface. The
// conversion set is read rather than walked a second time, so a value reaching
// sort.Interface as an argument, as an assignment or as a satisfaction assertion
// is recorded by one rule.
//
// A destination interface reaches methods, because the interface is a set of methods
// and the package behind it calls them: what sorting reads of a value is the three
// methods and nothing else.
func (f *encodingFlow) walkConversions() error {
	if len(f.targets) == 0 {
		return nil
	}
	conversions, err := Conversions(f.kept.in)
	if err != nil {
		return fmt.Errorf("encoding-reflection: %w", err)
	}
	for _, c := range conversions {
		name, reaches := matchInterface(f.targets, c.To)
		if !reaches {
			continue
		}
		if err := f.retain(c.From, c.Site, destination{
			detail: "converted to " + name, methods: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// retain records what every defined type a destination walking a value of t reads
// keeps: its exported fields, every field of it that carries a struct tag whether
// that field is exported or not, because a tagged field is named by its tag and not
// by its visibility, and the exported methods the type declares where the
// destination reaches a method at all.
func (f *encodingFlow) retain(t types.Type, at token.Pos, d destination) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	for _, named := range encoderReach(t) {
		f.members(named, site, d)
	}
	return nil
}

// members records what one destination reads of one defined type: the exported
// methods the type declares where the destination reaches a method at all, and then
// the fields of it a destination reading fields reads.
func (f *encodingFlow) members(named *types.Named, site token.Position, d destination) {
	origin := named.Origin()
	if d.methods {
		for m := range origin.Methods() {
			if m.Exported() {
				f.kept.record(m, site, d.detail)
			}
		}
	}
	st, isStruct := origin.Underlying().(*types.Struct)
	if !isStruct {
		return
	}
	for i := range st.NumFields() {
		if st.Field(i).Exported() || st.Tag(i) != "" {
			f.kept.record(st.Field(i), site, d.detail)
		}
	}
}

// encodingDestination reports whether a call to fn reaches the class, and how.
func encodingDestination(fn *types.Func) (destination, bool) {
	pkg := fn.Pkg()
	if pkg == nil {
		return destination{}, false
	}
	if methods, found := destinationPackages[pkg.Path()]; found {
		if pkg.Path() == reflectPackage && fn.Name() == deepEqual {
			methods = false
		}
		return destination{detail: "passed to " + fn.FullName(), methods: methods}, true
	}
	if pkg.Path() == sqlPackage && fn.Name() == sqlScanMethod && scansARow(fn) {
		return destination{detail: "scanned by " + fn.FullName(), pointers: true}, true
	}
	// A structured-logging call hands its operands to a handler, which may render
	// one by its members, so the set of those calls is the one the formatting
	// class reads and this class shares it. A handler reaches methods: it renders a
	// value that answers LogValue or String through that method, and one that
	// answers neither by marshalling its fields.
	if _, logs := slogFunctions[fn.Name()]; pkg.Path() == slogPackage && logs {
		return destination{detail: "passed to " + fn.FullName(), methods: true}, true
	}
	return destination{}, false
}

// scansARow reports whether fn is the Scan method of a row or of a row set rather
// than another Scan the same package declares.
func scansARow(fn *types.Func) bool {
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	t := types.Unalias(recv.Type())
	if p, isPointer := t.(*types.Pointer); isPointer {
		t = types.Unalias(p.Elem())
	}
	named, isNamed := t.(*types.Named)
	if !isNamed {
		return false
	}
	name := named.Obj().Name()
	return name == sqlRows || name == sqlRow
}

// conversionTargets resolves the destination interfaces from the loaded program.
// An interface no loaded package declares cannot be the target of a conversion
// the same program makes, so a missing one narrows nothing.
func conversionTargets(pkgs []*packages.Package) []interfaceTarget {
	var found []interfaceTarget
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil {
			return
		}
		for _, want := range destinationInterfaces {
			if p.PkgPath != want.pkg {
				continue
			}
			obj := p.Types.Scope().Lookup(want.name)
			if obj == nil {
				continue
			}
			if iface, isInterface := obj.Type().Underlying().(*types.Interface); isInterface {
				found = append(found, interfaceTarget{iface: iface, name: want.pkg + "." + want.name})
			}
		}
	})
	return found
}

// matchInterface returns the name of the destination interface identical to
// iface, and reports whether there is one.
func matchInterface(targets []interfaceTarget, iface *types.Interface) (string, bool) {
	for _, t := range targets {
		if types.Identical(t.iface, iface) {
			return t.name, true
		}
	}
	return "", false
}

// namedTypesReached returns every defined type a value of t is composed of,
// reaching through a pointer, a slice, an array and a map key or value, and
// stopping at each defined type it finds. A channel is not one of them: nothing
// reads the value a channel carries out of the value it is a member of, and the
// standard encoders refuse a channel outright.
func namedTypesReached(t types.Type) []*types.Named {
	var found []*types.Named
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
		}
	}
	return found
}

// encoderReach returns every defined type an encoder walking a value of t reads:
// the types the value is composed of, then the types the members of each of those
// are composed of, until no further type joins. A type reached by several paths
// joins once, and a generic type instantiated twice joins once per instantiation,
// because the members of the two carry different types.
func encoderReach(t types.Type) []*types.Named {
	found := namedTypesReached(t)
	seen := make(map[string]bool, len(found))
	for _, named := range found {
		seen[types.TypeString(named, nil)] = true
	}
	for at := 0; at < len(found); at++ {
		for _, next := range membersReached(found[at]) {
			key := types.TypeString(next, nil)
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
// elements of what it is built from where it is no struct. A member typed as an
// interface is where the walk stops, because the dynamic type an encoder would
// read there is not in the type information.
func membersReached(named *types.Named) []*types.Named {
	st, isStruct := named.Underlying().(*types.Struct)
	if !isStruct {
		return concreteTypes(namedTypesReached(named.Underlying()))
	}
	var found []*types.Named
	for field := range st.Fields() {
		if types.IsInterface(field.Type()) {
			continue
		}
		found = append(found, concreteTypes(namedTypesReached(field.Type()))...)
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
// reasons about, at a site a file of the target holds, at most once per site;
// every class file of this package builds one of these rather than its own map.
type retention struct {
	in    *Input
	seen  map[exemptionKey]bool
	class Class
	found []graph.Exemption
}

// exemptionKey identifies one symbol held back at one site.
type exemptionKey struct {
	id   graph.SymbolID
	site string
}

// newRetention prepares the accumulation of one class over one input.
func newRetention(in *Input, class Class) *retention {
	return &retention{in: in, class: class, seen: make(map[exemptionKey]bool)}
}

// record keeps one exemption, unless the symbol is not one the run reasons about
// or this class already recorded the symbol at this site.
func (r *retention) record(obj types.Object, site token.Position, detail string) {
	id, held := r.in.Resolve.Object(obj)
	if !held {
		return
	}
	key := exemptionKey{id: id, site: site.String()}
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.found = append(r.found, graph.Exemption{
		ID:     id,
		Class:  string(r.class),
		Site:   site,
		Detail: detail,
	})
}

// site renders one position. Every position the load compiled is a position of
// the target, so one the resolver cannot render is a failure of the run rather
// than a site to pass over.
func (r *retention) site(pos token.Pos) (token.Position, error) {
	return r.in.Resolve.Render(pos)
}

// exemptions returns what the class recorded, ordered by site and then by symbol.
func (r *retention) exemptions() []graph.Exemption {
	slices.SortFunc(r.found, byEvidence)
	return r.found
}

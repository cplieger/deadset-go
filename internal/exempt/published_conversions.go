package exempt

import (
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"golang.org/x/tools/go/packages"
)

// publishedConversions is the sites a program outside the analysis can write into
// a library's published API, which grammar/analysis.md "Conversions an outside
// program writes" states: at each published parameter of an interface type, and at
// each type parameter of a published declaration whose constraint is an interface,
// one site per type of the target an outside program can hold that implements it.
// Each is positioned at the parameter or the type parameter, so the declaration
// declaring it holds the site. There are none unless the target is a library whose
// run loaded no consumer: a loaded consumer's sites are the run's own.
func publishedConversions(in *Input) []graph.Conversion {
	if !in.Options.PublishedAPI || in.Result == nil || len(in.Result.Consumers) > 0 {
		return nil
	}
	if in.published == nil {
		scan := newPublishedScan(in.Result.Packages, in.Resolve)
		sites := scan.sites()
		in.published = &sites
	}
	return *in.published
}

// publishedScan reads one configuration's target packages for what an outside
// program can hold and where it can hand a value over.
type publishedScan struct {
	resolve    *graph.Resolver
	target     map[string]bool       // the target's packages, by [graph.Resolver.PackageKey]
	holdable   map[string]types.Type // by [graph.Resolver.TypeKey]
	reached    map[string]bool       // the types the published API reaches, by [graph.Resolver.TypeKey]
	functions  map[*types.Func]bool  // the published functions read
	typeParams map[*types.TypeParam]bool
	queue      []types.Type         // holdable types not yet read
	parameters []publishedParameter // every published parameter and type parameter
	published  []*packages.Package  // the packages of the published API
	order      []string             // holdable type keys in the order they joined
}

// publishedParameter is one position an outside program hands a value to: a
// parameter whose type is t, or a type parameter whose constraint is t. outside
// marks one a function outside the target declares, which has no position the
// target holds.
type publishedParameter struct {
	t       types.Type
	pos     token.Pos
	outside bool
}

// newPublishedScan indexes the target's packages, each once.
func newPublishedScan(pkgs []*packages.Package, resolve *graph.Resolver) *publishedScan {
	s := &publishedScan{
		resolve:    resolve,
		target:     make(map[string]bool),
		holdable:   make(map[string]types.Type),
		reached:    make(map[string]bool),
		functions:  make(map[*types.Func]bool),
		typeParams: make(map[*types.TypeParam]bool),
	}
	seen := make(map[string]bool)
	for _, p := range graph.SortedPackages(pkgs) {
		if p.Types == nil || p.Module == nil || !p.Module.Main || p.ForTest != "" || seen[resolve.PackageKey(p.Types)] {
			continue
		}
		seen[resolve.PackageKey(p.Types)] = true
		s.target[resolve.PackageKey(p.Types)] = true
		if publishedPackage(p) {
			s.published = append(s.published, p)
		}
	}
	return s
}

// publishedPackage reports whether a program outside the module can import one
// package of the target: neither a command, an external test package nor a package
// below an internal directory.
func publishedPackage(p *packages.Package) bool {
	return p.Name != "main" && !strings.HasSuffix(p.PkgPath, "_test") &&
		!slices.Contains(strings.Split(p.PkgPath, "/"), "internal")
}

// sites reads the published API and records one site per published parameter and
// holdable type that implements its interface.
func (s *publishedScan) sites() []graph.Conversion {
	for _, p := range s.published {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			s.seed(scope.Lookup(name))
		}
	}
	s.close()
	candidates := s.candidates()
	var sites []graph.Conversion
	for _, param := range s.parameters {
		iface := methodInterface(param.t)
		if iface == nil {
			continue
		}
		for _, t := range candidates {
			from, implements := implementing(t, iface)
			if !implements {
				continue
			}
			// A parameter of a function outside the target is positioned at the
			// type, so the type's own declaration holds the site, which retains
			// while the type is live and never less than the function would.
			at := param.pos
			if param.outside {
				at = t.Obj().Pos()
			}
			sites = append(sites, graph.Conversion{
				From: from, Interface: param.t, To: iface, Name: spelled(param.t), Site: at,
			})
		}
	}
	return sites
}

// seed reads one exported object of a package of the published API.
func (s *publishedScan) seed(obj types.Object) {
	if !obj.Exported() {
		return
	}
	s.reach(obj.Type())
	switch o := obj.(type) {
	case *types.TypeName:
		s.hold(o.Type())
		if named, isNamed := types.Unalias(o.Type()).(*types.Named); isNamed {
			s.typeParameters(named.TypeParams(), false)
		}
	case *types.Func:
		s.function(o, o.Signature())
	case *types.Var, *types.Const:
		s.hold(o.Type())
	}
}

// hold adds one type to the holdable set.
func (s *publishedScan) hold(t types.Type) {
	if t == nil {
		return
	}
	t = types.Unalias(t)
	key := s.resolve.TypeKey(t)
	if _, held := s.holdable[key]; held {
		return
	}
	s.holdable[key] = t
	s.order = append(s.order, key)
	s.queue = append(s.queue, t)
}

// close reads every holdable type until no type joins: what a value of it hands
// out, and the published functions selectable on it.
func (s *publishedScan) close() {
	for len(s.queue) > 0 {
		t := s.queue[0]
		s.queue = s.queue[1:]
		s.members(t)
	}
}

// members reads what a value of one holdable type hands out: the element, key and
// result types of its type, its exported fields, a promoted one included, and the
// published functions selectable on it.
func (s *publishedScan) members(t types.Type) {
	switch u := t.(type) {
	case *types.Pointer:
		s.hold(u.Elem())
		return
	case *types.Named:
		s.selectable(u)
		if _, isInterface := u.Underlying().(*types.Interface); !isInterface {
			s.composite(u.Underlying())
		}
		return
	}
	s.composite(t)
}

// composite reads the types a type with no name of its own hands out.
func (s *publishedScan) composite(t types.Type) {
	switch u := t.(type) {
	case *types.Pointer:
		s.hold(u.Elem())
	case *types.Slice:
		s.hold(u.Elem())
	case *types.Array:
		s.hold(u.Elem())
	case *types.Chan:
		s.hold(u.Elem())
	case *types.Map:
		s.hold(u.Key())
		s.hold(u.Elem())
	case *types.Signature:
		for v := range u.Results().Variables() {
			s.hold(v.Type())
		}
	case *types.Struct:
		s.fields(u, make(map[*types.Struct]bool))
	case *types.Interface:
		s.interfaceMethods(u)
	}
}

// fields holds the type of every exported field of a struct, and of every field
// an embedded field of any visibility promotes.
func (s *publishedScan) fields(st *types.Struct, seen map[*types.Struct]bool) {
	if seen[st] {
		return
	}
	seen[st] = true
	for field := range st.Fields() {
		if field.Exported() {
			s.hold(field.Type())
		}
		if !field.Embedded() {
			continue
		}
		embedded := field.Type()
		if pointer, isPointer := embedded.(*types.Pointer); isPointer {
			embedded = pointer.Elem()
		}
		if inner, isStruct := embedded.Underlying().(*types.Struct); isStruct {
			s.fields(inner, seen)
		}
	}
}

// selectable reads every exported method selectable on a value of one defined
// type or a pointer to it, a promoted one included.
func (s *publishedScan) selectable(named *types.Named) {
	var set *types.MethodSet
	if types.IsInterface(named) {
		set = types.NewMethodSet(named)
	} else {
		set = types.NewMethodSet(types.NewPointer(named))
	}
	for selection := range set.Methods() {
		if fn, isFunc := selection.Obj().(*types.Func); isFunc && fn.Exported() {
			s.function(fn, fn.Signature())
		}
	}
}

// interfaceMethods reads the methods of an interface an outside program holds: an
// exported one is a published function, and the published code calls any of them
// with values of its own.
func (s *publishedScan) interfaceMethods(iface *types.Interface) {
	for m := range iface.Methods() {
		if m.Exported() {
			s.function(m, m.Signature())
			continue
		}
		for v := range m.Signature().Params().Variables() {
			s.hold(v.Type())
		}
	}
}

// function reads one published function: its results are held, the parameters of
// a function-typed parameter are held, and each parameter and type parameter is a
// position an outside program hands a value to.
func (s *publishedScan) function(fn *types.Func, sig *types.Signature) {
	if s.functions[fn] {
		return
	}
	s.functions[fn] = true
	outside := fn.Pkg() == nil || !s.target[s.resolve.PackageKey(fn.Pkg())]
	for v := range sig.Results().Variables() {
		s.hold(v.Type())
	}
	params := sig.Params()
	for i := range params.Len() {
		v := params.At(i)
		t := v.Type()
		if callback, isFunc := t.Underlying().(*types.Signature); isFunc {
			for p := range callback.Params().Variables() {
				s.hold(p.Type())
			}
		}
		if sig.Variadic() && i == params.Len()-1 {
			if slice, isSlice := t.(*types.Slice); isSlice {
				t = slice.Elem()
			}
		}
		s.parameters = append(s.parameters, publishedParameter{t: t, pos: v.Pos(), outside: outside})
	}
	s.typeParameters(sig.TypeParams(), outside)
}

// typeParameters records each type parameter of a published declaration.
func (s *publishedScan) typeParameters(list *types.TypeParamList, outside bool) {
	for tp := range list.TypeParams() {
		if !s.typeParams[tp] {
			s.typeParams[tp] = true
			s.parameters = append(s.parameters, publishedParameter{t: tp.Constraint(), pos: tp.Obj().Pos(), outside: outside})
		}
	}
}

// reach reads the types the published API reaches, as the confidence rule states,
// for the interfaces among them: each such interface's methods are published
// functions or take values the published code supplies.
func (s *publishedScan) reach(t types.Type) {
	if t == nil {
		return
	}
	t = types.Unalias(t)
	key := s.resolve.TypeKey(t)
	if s.reached[key] {
		return
	}
	s.reached[key] = true
	switch u := t.(type) {
	case *types.Named:
		s.reachNamed(u)
	case interface{ Elem() types.Type }:
		if m, isMap := u.(*types.Map); isMap {
			s.reach(m.Key())
		}
		s.reach(u.Elem())
	case *types.Signature:
		s.reachSignature(u)
	case *types.Struct:
		for field := range u.Fields() {
			if field.Exported() || field.Embedded() {
				s.reach(field.Type())
			}
		}
	case *types.Interface:
		s.interfaceMethods(u)
		for embedded := range u.EmbeddedTypes() {
			s.reach(embedded)
		}
	}
}

// reachNamed reaches what a defined type the published API reaches holds: its
// type arguments, its exported methods' signatures and its underlying type.
func (s *publishedScan) reachNamed(u *types.Named) {
	if iface, isInterface := u.Underlying().(*types.Interface); isInterface {
		s.interfaceMethods(iface)
	}
	for arg := range u.TypeArgs().Types() {
		s.reach(arg)
	}
	set := types.NewMethodSet(types.NewPointer(u))
	if types.IsInterface(u) {
		set = types.NewMethodSet(u)
	}
	for selection := range set.Methods() {
		if selection.Obj().Exported() {
			s.reach(selection.Obj().Type())
		}
	}
	s.reach(u.Underlying())
}

// reachSignature reaches the parameters, results and constraints of one function
// type.
func (s *publishedScan) reachSignature(u *types.Signature) {
	for v := range u.Params().Variables() {
		s.reach(v.Type())
	}
	for v := range u.Results().Variables() {
		s.reach(v.Type())
	}
	for tp := range u.TypeParams().TypeParams() {
		s.reach(tp.Constraint())
	}
}

// candidates is every defined type of the target, not an interface, a value of
// which or of a pointer to which an outside program can hold, each once, in the
// order it joined the holdable set.
func (s *publishedScan) candidates() []*types.Named {
	seen := make(map[*types.Named]bool)
	var found []*types.Named
	for _, key := range s.order {
		t := s.holdable[key]
		if pointer, isPointer := t.(*types.Pointer); isPointer {
			t = types.Unalias(pointer.Elem())
		}
		named, isNamed := t.(*types.Named)
		if !isNamed || named.Obj().Pkg() == nil || !s.target[s.resolve.PackageKey(named.Obj().Pkg())] || types.IsInterface(named) {
			continue
		}
		named = named.Origin()
		if !seen[named] {
			seen[named] = true
			found = append(found, named)
		}
	}
	return found
}

// methodInterface is the interface a published parameter's type is, where it
// requires methods and holds no type term and does not embed comparable, and nil
// otherwise.
func methodInterface(t types.Type) *types.Interface {
	if _, isParam := t.(*types.TypeParam); isParam || t == nil {
		return nil
	}
	iface, isInterface := t.Underlying().(*types.Interface)
	if !isInterface || iface.NumMethods() == 0 || !iface.IsMethodSet() || iface.IsComparable() {
		return nil
	}
	return iface
}

// implementing is the type a site converts for one candidate: the type itself or a
// pointer to it, whichever implements the interface, the type first. A generic type
// implements it where every method of the interface has a method of the type of the
// same name with as many parameters and results, and the site converts a pointer to
// the generic type.
func implementing(named *types.Named, iface *types.Interface) (types.Type, bool) {
	if named.TypeParams().Len() > 0 {
		pointer := types.NewPointer(named)
		return pointer, matchesByArity(pointer, iface)
	}
	if types.Implements(named, iface) {
		return named, true
	}
	pointer := types.NewPointer(named)
	return pointer, types.Implements(pointer, iface)
}

// matchesByArity reports whether a value of t has, for every method of the
// interface, a method of the same name with as many parameters and as many results.
func matchesByArity(t types.Type, iface *types.Interface) bool {
	set := types.NewMethodSet(t)
	for m := range iface.Methods() {
		selection := set.Lookup(m.Pkg(), m.Name())
		if selection == nil {
			return false
		}
		fn, isFunc := selection.Obj().(*types.Func)
		if !isFunc {
			return false
		}
		have, want := fn.Signature(), m.Signature()
		if have.Params().Len() != want.Params().Len() || have.Results().Len() != want.Results().Len() {
			return false
		}
	}
	return true
}

// genericOrigin reports whether t is a generic type no instantiation names, or a
// pointer to one, which a published site converts under the name-and-arity bound.
func genericOrigin(t types.Type) bool {
	if pointer, isPointer := t.(*types.Pointer); isPointer {
		t = pointer.Elem()
	}
	named, isNamed := types.Unalias(t).(*types.Named)
	return isNamed && named.TypeParams().Len() > 0 && named.TypeArgs().Len() == 0
}

// spelled names one interface type the way a message carries it.
func spelled(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

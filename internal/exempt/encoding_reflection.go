package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// destinationPackages are the packages whose every function and method may name
// the members of a value it is given at run time, so a value reaching any of them
// keeps the members no reference points at. The value is what the destination reads
// of the value's METHODS beside its fields: a standard encoder resolves the methods
// of [methodsResolvedByName] and calls no other, while a template engine selects a
// method by the same syntax it selects a field with.
var destinationPackages = map[string]reach{
	"encoding/gob":     reachGobEncode | reachGobDecode,
	"encoding/json":    reachJSONEncode | reachJSONDecode,
	"encoding/json/v2": reachJSONEncode | reachJSONDecode,
	"encoding/xml":     reachXMLEncode | reachXMLDecode,
	"html/template":    reachExportedMethods,
	"reflect":          reachExportedMethods,
	"text/template":    reachExportedMethods,
}

// reach is what a destination reads of the methods of a value it is given, beside
// the fields every destination of this class reads. A value that reaches two
// destinations keeps what both of them read, which is the union of their reaches, so
// each destination is one bit rather than one value.
type reach uint16

// What a destination reads of a value's methods: nothing, every exported method the
// type declares, or the methods one direction of one encoder resolves by name.
const (
	reachNoMethod        reach = 0
	reachExportedMethods reach = 1 << 0
	reachJSONEncode      reach = 1 << 1
	reachJSONDecode      reach = 1 << 2
	reachXMLEncode       reach = 1 << 3
	reachXMLDecode       reach = 1 << 4
	reachGobEncode       reach = 1 << 5
	reachGobDecode       reach = 1 << 6

	// The two directions, so one entry point of a destination keeps its own.
	reachEncoding reach = reachJSONEncode | reachXMLEncode | reachGobEncode
	reachDecoding reach = reachJSONDecode | reachXMLDecode | reachGobDecode
)

// methodsResolvedByName are the methods a destination resolves by name on a value it
// encodes or decodes, each with the destinations that resolve it. A destination
// walking a value keeps such a method on every defined type it reaches, because the
// destination looks the method up on the type and the reference graph holds no edge
// to it.
var methodsResolvedByName = map[string]reach{
	"AppendText":        reachJSONEncode,
	"GobDecode":         reachGobDecode,
	"GobEncode":         reachGobEncode,
	"MarshalBinary":     reachGobEncode,
	"MarshalJSON":       reachJSONEncode,
	"MarshalJSONTo":     reachJSONEncode,
	"MarshalText":       reachJSONEncode | reachXMLEncode,
	"MarshalXML":        reachXMLEncode,
	"MarshalXMLAttr":    reachXMLEncode,
	"UnmarshalBinary":   reachGobDecode,
	"UnmarshalJSON":     reachJSONDecode,
	"UnmarshalJSONFrom": reachJSONDecode,
	"UnmarshalText":     reachJSONDecode | reachXMLDecode,
	"UnmarshalXML":      reachXMLDecode,
	"UnmarshalXMLAttr":  reachXMLDecode,
}

// The prefixes an entry point of a destination package names its direction by.
var (
	encodingEntryPoints = [...]string{"Encode", "Marshal"}
	decodingEntryPoints = [...]string{"Decode", "Unmarshal"}
)

// reads reports whether a destination of this reach reads one method of a defined
// type it walks.
func (r reach) reads(m *types.Func) bool {
	if r&reachExportedMethods != 0 && m.Exported() {
		return true
	}
	return r&methodsResolvedByName[m.Name()] != 0
}

// forEntryPoint is what one entry point of a destination package reads of the
// methods of a value it is given: an entry point that encodes a value resolves no
// method that decodes one, and the other way about. An entry point naming neither
// direction reads both, because a registration takes a value for either.
func (r reach) forEntryPoint(name string) reach {
	switch {
	case hasAnyPrefix(name, encodingEntryPoints[:]):
		return r &^ reachDecoding
	case hasAnyPrefix(name, decodingEntryPoints[:]):
		return r &^ reachEncoding
	default:
		return r
	}
}

// hasAnyPrefix reports whether name begins with one of the prefixes.
func hasAnyPrefix(name string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
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
// class, each named by its package path and its name. A conversion to one reaches
// every exported method, because the interface is a set of methods and the package
// behind it calls them.
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

// namedDestinationPackages joins the destination tables, so the set of named
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
// thing. A standard encoder retains the methods it resolves by name in the direction
// the entry point encodes or decodes in, which [methodsResolvedByName] lists; a
// database scan and the comparison of two values by reflection read fields and call
// no method of the value, so those retain fields alone. A template engine selects a
// method by the syntax it selects a field with, a structured-logging handler renders
// a value through the method it answers with, a sort interface is three methods, and
// every other entry point of the reflection package hands out a value from which a
// method is reachable by name, so those retain the exported methods as well.
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
// call it forwards to. A wrapper retains what its destination retains rather than the
// full set, because its body is in the program: a value handed to a wrapper that
// encodes it keeps the fields and the marshalling methods an encoder reads, one handed
// to a wrapper that renders it through a template keeps every exported method, and a
// wrapper with two destinations retains the union.
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
		kept:    newRetention(in, EncodingReflection),
		edge:    newBoundary(in, namedDestinationParameter),
		targets: conversionTargets(in.Result.Packages),
	}
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
	kept    *retention
	edge    *boundary
	targets []interfaceTarget
}

// namedDestinationParameter reports whether one parameter of one function is a
// destination this class names by itself, which every parameter of one is (what a
// destination call names is the argument position and not the parameter's meaning),
// and what that destination reads of the value it is given. It is the base case the
// boundary's forwarding fixpoint starts from, so a wrapper that hands its own
// parameter to an encoder carries the encoder's set and one that hands it to a
// template engine carries the engine's.
func namedDestinationParameter(fn *types.Func, _ int) (reach, bool) {
	d, found := encodingDestination(fn)
	return d.reach, found
}

// interfaceTarget is one interface the class treats as a destination, resolved
// from the loaded program so a conversion is matched on the declared type rather
// than on a method-set shape a local interface could imitate.
type interfaceTarget struct {
	iface *types.Interface
	name  string
}

// destination is what one call is to the class: the clause an exemption records,
// what it reads of the methods of the value it is given beside its fields, and
// whether only a pointer argument flows into it.
type destination struct {
	detail   string
	reach    reach
	pointers bool
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
		failed = f.crossingArguments(info, call)
		return failed == nil
	})
	return failed
}

// crossingArguments records the struct values one call carries out of the analysed
// program, which the boundary's crossing test decides argument by argument.
//
// What is retained is what reads the value where it arrives. A callee the program does
// not hold retains the full set, every exported method included, because its body
// decides what it reads and the analysis does not have it; a wrapper the program does
// hold retains what its own destination retains, so a value handed to a wrapper that
// encodes it keeps its fields and the methods that encoder resolves by name. The detail
// names the immediate callee either way: a wrapper's caller reads the wrapper's name at
// its own call.
func (f *encodingFlow) crossingArguments(info *types.Info, call *ast.CallExpr) error {
	for i, arg := range call.Args {
		out, crosses := f.edge.crossing(info, call, i)
		if !crosses {
			continue
		}
		d := destination{detail: "passed to " + out.callee.FullName(), reach: out.reach}
		if err := f.retain(info.TypeOf(arg), arg.Pos(), d); err != nil {
			return err
		}
	}
	return nil
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
			detail: "converted to " + name, reach: reachExportedMethods,
		}); err != nil {
			return err
		}
	}
	return nil
}

// retain records what every defined type a destination walking a value of t reads
// keeps: its exported fields, every field of it that carries a struct tag whether
// that field is exported or not, because a tagged field is named by its tag and not
// by its visibility, and the methods the destination reads.
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

// members records what one destination reads of one defined type: the methods of it
// the destination reads, and then the fields every destination of this class reads.
func (f *encodingFlow) members(named *types.Named, site token.Position, d destination) {
	origin := named.Origin()
	for m := range origin.Methods() {
		if d.reach.reads(m) {
			f.kept.record(m, site, d.detail)
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
	if pkg.Path() == reflectPackage && fn.Name() == deepEqual {
		return destination{detail: "passed to " + fn.FullName(), reach: reachNoMethod}, true
	}
	if reads, found := destinationPackages[pkg.Path()]; found {
		return destination{
			detail: "passed to " + fn.FullName(),
			reach:  reads.forEntryPoint(fn.Name()),
		}, true
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
		return destination{detail: "passed to " + fn.FullName(), reach: reachExportedMethods}, true
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

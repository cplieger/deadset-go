package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// destinationPackages are the packages whose every function and method may name
// the members of a value it is given at run time, each with the methods it resolves
// by name: a standard encoder resolves the methods of [methodsResolvedByName] and
// calls no other, while a template engine selects a method by the syntax it selects
// a field with.
var destinationPackages = map[string]methodReach{
	"encoding/gob":     reachGobEncode | reachGobDecode,
	"encoding/json":    reachJSONEncode | reachJSONDecode,
	"encoding/json/v2": reachJSONEncode | reachJSONDecode,
	"encoding/xml":     reachXMLEncode | reachXMLDecode,
	"html/template":    reachExportedMethods,
	"reflect":          reachExportedMethods,
	"text/template":    reachExportedMethods,
}

// jsonPackages are the encoders whose decoder can refuse a document naming a member
// the type lacks.
var jsonPackages = map[string]bool{"encoding/json": true, "encoding/json/v2": true}

// xmlPackage is the XML encoder's package.
const xmlPackage = "encoding/xml"

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

	// xmlName is whether an XML decoder compares the value's XMLName field.
	xmlName bool
}

// tagKeys is a set of the struct tag keys an encoder names a field by.
type tagKeys uint8

// The tag keys an encoder skips a field by, each one bit.
const (
	jsonKey tagKeys = 1 << iota
	xmlKey
)

// tagKeyOf is the tag key the encoders of one package read, and zero for a package
// whose encoder reads no tag.
func tagKeyOf(pkg string) tagKeys {
	switch {
	case jsonPackages[pkg]:
		return jsonKey
	case pkg == xmlPackage:
		return xmlKey
	default:
		return 0
	}
}

// skips reports whether every encoder the fields are read through skips one field,
// its tag naming the key "-" for each of them. A tag of "-," names the key "-".
func (k tagKeys) skips(tag string) bool {
	if k == 0 {
		return false
	}
	st := reflect.StructTag(tag)
	for key, name := range map[tagKeys]string{jsonKey: "json", xmlKey: "xml"} {
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
	}
	switch {
	case !r.fields:
		joined.keys = other.keys
	case !other.fields:
		joined.keys = r.keys
	case r.keys != 0 && other.keys != 0:
		joined.keys = r.keys | other.keys
	}
	joined.outside = joinPackages(r.outside, other.outside)
	joined.contained = joinPackages(r.contained, other.contained)
	return joined
}

// joinPackages is the packages of two lists ordered by path, each once.
func joinPackages(held, other []*types.Package) []*types.Package {
	joined := slices.Clone(held)
	for _, pkg := range other {
		at, found := slices.BinarySearchFunc(joined, pkg.Path(), func(p *types.Package, path string) int {
			return strings.Compare(p.Path(), path)
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
		len(widened.contained) == len(r.contained) && widened.keys == r.keys && widened.xmlName == r.xmlName
}

// storesOnly reports whether every destination of this reach is a standard
// container, which hands the value back unchanged and so reads nothing beneath it.
func (r reach) storesOnly() bool {
	return len(r.contained) > 0 && r.methods == 0 && !r.fields && !r.xmlName && len(r.outside) == 0
}

// methodsResolvedByName are the methods a destination resolves by name on a value it
// encodes or decodes, each with the destinations that resolve it. A destination
// walking a value keeps such a method on every defined type it reaches, because the
// destination looks the method up on the type and the reference graph holds no edge
// to it.
var methodsResolvedByName = map[string]methodReach{
	"AppendText":        reachJSONEncode,
	"GobDecode":         reachGobDecode,
	"GobEncode":         reachGobEncode,
	"LogValue":          reachLogValue,
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
func (r methodReach) reads(m *types.Func) bool {
	if r&reachExportedMethods != 0 && m.Exported() {
		return true
	}
	return r&methodsResolvedByName[m.Name()] != 0
}

// entryPoint is what one function of an encoder package retains of a value it is
// given: an encoding entry point reads fields and resolves the encoding methods, a
// decoding one fills fields through reflection, which reads none, and resolves the
// decoding methods, and any other, a registration among them, retains both. A JSON
// decoder retains fields as well where the program makes it refuse unknown members.
func entryPoint(pkg, name string, resolved methodReach, unknownMembers bool) reach {
	key := tagKeyOf(pkg)
	switch {
	case hasAnyPrefix(name, encodingEntryPoints[:]):
		return reach{methods: resolved &^ reachDecoding, fields: true, keys: key}
	case hasAnyPrefix(name, decodingEntryPoints[:]):
		return reach{
			methods: resolved &^ reachEncoding, fields: unknownMembers && jsonPackages[pkg], keys: key,
			xmlName: pkg == xmlPackage,
		}
	default:
		return reach{methods: resolved, fields: true, keys: key, xmlName: pkg == xmlPackage}
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
// type declares. Any other entry point reaches the exported methods only in a
// program that names one of [methodFinders].
const (
	reflectPackage = "reflect"
	deepEqual      = "DeepEqual"
)

// methodFinders are the methods of reflect.Value and reflect.Type that find a method
// of a value; a program calls a method through a Value only after one of them finds
// it. A program-wide answer is a superset of every value that reaches one.
var methodFinders = map[string]bool{"Method": true, "MethodByName": true, "NumMethod": true}

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

// namedDestinations are the packages whose destinations a rule names one by one: the
// encoders, the template engines and the reflection package above, the database and
// sorting packages, and the packages the format-verb class reads. The rule for a
// callee the analysis cannot read passes over them, so one call is one record.
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
// values reach a destination that reads them by name at run time keeps the members
// that destination reads, on the type and on every type reached from its fields,
// because the reference graph holds no edge to them. What a destination retains is
// [destination]'s, and every argument of a destination function flows. The walk
// reaches through an interface-typed field to the types the program stores in it
// ([storedTypes]), and a promoted method is retained where the embedded type is.
func EncodingReflectionDetector(in *Input) ([]graph.Exemption, error) {
	assert := assertableOf(in)
	f := &encodingFlow{
		kept:           newRetention(in, EncodingReflection),
		targets:        conversionTargets(in.Result.Packages),
		unknownMembers: namesFunction(in, jsonPackages, unknownMemberRefusals),
		findsMethods:   namesFunction(in, map[string]bool{reflectPackage: true}, methodFinders),
		closures:       make(map[string]*importClosure),
		assert:         assert,
		satisfying:     make(map[satisfactionKey]map[string]bool),
		methodNames:    make(map[*types.Named]map[string]bool),
	}
	f.decode = newDecoders(f, assert.source)
	f.edge = newBoundary(in, f.namedDestinationParameter)
	f.stored = storedTypesOf(in, f.edge.sites)
	if err := f.walkCalls(); err != nil {
		return nil, err
	}
	if err := f.walkConversions(); err != nil {
		return nil, err
	}
	if err := f.walkReturns(); err != nil {
		return nil, err
	}
	return f.kept.exemptions(), nil
}

// encodingFlow accumulates the class over one loaded configuration.
type encodingFlow struct {
	kept        *retention
	edge        *boundary
	stored      *storedTypes
	closures    map[string]*importClosure // by package path, computed on first use
	assert      *assertable
	outside     *[]*types.Package                   // the program's outside imports, on first use
	byName      map[string][]interfaceMethod        // their interfaces' methods, on first use
	satisfying  map[satisfactionKey]map[string]bool // the method names retained
	methodNames map[*types.Named]map[string]bool    // the names a type or a pointer to it declares
	decode      *decoders
	targets     []interfaceTarget

	// unknownMembers is whether the program makes a JSON decoder refuse a document
	// naming a member the type lacks.
	unknownMembers bool
	// findsMethods is whether the program names a reflection method finder.
	findsMethods bool
}

// namedDestinationParameter reports whether one parameter of one function is a
// destination this class names by itself, which every parameter of one is, and what
// that destination retains. It is the base case of the boundary's forwarding
// fixpoint.
func (f *encodingFlow) namedDestinationParameter(fn *types.Func, at int) (reach, bool) {
	if d, found := f.encodingDestination(fn); found {
		return d.reach, true
	}
	return f.decode.at(fn, at)
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
	for _, p := range graph.SortedPackages(f.kept.in.Result.Packages) {
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
	for _, decl := range file.Decls {
		enclosing := enclosingFunc(info, decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || failed != nil {
				return failed == nil
			}
			fn, isFunc := resolveObject(info, call.Fun).(*types.Func)
			if !isFunc {
				return true
			}
			if d, reaches := f.encodingDestination(fn); reaches {
				failed = f.arguments(info, enclosing, call.Args, &d)
				return failed == nil
			}
			failed = f.crossingArguments(info, call)
			return failed == nil
		})
		if failed != nil {
			return failed
		}
	}
	return nil
}

// crossingArguments records the struct values one call carries out of the analysed
// program, which the boundary's crossing test decides argument by argument. What is
// retained is what the boundary carries for the callee, and the detail names the
// immediate callee, so a wrapper's caller reads the wrapper's name.
func (f *encodingFlow) crossingArguments(info *types.Info, call *ast.CallExpr) error {
	callee, _ := resolveObject(info, call.Fun).(*types.Func)
	for i, arg := range call.Args {
		if at, supplies := parameterAt(callee.Signature(), i); supplies && !f.edge.declares(callee) {
			if decoded, decodes := f.decode.at(callee, at); decodes {
				d := destination{detail: "decoded by " + callee.FullName(), reach: decoded}
				if err := f.retain(info.TypeOf(arg), arg.Pos(), &d); err != nil {
					return err
				}
				continue
			}
		}
		out, crosses := f.edge.crossing(info, call, i)
		if !crosses {
			continue
		}
		d := destination{detail: "passed to " + out.callee.FullName(), reach: out.reach}
		if err := f.retain(info.TypeOf(arg), arg.Pos(), &d); err != nil {
			return err
		}
	}
	return nil
}

// arguments records the types the arguments of one destination call in the body of
// enclosing carry. A slice, an array or a map holding interface values carries the
// types the program stores in it ([storedTypes.argument]).
func (f *encodingFlow) arguments(info *types.Info, enclosing *types.Func, args []ast.Expr, d *destination) error {
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
		for _, stored := range f.stored.argument(info, enclosing, arg) {
			if err := f.retain(stored, arg.Pos(), d); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkReturns records every value a method of the program returns as an
// interface-typed result where the method implements a method of an exported
// interface an outside package declares: code outside the analysis calls the method
// through that interface and receives the value, so the value reaches that package.
func (f *encodingFlow) walkReturns() error {
	for _, one := range programFunctions(f.kept.in) {
		if one.decl.Recv == nil || one.decl.Body == nil {
			continue
		}
		answered, implements := f.outsideMethod(one.fn)
		if !implements {
			continue
		}
		d := destination{
			detail: "returned through " + answered.FullName(),
			reach:  reach{methods: reachEncoding | reachDecoding, fields: true, outside: []*types.Package{answered.Pkg()}},
		}
		if err := f.returned(one, &d); err != nil {
			return err
		}
	}
	return nil
}

// outsideMethod is the method of an exported interface declared outside the program
// that one method of the program implements, where its results hold an interface.
func (f *encodingFlow) outsideMethod(fn *types.Func) (*types.Func, bool) {
	sig := fn.Signature()
	if !slices.ContainsFunc(slices.Collect(sig.Results().Variables()), func(v *types.Var) bool { return types.IsInterface(v.Type()) }) {
		return nil, false
	}
	recv := sig.Recv().Type()
	for _, candidate := range f.outsideInterfaceMethods()[fn.Name()] {
		if types.Implements(recv, candidate.iface) {
			return candidate.method, true
		}
	}
	return nil, false
}

// interfaceMethod is one method of one exported interface an outside package
// declares.
type interfaceMethod struct {
	iface  *types.Interface
	method *types.Func
}

// outsideInterfaceMethods indexes the methods of the exported interfaces the outside
// packages declare by name, on first use.
func (f *encodingFlow) outsideInterfaceMethods() map[string][]interfaceMethod {
	if f.byName != nil {
		return f.byName
	}
	f.byName = make(map[string][]interfaceMethod)
	for _, pkg := range f.outsidePackages() {
		for _, iface := range declaredInterfaces(pkg, false) {
			for m := range iface.Methods() {
				f.byName[m.Name()] = append(f.byName[m.Name()], interfaceMethod{iface: iface, method: m})
			}
		}
	}
	return f.byName
}

// returned records the value each return statement of one method hands back at an
// interface-typed result, a function literal's returns excepted.
func (f *encodingFlow) returned(one programFunction, d *destination) error {
	results := one.fn.Signature().Results()
	var failed error
	ast.Inspect(one.decl.Body, func(n ast.Node) bool {
		if _, literal := n.(*ast.FuncLit); literal || failed != nil {
			return false
		}
		ret, isReturn := n.(*ast.ReturnStmt)
		if !isReturn || len(ret.Results) != results.Len() {
			return true
		}
		for i, value := range ret.Results {
			at := one.info.TypeOf(value)
			if !types.IsInterface(results.At(i).Type()) || at == nil || !carriesStruct(at) {
				continue
			}
			failed = f.retain(at, value.Pos(), d)
		}
		return failed == nil
	})
	return failed
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
		if err := f.retain(c.From, c.Site, &destination{
			detail: "converted to " + name, reach: reach{methods: reachExportedMethods, fields: true},
		}); err != nil {
			return err
		}
	}
	return nil
}

// retain records what every defined type a destination walking a value of t reads
// keeps.
func (f *encodingFlow) retain(t types.Type, at token.Pos, d *destination) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	reached := f.encoderReach(t)
	if d.reach.storesOnly() {
		reached = concreteTypes(namedTypesReached(t))
	}
	for _, named := range reached {
		f.members(named, site, d)
	}
	return nil
}

// members records what one destination reads of one defined type: the methods it
// resolves by name, the methods an outside package can name, and, where it reads
// fields, the exported fields and every field carrying a tag, because a tagged field
// is named by its tag and not by its visibility.
func (f *encodingFlow) members(named *types.Named, site token.Position, d *destination) {
	origin := named.Origin()
	every, satisfying := f.outsideMethods(named, d.reach)
	for m := range origin.Methods() {
		if d.reach.methods.reads(m) || (every && m.Exported()) || satisfying[m.Name()] {
			f.kept.record(m, site, d.detail)
		}
	}
	if st, isStruct := origin.Underlying().(*types.Struct); isStruct {
		f.fields(st, site, d)
	}
}

// fields records the fields of one struct a destination reads, and those of each
// struct type with no name a field it visits holds, whose fields are symbols of the
// type that holds the struct.
func (f *encodingFlow) fields(st *types.Struct, site token.Position, d *destination) {
	for i := range st.NumFields() {
		field := st.Field(i)
		visited := (field.Exported() || st.Tag(i) != "") && !d.reach.keys.skips(st.Tag(i))
		switch {
		case d.reach.fields && visited:
			f.kept.record(field, site, d.detail)
		case d.reach.xmlName && field.Name() == xmlNameField && isXMLName(field.Type()):
			f.kept.record(field, site, d.detail)
		}
		if !visited {
			continue
		}
		_, inner := typesReached(field.Type())
		for _, held := range inner {
			f.fields(held, site, d)
		}
	}
}

// xmlNameField is the field an XML decoder compares with the element's name.
const xmlNameField = "XMLName"

// isXMLName reports whether a type is encoding/xml's Name.
func isXMLName(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == xmlPackage && named.Obj().Name() == "Name"
}

// encodingDestination reports whether a call to fn reaches the class, and how.
func (f *encodingFlow) encodingDestination(fn *types.Func) (destination, bool) {
	pkg := fn.Pkg()
	if pkg == nil {
		return destination{}, false
	}
	if _, contains := graph.StandardContainer(fn); contains {
		return destination{detail: "passed to " + fn.FullName(), reach: reach{contained: []*types.Package{pkg}}}, true
	}
	detail := "passed to " + fn.FullName()
	if pkg.Path() == reflectPackage && (fn.Name() == deepEqual || !f.findsMethods) {
		return destination{detail: detail, reach: reach{fields: true}}, true
	}
	if resolved, found := destinationPackages[pkg.Path()]; found {
		if resolved&reachEncoding != 0 {
			return destination{detail: detail, reach: entryPoint(pkg.Path(), fn.Name(), resolved, f.unknownMembers)}, true
		}
		return destination{detail: detail, reach: reach{methods: resolved, fields: true}}, true
	}
	if pkg.Path() == sqlPackage && fn.Name() == sqlScanMethod && scansARow(fn) {
		return destination{detail: "scanned by " + fn.FullName(), reach: reach{fields: true}, pointers: true}, true
	}
	// The formatting class reads the same calls for String and Error.
	if _, logs := slogFunctions[fn.Name()]; pkg.Path() == slogPackage && logs {
		return destination{detail: detail, reach: reach{methods: reachLogValue, fields: true}}, true
	}
	return destination{}, false
}

// unknownMemberRefusals are the functions by which a program makes a JSON decoder
// refuse a document naming a member the type lacks: a method of the first package's
// decoder, and an option of the second package.
var unknownMemberRefusals = map[string]bool{"DisallowUnknownFields": true, "RejectUnknownMembers": true}

// namesFunction reports whether any file of the analysed program uses a function or
// method of one of the packages whose name is one of names.
func namesFunction(in *Input, pkgPaths, names map[string]bool) bool {
	for _, p := range programPackages(in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, object := range p.TypesInfo.Uses {
			if _, isFunc := object.(*types.Func); isFunc && object.Pkg() != nil &&
				pkgPaths[object.Pkg().Path()] && names[object.Name()] {
				return true
			}
		}
	}
	return false
}

// templatePackages are the template engines, whose presence in an outside package's
// import closure lets it resolve any exported method by name.
var templatePackages = map[string]bool{"text/template": true, "html/template": true}

// importClosure is what one package outside the analysed program can name: every
// interface it declares or writes as a literal, the exported interfaces the packages
// it imports at any depth and the outside packages of the program's import closure
// that import it declare, and whether a template engine is among the packages it
// imports.
type importClosure struct {
	interfaces []*types.Interface
	templates  bool
}

// closureOf reads one package's import closure on first use.
func (f *encodingFlow) closureOf(pkg *types.Package) *importClosure {
	if held, known := f.closures[pkg.Path()]; known {
		return held
	}
	imported := f.assert.importedBy(pkg)
	closure := &importClosure{templates: imported.templates}
	closure.interfaces = append(slices.Clone(f.assert.ownInterfaces(pkg)), imported.interfaces...)
	for _, importer := range f.outsidePackages() {
		if slices.ContainsFunc(importer.Imports(), func(p *types.Package) bool { return p.Path() == pkg.Path() }) {
			closure.interfaces = append(closure.interfaces, declaredInterfaces(importer, false)...)
		}
	}
	f.closures[pkg.Path()] = closure
	return closure
}

// outsidePackages is every package the program's import closure holds that is
// neither the target's nor a loaded consumer's, ordered by path.
func (f *encodingFlow) outsidePackages() []*types.Package {
	if f.outside != nil {
		return *f.outside
	}
	own := make(map[string]bool)
	var queue []*types.Package
	for _, p := range programPackages(f.kept.in.Result) {
		if p.Types != nil {
			own[p.PkgPath] = true
			queue = append(queue, p.Types)
		}
	}
	seen := make(map[string]bool)
	var found []*types.Package
	for len(queue) > 0 {
		one := queue[0]
		queue = queue[1:]
		for _, imported := range one.Imports() {
			if seen[imported.Path()] {
				continue
			}
			seen[imported.Path()] = true
			queue = append(queue, imported)
			if !own[imported.Path()] {
				found = append(found, imported)
			}
		}
	}
	slices.SortFunc(found, func(a, b *types.Package) int { return strings.Compare(a.Path(), b.Path()) })
	f.outside = &found
	return found
}

// declaredInterfaces is every non-generic interface with methods one package
// declares at package level whose type set is its method set, the unexported ones
// only where every is set. A generic interface is skipped, because
// [types.Implements] decides no uninstantiated one, and so is an interface holding a
// type term or embedding comparable, which only constrains a type parameter.
func declaredInterfaces(pkg *types.Package, every bool) []*types.Interface {
	var found []*types.Interface
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		typeName, isType := scope.Lookup(name).(*types.TypeName)
		if !isType || typeName.IsAlias() || (!every && !typeName.Exported()) {
			continue
		}
		named, isNamed := typeName.Type().(*types.Named)
		if !isNamed || named.TypeParams().Len() > 0 {
			continue
		}
		iface, isInterface := named.Underlying().(*types.Interface)
		if isInterface && iface.IsMethodSet() && iface.NumMethods() > 0 {
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
func (f *encodingFlow) outsideMethods(named *types.Named, r reach) (every bool, names map[string]bool) {
	if len(r.outside) == 0 && len(r.contained) == 0 {
		return false, nil
	}
	names = make(map[string]bool)
	for _, pkg := range r.outside {
		closure := f.closureOf(pkg)
		if closure.templates {
			return true, nil
		}
		maps.Copy(names, f.satisfied(satisfactionKey{named: named, pkg: pkg.Path()}, closure.interfaces))
	}
	for _, pkg := range r.contained {
		key := satisfactionKey{named: named, pkg: pkg.Path(), contained: true}
		maps.Copy(names, f.satisfied(key, f.assert.ownInterfaces(pkg)))
	}
	return false, names
}

// satisfied names the methods by which one type satisfies one package's interfaces,
// computed once per type and package.
func (f *encodingFlow) satisfied(key satisfactionKey, interfaces []*types.Interface) map[string]bool {
	held, known := f.satisfying[key]
	if !known {
		held = satisfiedMethods(key.named, f.declaredNames(key.named), interfaces)
		f.satisfying[key] = held
	}
	return held
}

// satisfactionKey is one type and one package outside the program it reaches, as
// a callee or as a container that stores it.
type satisfactionKey struct {
	named     *types.Named
	pkg       string
	contained bool
}

// declaredNames is the name of every method in the method set of a pointer to the
// type, which holds the type's own.
func (f *encodingFlow) declaredNames(named *types.Named) map[string]bool {
	if held, known := f.methodNames[named]; known {
		return held
	}
	held := declaredMethodNames(named)
	f.methodNames[named] = held
	return held
}

// declaredMethodNames is the name of every method in the method set of a pointer
// to the type.
func declaredMethodNames(named *types.Named) map[string]bool {
	set := types.NewMethodSet(types.NewPointer(named))
	held := make(map[string]bool, set.Len())
	for selection := range set.Methods() {
		held[selection.Obj().Name()] = true
	}
	return held
}

// satisfiedMethods names the methods of every interface the type, or a pointer to
// it, implements. An interface naming a method the type's names lack is one
// neither implements, so [types.Implements] is asked only of the rest.
func satisfiedMethods(named *types.Named, declared map[string]bool, interfaces []*types.Interface) map[string]bool {
	names := make(map[string]bool)
	pointer := types.NewPointer(named)
	for _, iface := range interfaces {
		if !namesEvery(declared, iface) {
			continue
		}
		if !types.Implements(named, iface) && !types.Implements(pointer, iface) {
			continue
		}
		for method := range iface.Methods() {
			names[method.Name()] = true
		}
	}
	return names
}

// namesEvery reports whether every method of the interface has a name among names.
func namesEvery(names map[string]bool, iface *types.Interface) bool {
	for method := range iface.Methods() {
		if !names[method.Name()] {
			return false
		}
	}
	return true
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
		seen[types.TypeString(named, nil)] = true
	}
	for at := 0; at < len(found); at++ {
		for _, next := range f.membersReached(found[at]) {
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
			for _, stored := range f.stored.of(field) {
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

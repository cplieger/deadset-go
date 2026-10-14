package exempt

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
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
	htmlTemplate:       reachExportedMethods,
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

	// encoders are the encoding directions of the standard encoders the fields are
	// read through, where every reader of the fields is one, which walks no field of
	// a type that encodes itself ([selfEncoders]). Zero, the fields are read whatever
	// methods the type has.
	encoders methodReach

	// fills is whether a decoder outside the analysed program fills the fields
	// and may read them ([fillingNames]).
	fills bool

	// xmlName is whether an XML decoder compares the value's XMLName field.
	xmlName bool

	// holders is whether a decoder reads a field of a pointer, interface or map
	// type the program writes, to decode into the value the field holds.
	holders bool

	// results is whether the destination also reads what each method it calls
	// returns, as a template engine evaluating a chain does.
	results bool
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
		holders: r.holders || other.holders,
		fills:   r.fills || other.fills,
		results: r.results || other.results,
	}
	switch {
	case !r.fields:
		joined.keys, joined.encoders = other.keys, other.encoders
	case !other.fields:
		joined.keys, joined.encoders = r.keys, r.encoders
	default:
		if r.keys != 0 && other.keys != 0 {
			joined.keys = r.keys | other.keys
		}
		if r.encoders != 0 && other.encoders != 0 {
			joined.encoders = r.encoders | other.encoders
		}
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
		len(widened.contained) == len(r.contained) && widened.keys == r.keys && widened.xmlName == r.xmlName &&
		widened.holders == r.holders && widened.encoders == r.encoders && widened.fills == r.fills &&
		widened.results == r.results
}

// storesOnly reports whether every destination of this reach is a standard
// container, which hands the value back unchanged and so reads nothing beneath it.
func (r reach) storesOnly() bool {
	return len(r.contained) > 0 && r.methods == 0 && !r.fields && !r.xmlName && !r.holders && !r.fills &&
		len(r.outside) == 0
}

// The two encoding methods more than one encoder resolves.
const (
	marshalJSON = "MarshalJSON"
	marshalText = "MarshalText"
)

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
	marshalJSON:         reachJSONEncode,
	"MarshalJSONTo":     reachJSONEncode,
	marshalText:         reachJSONEncode | reachXMLEncode,
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
		return reach{methods: resolved &^ reachDecoding, fields: true, keys: key, encoders: resolved & reachEncoding}
	case hasAnyPrefix(name, decodingEntryPoints[:]):
		return reach{
			methods: resolved &^ reachEncoding, fields: unknownMembers && jsonPackages[pkg], keys: key,
			xmlName: pkg == xmlPackage, holders: true,
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

// sortPackage is the sorting package: its empty-interface parameters swap elements
// and read no field.
const sortPackage = "sort"

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
	conversions, err := programConversions(in)
	if err != nil {
		return nil, fmt.Errorf("encoding-reflection: %w", err)
	}
	f := &encodingFlow{
		conversions:    conversions,
		kept:           newRetention(in, EncodingReflection),
		filled:         newRetention(in, EncodingReflection),
		own:            programPaths(in),
		unknownMembers: namesFunction(in, jsonPackages, unknownMemberRefusals),
		findsMethods:   namesFunction(in, map[string]bool{reflectPackage: true}, methodFinders),
		closures:       make(map[string]*importClosure),
		assert:         assert,
		satisfying:     make(map[satisfactionKey]map[string]bool),
		methodNames:    make(map[*types.Named]map[string]bool),
		typeArgs:       typeArgumentsOf(programPackages(in.Result)),
	}
	f.templates = newTemplateOrigins(in)
	f.decode = newDecoders(f, assert.source)
	f.edge = newBoundary(in, f.namedDestinationParameter)
	f.stored = storedTypesOf(in, f.edge.sites)
	if err := f.walkCalls(); err != nil {
		return nil, err
	}
	if err := f.walkReturns(); err != nil {
		return nil, err
	}
	if err := f.walkTypeArguments(); err != nil {
		return nil, err
	}
	if err := f.walkFieldAssertions(); err != nil {
		return nil, err
	}
	kept := f.kept.exemptions()
	retained := make(map[graph.SymbolID]bool, len(kept))
	for i := range kept {
		retained[kept[i].ID] = retained[kept[i].ID] || holdsInMode(&kept[i], in.Mode)
	}
	in.filled = slices.DeleteFunc(f.filled.exemptions(), func(e graph.Exemption) bool {
		return retained[e.ID] || !holdsInMode(&e, in.Mode)
	})
	return kept, nil
}

// encodingFlow accumulates the class over one loaded configuration.
type encodingFlow struct {
	typeArgs    *typeArguments
	kept        *retention
	filled      *retention      // the fields only a filling callee retains ([fillingNames])
	own         map[string]bool // the program's package paths
	edge        *boundary
	stored      *storedTypes
	closures    map[string]*importClosure // by package path, computed on first use
	assert      *assertable
	byName      map[string][]interfaceMethod        // their interfaces' methods, on first use
	satisfying  map[satisfactionKey]map[string]bool // the method names retained
	methodNames map[*types.Named]map[string]bool    // the names a type or a pointer to it declares
	decode      *decoders
	writes      map[token.Position]bool       // the fields the program writes, on first use
	reached     map[types.Type][]*types.Named // encoderReach's answers, which spell every type they meet
	selfEncoded map[selfEncodingKey]selfEncodedTypes
	templates   *templateOrigins   // which templates an execution's receiver holds
	conversions []graph.Conversion // the program's, consumers' included

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
func (f *encodingFlow) namedDestinationParameter(fn *types.Func, at int, site callSite) (reach, bool) {
	if d, found := f.encodingDestination(fn); found {
		narrowReflection(fn, site, &d)
		return d.reach, true
	}
	if decoded, decodes := f.decode.at(fn, at); decodes {
		return decoded, true
	}
	if f.fills(fn, at) {
		return opaqueParameters(fn)[0].reach.filling(), true
	}
	return reach{}, false
}

// fillingNames are the name prefixes by which a function or method outside the
// analysed program says it fills the value it is given from a document. Such a callee
// may also read what it fills, as a validating binder does, so a field it alone
// retains is reported at the class possible rather than held live.
var fillingNames = [...]string{"Bind", "Decode", "MustBind", "ShouldBind", "Unmarshal"}

// fills reports whether the empty-interface parameter at one position of fn fills its
// argument: fn is a function or method outside the analysed program, of a package no
// destination rule names, whose name says it fills its argument, and whose own body
// does not hand the parameter to a destination that reads fields
// ([decoders.readsFields]).
func (f *encodingFlow) fills(fn *types.Func, at int) bool {
	pkg := fn.Pkg()
	return pkg != nil && !f.own[pkg.Path()] && !namedDestinations[pkg.Path()] && hasAnyPrefix(fn.Name(), fillingNames[:]) &&
		slices.Contains(erasedParameters(fn.Signature()), at) && !f.decode.readsFields(fn, at)
}

// filling is what a filling callee retains in place of the fields an opaque callee
// reads.
func (r reach) filling() reach {
	r.fields, r.fills, r.keys, r.encoders = false, true, 0, 0
	return r
}

// programPaths is the import path of every package of the analysed program.
func programPaths(in *Input) map[string]bool {
	held := make(map[string]bool)
	for _, p := range programPackages(in.Result) {
		held[p.PkgPath] = true
	}
	return held
}

// printingMethods are the methods a template engine calls on a value it prints
// whatever its templates name, through fmt (https://pkg.go.dev/text/template#hdr-Actions).
var printingMethods = map[string]bool{errorMethod: true, "Format": true, stringMethod: true}

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

// templateNames is [templateOrigins.executed]'s names for a call of fn, nil where
// the call is no template execution or its set is not read whole.
func (f *encodingFlow) templateNames(info *types.Info, file *ast.File, fn *types.Func, call *ast.CallExpr) map[string]bool {
	if !templatePackages[fn.Pkg().Path()] || !templateExecutions[fn.Name()] {
		return nil
	}
	return f.templates.executed(info, filepath.Dir(f.kept.in.Result.Fset.Position(file.FileStart).Filename), call)
}

// narrowReflection drops the methods a reflect destination reads where what the call
// returns reaches no method finder ([findsMethodsFrom]).
func narrowReflection(fn *types.Func, site callSite, d *destination) {
	if fn.Pkg().Path() == reflectPackage && d.reach.methods != 0 && site.call != nil &&
		!findsMethodsFrom(site.info, site.decl, site.call) {
		d.reach.methods = 0
	}
}

// findsMethodsFrom reports whether what one reflect call returns may reach a method
// finder: false only where the call heads a chain of reflect method calls, none a
// finder, whose last result is neither a reflect.Value nor a reflect.Type, or is one
// a local variable of decl takes whose every use heads such a chain.
func findsMethodsFrom(info *types.Info, decl ast.Node, call *ast.CallExpr) bool {
	return chainFindsMethods(info, decl, call, map[types.Object]bool{})
}

// chainFindsMethods is [findsMethodsFrom] for the chain start heads, seen holding the
// local variables already followed.
func chainFindsMethods(info *types.Info, decl ast.Node, start ast.Expr, seen map[types.Object]bool) bool {
	path := pathTo(decl, start)
	at, end, finds := chainEnd(info, path, start)
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
			found = chainFindsMethods(info, decl, id, seen)
		}
		return !found
	})
	return found
}

// chainEnd follows the chain of reflect method calls start heads along path, the
// nodes from the enclosing declaration down to start: it returns the chain's last
// expression and its index in path, or finds true where a call of it is a method
// finder.
func chainEnd(info *types.Info, path []ast.Node, start ast.Expr) (at ast.Expr, end int, finds bool) {
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
		if methodFinders[sel.Sel.Name] {
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
				narrowReflection(fn, callSite{info: info, decl: decl, call: call}, &d)
				d.named = f.templateNames(info, file, fn, call)
				failed = f.arguments(info, enclosing, calledSignature(info, fn, call), call.Args, &d)
				return failed == nil
			}
			failed = f.crossingArguments(info, enclosing, call)
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
func (f *encodingFlow) crossingArguments(info *types.Info, enclosing *types.Func, call *ast.CallExpr) error {
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
		if err := f.crossingArgument(info, enclosing, call, i); err != nil {
			return err
		}
	}
	return nil
}

// crossingArgument records what the argument at position i of one call carries out
// of the analysed program: its own struct value, the types it reaches of its own
// ([encodingFlow.ownTypes]), each struct value an interface-typed local or call result
// holds ([storedTypes.held]), where an empty-interface parameter's values cross at its
// callers' sites, and what a container of interface values it names holds
// ([storedTypes.argument]).
func (f *encodingFlow) crossingArgument(info *types.Info, enclosing *types.Func, call *ast.CallExpr, i int) error {
	out, sinks := f.edge.sinkOf(info, call, i)
	if !sinks {
		return nil
	}
	arg := call.Args[i]
	d := destination{detail: "passed to " + out.callee.FullName(), reach: out.reach}
	if at, supplies := parameterAt(out.callee.Signature(), i); supplies && !f.edge.declares(out.callee) && f.fills(out.callee, at) {
		d = destination{detail: "decoded by " + out.callee.FullName(), reach: out.reach.filling()}
	}
	flowing := f.ownTypes(info, arg)
	if at := info.TypeOf(arg); at != nil && carriesStruct(at) {
		flowing = append(flowing, at)
	}
	for _, held := range slices.Concat(f.stored.held(info, enclosing, arg), f.stored.argument(info, enclosing, arg)) {
		if carriesStruct(held) {
			flowing = append(flowing, held)
		}
	}
	for _, t := range flowing {
		if err := f.retain(t, arg.Pos(), &d); err != nil {
			return err
		}
	}
	return nil
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
// type, at the sites [programConversions] records.
func (f *encodingFlow) convertedTo(iface *types.Named) []types.Type {
	var found []types.Type
	for i := range f.conversions {
		if c := &f.conversions[i]; types.Identical(c.Interface, iface) {
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

// arguments records the types the arguments of one destination call of signature
// sig in the body of enclosing carry. A slice, an array or a map holding interface
// values carries the types the program stores in it ([storedTypes.argument]), and an
// argument for a context parameter carries none ([isContext]).
func (f *encodingFlow) arguments(info *types.Info, enclosing *types.Func, sig *types.Signature, args []ast.Expr, d *destination) error {
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
		if err := f.retain(at, arg.Pos(), d); err != nil {
			return err
		}
		for _, stored := range slices.Concat(f.stored.argument(info, enclosing, arg), f.stored.value(info, enclosing, arg),
			f.ownTypes(info, arg)) {
			if err := f.retain(stored, arg.Pos(), d); err != nil {
				return err
			}
		}
	}
	return nil
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

// retain records what every defined type a destination walking a value of t reads
// keeps.
func (f *encodingFlow) retain(t types.Type, at token.Pos, d *destination) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	// A value of a type parameter's type is a value of each type argument the
	// program instantiates the parameter with.
	for _, one := range append([]types.Type{t}, f.typeArgs.bindings(t)...) {
		reached, encoded := f.encoderReach(one), map[string]bool(nil)
		if d.reach.encoders != 0 {
			reached, encoded = f.selfEncodedReach(one, d.reach.encoders)
		}
		if d.reach.storesOnly() {
			reached = concreteTypes(namedTypesReached(one))
		}
		if d.reach.results {
			reached = f.calledResults(slices.Clone(reached), d)
		}
		for _, named := range reached {
			f.members(named, site, d, encoded[types.TypeString(named, nil)])
		}
	}
	return nil
}

// members records what one destination reads of one defined type: the methods it
// resolves by name, the methods an outside package can name, and, where it reads
// fields and the type does not encode itself, the exported fields and every field
// carrying a tag, because a tagged field is named by its tag and not by its visibility.
func (f *encodingFlow) members(named *types.Named, site evidence, d *destination, encodesItself bool) {
	origin := named.Origin()
	every, satisfying := f.outsideMethods(named, d.reach)
	for m := range origin.Methods() {
		resolved := d.reach.methods.reads(m) && (d.named == nil || d.named[m.Name()] || printingMethods[m.Name()])
		if resolved || (every && m.Exported()) || satisfying[m.Name()] {
			f.kept.record(m, site, d.detail)
		}
	}
	if st, isStruct := origin.Underlying().(*types.Struct); isStruct && !encodesItself {
		f.fields(st, site, d)
	}
}

// fields records the fields of one struct a destination reads, and those of each
// struct type with no name a field it visits holds, whose fields are symbols of the
// type that holds the struct. An exported, a tagged and an embedded struct field are
// visited (https://pkg.go.dev/encoding/json#Marshal).
func (f *encodingFlow) fields(st *types.Struct, site evidence, d *destination) {
	for i := range st.NumFields() {
		field := st.Field(i)
		visited := (field.Exported() || st.Tag(i) != "" || embedsStruct(field)) && !d.reach.keys.skips(st.Tag(i))
		switch {
		case f.reads(field, visited, d):
			f.kept.record(field, site, d.detail)
		case d.reach.fills && visited:
			f.filled.record(field, site, d.detail)
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

// reads reports whether one destination reads one field: a field it visits where it
// reads fields and, for a template engine, a template names, an XML decoder's XMLName,
// or a written holder a decoder fills through.
func (f *encodingFlow) reads(field *types.Var, visited bool, d *destination) bool {
	switch {
	case d.reach.fields && visited && (d.named == nil || d.named[field.Name()] || field.Embedded()):
		return true
	case d.reach.xmlName && field.Name() == xmlNameField && isXMLName(field.Type()):
		return true
	default:
		return d.reach.holders && visited && holdsValue(field.Type()) && f.written()[f.edge.sites.of(field)]
	}
}

// embedsStruct reports whether one field is an embedded struct or pointer to one,
// whose fields an encoder promotes whether or not the embedded type is exported.
func embedsStruct(field *types.Var) bool {
	return field.Embedded() && isStructValue(field.Type())
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
		return destination{detail: detail, reach: reach{methods: resolved, fields: true, results: templatePackages[pkg.Path()] && templateExecutions[fn.Name()]}}, true
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
var templatePackages = map[string]bool{"text/template": true, htmlTemplate: true}

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

// outsidePackages is [outsideClosure] over the flow's input.
func (f *encodingFlow) outsidePackages() []*types.Package {
	return outsideClosure(f.kept.in)
}

// outsideClosure is every package the program's import closure holds that is
// neither the target's nor a loaded consumer's, ordered by path, computed once per
// input.
func outsideClosure(in *Input) []*types.Package {
	if in.outside != nil {
		return *in.outside
	}
	own := make(map[string]bool)
	var queue []*types.Package
	for _, p := range programPackages(in.Result) {
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
	in.outside = &found
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
	if held, known := f.reached[t]; known {
		return held
	}
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
	if f.reached == nil {
		f.reached = make(map[types.Type][]*types.Named)
	}
	f.reached[t] = found
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
// reasons about at most once per site;
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
	site evidence
}

// newRetention prepares the accumulation of one class over one input.
func newRetention(in *Input, class Class) *retention {
	return &retention{in: in, class: class, seen: make(map[exemptionKey]bool)}
}

// record keeps one exemption, unless the symbol is not one the run reasons about
// or this class already recorded the symbol at this site.
func (r *retention) record(obj types.Object, site evidence, detail string) {
	id, held := r.in.Resolve.Object(obj)
	if !held {
		return
	}
	key := exemptionKey{id: id, site: site}
	if r.seen[key] {
		return
	}
	r.seen[key] = true
	r.found = append(r.found, graph.Exemption{
		ID:       id,
		Class:    string(r.class),
		Consumer: site.consumer,
		Site:     site.at,
		Detail:   detail,
	})
}

// evidence is where one piece of evidence is written: a position of the target, or
// of the loaded consumer consumer names, relative to that consumer's root.
type evidence struct {
	consumer string
	at       token.Position
}

// site renders one position. Every position the load compiled is a file of the
// target or of a loaded consumer, so one the resolver cannot render is a failure of
// the run rather than a site to pass over.
func (r *retention) site(pos token.Pos) (evidence, error) {
	consumer, at, err := r.in.Resolve.Site(pos)
	return evidence{consumer: consumer, at: at}, err
}

// exemptions returns what the class recorded, ordered by site and then by symbol.
func (r *retention) exemptions() []graph.Exemption {
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

// written is every field the program writes, keyed by where it is declared: a field
// a composite literal sets, keyed or positional, and a field an assignment stores
// into.
func (f *encodingFlow) written() map[token.Position]bool {
	if f.writes != nil {
		return f.writes
	}
	f.writes = make(map[token.Position]bool)
	for _, p := range programPackages(f.kept.in.Result) {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			for _, field := range fieldsWritten(p.TypesInfo, file) {
				f.writes[f.edge.sites.of(field)] = true
			}
		}
	}
	return f.writes
}

// fieldsWritten is every field one file writes: by a composite literal, keyed or
// positional, and by an assignment.
func fieldsWritten(info *types.Info, file *ast.File) []*types.Var {
	var found []*types.Var
	selected := func(expr ast.Expr) {
		if sel, isSelector := ast.Unparen(expr).(*ast.SelectorExpr); isSelector {
			found = append(found, selectedField(info, sel))
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if st, isStruct := types.Unalias(info.TypeOf(n)).Underlying().(*types.Struct); isStruct {
				for i, elt := range n.Elts {
					found = append(found, literalField(info, st, elt, i))
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				selected(lhs)
			}
		}
		return true
	})
	return slices.DeleteFunc(found, func(field *types.Var) bool { return field == nil })
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

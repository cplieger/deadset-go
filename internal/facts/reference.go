// Package facts is what one walk of a loaded configuration's syntax records: the
// references with the rule behind each, the conversions to interface types, and
// the walk order every later pass reads the configuration in.
package facts

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"golang.org/x/tools/go/packages"
)

// RefKind is how a reference uses its target. String is the spelling a report
// and a message carry.
type RefKind uint8

// The ways one declaration uses another.
const (
	RefRead       RefKind = iota // every use no other kind names; an operand of & is a read
	RefWrite                     // a store into the target: the left side of =, the operand of ++, -- or an op=, a field a composite literal keys, a collection an index, a key or delete stores into, and both sides of the append-back idiom
	RefCall                      // the callee of a call
	RefTypeUse                   // a type in a type position
	RefConversion                // T(x)
	RefEmbed                     // an embedded field or an embedded interface
	RefAssert                    // x.(T), or a type in a type-switch case
)

// via is the rule that made a reference: the identifier at its site, or one of the
// reads analysis.md synthesizes where no identifier names the declaration. String
// is the spelling a message carries.
type via uint8

// The rules a reference is made by.
const (
	viaIdentifier            via = iota // an identifier names the declaration
	viaImport                           // an import names the package
	viaPromotedPath                     // an embedded field a selection reaches through
	viaComparison                       // a field equality, a switch or a map key compares
	viaContainerKey                     // a field a standard container compares as a key
	viaStructIdentity                   // a field a conversion between struct types requires
	viaLayout                           // a field a memory-layout use lays out
	viaConversionRequirement            // a method or embedded field an interface conversion requires
	viaLiteralTwin                      // the field of an identical struct type literal
	viaCgoRecovery                      // a member selected on a value of C converted to a Go type
	viaConstantRepetition               // a constant specification repeating the expression list before it
)

var viaNames = [...]string{
	viaIdentifier:            "identifier",
	viaImport:                "import",
	viaPromotedPath:          "promoted-path",
	viaComparison:            "comparison",
	viaContainerKey:          "container-key",
	viaStructIdentity:        "struct-identity",
	viaLayout:                "layout",
	viaConversionRequirement: "conversion-requirement",
	viaLiteralTwin:           "literal-twin",
	viaCgoRecovery:           "cgo-recovery",
	viaConstantRepetition:    "constant-repetition",
}

// Synthesized reports whether the rule is one no identifier at the site spells.
func (v via) Synthesized() bool { return v != viaIdentifier }

// String returns the rule's spelling, and a numbered form for a value outside the
// set so a message never loses the number it was given.
func (v via) String() string {
	if int(v) >= len(viaNames) {
		return "via(" + strconv.Itoa(int(v)) + ")"
	}
	return viaNames[v]
}

// The language's own functions whose call stores into the value they are given.
const (
	appendBuiltin = "append"
	deleteBuiltin = "delete"
)

var refKindNames = [...]string{
	RefRead:       "read",
	RefWrite:      "write",
	RefCall:       "call",
	RefTypeUse:    "type-use",
	RefConversion: "conversion",
	RefEmbed:      "embed",
	RefAssert:     "assert",
}

// String returns the kind's spelling, and a numbered form for a value outside
// the set so a message never loses the number it was given.
func (k RefKind) String() string {
	if int(k) >= len(refKindNames) {
		return "RefKind(" + strconv.Itoa(int(k)) + ")"
	}
	return refKindNames[k]
}

// Reference is one use of a target symbol, by a declaration of the target or by a
// loaded consumer.
type Reference struct {
	// From is the declaration of the target that encloses the referencing
	// identifier; a consumer's reference has none, its declarations are not
	// enumerated. An import's From is its file's own symbol: it serves every
	// declaration of the file at once, and the file and package kinds read it.
	From inventory.SymbolID
	To   inventory.SymbolID

	// Consumer is the module path of the loaded consumer that made the reference,
	// and is empty for a reference the target made. A consumer's reference is an
	// actual caller of the target from outside it, which is what the sweep reads
	// it as.
	Consumer string

	// Span is, on a write the target makes as the one target of an assignment, an
	// increment, a keyed literal element or a delete statement, the statement or
	// element deleting the write deletes. It is zero on every other reference.
	Span inventory.SourceRange

	// Site is where the referencing identifier is written, which a Span holds or
	// does not.
	Site inventory.Offset

	// Carriers are the declarations that deleting with their writes deletes the
	// referencing identifier, innermost first: From where the identifier is in
	// From's own declared type, then each declaration a write stores into whose
	// statement or element (the write's Span) holds the identifier. It is empty
	// elsewhere and on a consumer's reference.
	Carriers []inventory.SymbolID

	// Pos is the referencing identifier's rendered position, relative to the root
	// of the module that made the reference, with forward slashes and a Column
	// counting UTF-16 code units. A consumer's reference is therefore read
	// together with Consumer, which names the module the path is relative to.
	Pos token.Position

	// Configs is the set of the matrix's build configurations the reference was
	// seen in. One walk fills none of it, because a walk is of one configuration;
	// the matrix merge sets each configuration's own, and the graph of the matrix
	// holds one reference several configurations make once, with their union.
	Configs inventory.ConfigSet
	Kind    RefKind
	Via     via
	Test    bool // the referencing file is a test file
}

// Selects reports whether a reference of this kind reaches a method through the
// value it is written on: a call, and a selection that takes the method as a value.
// A selection through an interface value names the method the interface declares
// rather than the method of the concrete type behind it.
func (k RefKind) Selects() bool { return k == RefCall || k == RefRead }

// Mode is the run's reference mode: which references a sweep counts, and how a
// reference from a loaded consumer's test file is classified. The composition root
// decides it once per run and hands it to every stage that reads it, so no two
// stages of one run answer about different reference sets.
type Mode struct {
	// Production drops the references test files made and leaves the test roots
	// out of the reachability seed; a test root itself stays live. A mark or an
	// exemption still seeds a closure over every reference, but the symbol a
	// retained test declaration names directly counts production references alone.
	Production bool

	// ConsumerTestsProduction classifies a reference from a loaded consumer's test
	// file as production rather than test. It is a classification, not a filter: it
	// decides the split a candidate reports as well as what a production sweep counts.
	ConsumerTestsProduction bool
}

// TestReference reports whether the mode classifies one reference as a test file's,
// which a loaded consumer's test reference is not where the mode counts it as
// production.
func (m Mode) TestReference(r *Reference) bool {
	return r.Test && (r.Consumer == "" || !m.ConsumerTestsProduction)
}

// Uncounted reports whether the mode counts one reference as no use at all, which a
// production mode does for every test reference.
func (m Mode) Uncounted(r *Reference) bool {
	return m.Production && m.TestReference(r)
}

// CountsConsumerTests reports whether the mode counts a loaded consumer's test-file
// reference as a call from outside the target.
func (m Mode) CountsConsumerTests() bool {
	return !m.Production || m.ConsumerTestsProduction
}

// References walks each source file of one configuration once ([inventory.Sources]),
// the target's and every declared consumer's, and returns every reference to a
// declaration of symbols in a deterministic order, plus the test-file rules applied.
// A reference a test file or a package tests classifies as test support
// carries Test; one to a declaration of another module or of the language is none.
// A target file the resolver cannot render ends the walk with [inventory.ErrSource];
// a consumer's files render against that consumer's root.
func References(r *load.Result, rs *resolve.Resolver, symbols []inventory.Symbol, tests roots.TestClasses) ([]Reference, []inventory.TestFileRule, error) {
	p := newReferencePass(r, rs, symbols, tests)
	if err := p.walk(r.Packages); err != nil {
		return nil, nil, err
	}
	for i, consumer := range r.Referencing() {
		if err := p.walkConsumer(i, &consumer); err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(p.refs, byUse)
	rules := []inventory.TestFileRule{{Rule: inventory.GoTestSuffixRule, Matched: p.testFiles}}
	if p.supportFile > 0 {
		rules = append(rules, inventory.TestFileRule{Rule: roots.TestSupportRule, Matched: p.supportFile})
	}
	return p.refs, rules, nil
}

// byUse orders two references by the module that made them, the target's own
// first, then by position, the symbol referenced, the declaration referencing it,
// the kind and the rule: a total order, because one identifier never references one
// symbol twice from one declaration, and a position is unique within one module.
//
//nolint:gocritic // slices.SortFunc fixes a comparator's parameters to values.
func byUse(a, b Reference) int {
	if c := strings.Compare(a.Consumer, b.Consumer); c != 0 {
		return c
	}
	if c := inventory.ByPosition(a.Pos, b.Pos); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.To), string(b.To)); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.From), string(b.From)); c != 0 {
		return c
	}
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Via, b.Via))
}

// referencePass accumulates the references of one loaded configuration.
type referencePass struct {
	resolve     *resolve.Resolver
	at          *inventory.Positions // renders the file being walked, the resolver's while the target is walked
	info        *types.Info          // the type information of the variant that compiles the file being walked
	tests       roots.TestClasses
	packages    map[load.PackageID]inventory.SymbolID // the inventory's package symbol per package, which an import resolves to
	pkgKeys     map[*types.Package]load.PackageID
	kinds       map[token.Pos]RefKind  // the kind a parent node fixes for an identifier below it
	spans       map[token.Pos]ast.Node // the statement or element a write's deletion deletes, by the written identifier
	callees     map[token.Pos]bool     // the identifiers a call expression names as its callee
	idOf        func(*packages.Package) load.PackageID
	instances   *instantiations           // the type arguments a layout read lays a type parameter out as
	literals    literalStructs            // the struct type literals walked, by identity
	consumer    string                    // the module path of the consumer being walked, empty while the target is
	file        inventory.SymbolID        // the symbol of the file being walked, empty outside the inventory
	syntax      *ast.File                 // the file being walked
	converted   map[*types.Var]types.Type // per file, the type a typeless variable's conversion initializer names
	annotated   inventory.SymbolID        // the declaration whose declared type is being walked, empty elsewhere
	err         error
	refs        []Reference
	testFiles   int
	supportFile int  // the files of the target's test-support packages
	test        bool // the file being walked is test code
}

// newReferencePass prepares one configuration's walk.
func newReferencePass(r *load.Result, rs *resolve.Resolver, symbols []inventory.Symbol, tests roots.TestClasses) *referencePass {
	p := &referencePass{
		resolve:   rs,
		at:        rs.Positions(),
		tests:     tests,
		packages:  make(map[load.PackageID]inventory.SymbolID),
		kinds:     make(map[token.Pos]RefKind),
		spans:     make(map[token.Pos]ast.Node),
		callees:   make(map[token.Pos]bool),
		idOf:      r.ID,
		pkgKeys:   r.TypesIDs(),
		instances: instantiationsOf(r.Packages),
	}
	for i := range symbols {
		if s := &symbols[i]; s.Kind == inventory.KindPackage {
			p.packages[s.Package] = s.ID
		}
	}
	return p
}

// walkConsumer walks every package of one loaded consumer, recording the
// references it makes to the target's declarations, each with no From. Its files
// render against the consumer's own root, the module directory the scope named,
// through the resolver's renderer, so a consumer's file renders one way in every
// pass wherever it is.
func (p *referencePass) walkConsumer(at int, consumer *load.Consumer) error {
	p.consumer = consumer.ID
	p.at = p.resolve.Consumer(at)
	defer func() {
		p.consumer = ""
		p.at = p.resolve.Positions()
	}()
	return p.walk(consumer.Packages)
}

// walk visits every file of every variant of one module, in one order, and
// returns the first failure the walk met.
func (p *referencePass) walk(pkgs []*packages.Package) error {
	p.literals.add(pkgs, p.at.Fset)
	for pkg, f := range inventory.Sources(pkgs, p.at.Fset) {
		if err := p.walkFile(pkg, f); err != nil {
			return err
		}
	}
	return nil
}

// walkFile walks one source file under the variant that compiles it first.
func (p *referencePass) walkFile(pkg *packages.Package, f *ast.File) error {
	position, err := p.at.Render(f.FileStart)
	if err != nil {
		return err
	}

	p.info = pkg.TypesInfo
	p.syntax, p.converted = f, nil
	// The file's own symbol, which its imports reference from. A consumer's file
	// is not in the inventory and does not render against the target's root, so
	// it is not resolved at all rather than resolved and failing.
	p.file = ""
	if p.consumer == "" {
		p.file, _ = p.resolve.At(f.FileStart)
	}
	_, p.test = inventory.IsTestFile(position.Filename)
	switch {
	case p.test:
		p.testFiles++
	case p.consumer == "" && p.tests.Support[p.idOf(pkg)]:
		p.test = true
		p.supportFile++
	}

	first := len(p.refs)
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			p.walkFunc(d)
		case *ast.GenDecl:
			p.walkGenDecl(d)
		}
		p.readConversions(d)
		if p.err != nil {
			return p.err
		}
	}
	carryWrites(p.refs[first:])
	return nil
}

// carryWrites appends to the Carriers of every reference of one file the
// declaration each write stores into whose Span holds the reference, innermost
// first. Spans nest or are disjoint, so one pass over the sites in order, with the
// spans holding the current site open, finds them all.
func carryWrites(refs []Reference) {
	writes := writesBySpan(refs)
	if len(writes) == 0 {
		return
	}
	var open []int
	next := 0
	for _, i := range bySite(refs) {
		r := &refs[i]
		for ; next < len(writes); next++ {
			span := refs[writes[next]].Span
			if c := strings.Compare(span.File, r.Site.File); c > 0 || (c == 0 && span.From > r.Site.At) {
				break
			}
			open = append(open, writes[next])
		}
		open = slices.DeleteFunc(open, func(w int) bool { return !refs[w].Span.Holds(r.Site) })
		for _, w := range slices.Backward(open) {
			if to := refs[w].To; to != r.To && !slices.Contains(r.Carriers, to) {
				r.Carriers = append(r.Carriers, to)
			}
		}
	}
}

// writesBySpan is the place of every write with a span, outermost first: by file,
// then by where the span starts, then the longer first.
func writesBySpan(refs []Reference) []int {
	var writes []int
	for i := range refs {
		if refs[i].Span != (inventory.SourceRange{}) {
			writes = append(writes, i)
		}
	}
	slices.SortFunc(writes, func(a, b int) int {
		x, y := refs[a].Span, refs[b].Span
		return cmp.Or(strings.Compare(x.File, y.File), cmp.Compare(x.From, y.From), cmp.Compare(y.To, x.To))
	})
	return writes
}

// bySite is the place of every reference, in the order of the sites they are
// written at.
func bySite(refs []Reference) []int {
	sites := make([]int, len(refs))
	for i := range sites {
		sites[i] = i
	}
	slices.SortStableFunc(sites, func(a, b int) int {
		x, y := refs[a].Site, refs[b].Site
		return cmp.Or(strings.Compare(x.File, y.File), cmp.Compare(x.At, y.At))
	})
	return sites
}

// inspectAnnotation walks the declared type of one declaration, whose references
// the declaration carries.
func (p *referencePass) inspectAnnotation(node ast.Node, id inventory.SymbolID, except ast.Node) {
	p.annotated = id
	p.inspectExcept(node, id, except)
	p.annotated = ""
}

// walkFunc walks one function or method. Its receiver, its signature and its
// body reference from the function itself, while a type parameter's constraint
// references from that parameter, because deleting the parameter deletes the
// constraint with it.
func (p *referencePass) walkFunc(d *ast.FuncDecl) {
	id, ok := p.owner(d.Name.Pos())
	if !ok {
		return
	}
	if list := d.Type.TypeParams; list != nil {
		for _, f := range list.List {
			p.walkTypeParams(f)
		}
	}
	if d.Recv != nil {
		p.inspect(d.Recv, id)
	}
	p.inspect(d.Type.Params, id)
	if d.Type.Results != nil {
		p.inspect(d.Type.Results, id)
	}
	if d.Body != nil {
		p.inspect(d.Body, id)
	}
}

// walkTypeParams walks one type-parameter group, whose constraint references
// from every parameter the group declares.
func (p *referencePass) walkTypeParams(f *ast.Field) {
	for _, n := range f.Names {
		id, ok := p.owner(n.Pos())
		if !ok {
			return
		}
		p.inspect(f.Type, id)
	}
}

// walkGenDecl walks one declaration group.
func (p *referencePass) walkGenDecl(d *ast.GenDecl) {
	var repeated *ast.ValueSpec
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ImportSpec:
			p.walkImport(s)
		case *ast.TypeSpec:
			p.walkTypeSpec(s)
		case *ast.ValueSpec:
			p.walkValueSpec(s)
			if d.Tok != token.CONST {
				continue
			}
			if len(s.Values) > 0 {
				repeated = s
			} else if repeated != nil {
				p.walkRepeated(s, repeated)
			}
		}
	}
}

// walkRepeated records what a constant specification that writes no expression
// list references through the list it repeats, and that list's type: each
// declaration they name, from each constant the specification declares, at its
// first name (Go specification, Constant declarations: an omitted list is the
// textual substitution of the first preceding one).
func (p *referencePass) walkRepeated(s, repeated *ast.ValueSpec) {
	ids := make([]inventory.SymbolID, 0, len(s.Names))
	for _, n := range s.Names {
		if id, ok := p.owner(n.Pos()); ok {
			ids = append(ids, id)
		}
	}
	nodes := slices.Clone(repeated.Values)
	if repeated.Type != nil {
		nodes = append(nodes, repeated.Type)
	}
	for _, node := range nodes {
		ast.Inspect(node, func(n ast.Node) bool {
			ident, isIdent := n.(*ast.Ident)
			if isIdent {
				p.repeatedUse(ident, ids, s.Names[0].Pos())
			}
			return !isIdent
		})
	}
}

// repeatedUse records the declaration one identifier of a repeated list names, from
// each constant of ids, at pos.
func (p *referencePass) repeatedUse(ident *ast.Ident, ids []inventory.SymbolID, pos token.Pos) {
	obj := p.info.Uses[ident]
	if obj == nil {
		return
	}
	if to, held := p.resolve.Object(obj); held {
		for _, id := range ids {
			p.add(id, to, pos, p.kindOf(ident, obj), viaConstantRepetition)
		}
	}
}

// walkImport records the reference one import declaration makes: a read of the
// imported package's own symbol, at the import spec, from the file (an import
// serves every declaration of it). The identifier walk records nothing here, the
// import resolving to a package name no inventory symbol is written at. Nothing is
// recorded for a package the inventory holds no symbol for, every dependency, nor
// for a consumer's import.
func (p *referencePass) walkImport(spec *ast.ImportSpec) {
	if p.file == "" {
		return
	}
	name := p.info.PkgNameOf(spec)
	if name == nil || name.Imported() == nil {
		return
	}
	to, held := p.packages[load.TypesID(p.pkgKeys, name.Imported())]
	if !held {
		return
	}
	p.add(p.file, to, spec.Pos(), RefRead, viaImport)
}

// walkTypeSpec walks one type declaration. A type's own type parameters belong
// to the type rather than being subjects of their own, so their constraints
// reference from the type.
func (p *referencePass) walkTypeSpec(s *ast.TypeSpec) {
	id, ok := p.owner(s.Name.Pos())
	if !ok {
		return
	}
	if s.TypeParams != nil {
		p.inspect(s.TypeParams, id)
	}
	switch t := s.Type.(type) {
	case *ast.StructType:
		p.walkStruct(t, id)
	case *ast.InterfaceType:
		p.walkInterface(t, id)
	default:
		p.inspect(s.Type, id)
	}
}

// walkValueSpec walks one constant or variable group. A name's type and the
// value that initializes it reference from that name; where one initializer list
// feeds several names, no name owns a value of its own and the group's first
// name carries the list.
func (p *referencePass) walkValueSpec(s *ast.ValueSpec) {
	ids := make([]inventory.SymbolID, 0, len(s.Names))
	for _, n := range s.Names {
		id, ok := p.owner(n.Pos())
		if !ok {
			return
		}
		ids = append(ids, id)
		if s.Type != nil {
			p.inspectAnnotation(s.Type, id, nil)
		}
	}
	switch {
	case len(ids) == len(s.Values):
		for i, v := range s.Values {
			p.inspect(v, ids[i])
		}
	case len(ids) > 0:
		for _, v := range s.Values {
			p.inspect(v, ids[0])
		}
	}
}

// walkStruct walks the fields of one struct type. Every field is a symbol of its
// own, so nothing in a struct body references from the type that declares it but
// the layout reads a host-layout field records from owner.
func (p *referencePass) walkStruct(t *ast.StructType, owner inventory.SymbolID) {
	p.readHostLayout(t, owner)
	for _, f := range t.Fields.List {
		if len(f.Names) == 0 {
			p.walkEmbedded(f)
			continue
		}
		p.walkField(f)
	}
}

// walkEmbedded walks one embedded field, whose type references from the field
// the language names after that type.
func (p *referencePass) walkEmbedded(f *ast.Field) {
	named := inventory.EmbeddedName(f.Type)
	if named == nil {
		return
	}
	id, ok := p.owner(named.Pos())
	if !ok {
		return
	}
	p.kinds[named.Pos()] = RefEmbed
	p.inspect(f.Type, id)
}

// walkField walks one named field group. A field's type references from that
// field, and an anonymous struct the type carries declares the field's own
// members, whose container is the group's first name.
func (p *referencePass) walkField(f *ast.Field) {
	inner := inventory.AnonymousStruct(f.Type)
	members := inner
	for _, n := range f.Names {
		id, ok := p.owner(n.Pos())
		if !ok {
			return
		}
		p.inspectAnnotation(f.Type, id, inner)
		if members == nil {
			continue
		}
		p.walkStruct(members, id)
		members = nil
	}
}

// walkInterface walks one interface type. A declared method references from
// itself; an embedded interface and a type-set element declare nothing, so they
// reference from the interface.
func (p *referencePass) walkInterface(t *ast.InterfaceType, owner inventory.SymbolID) {
	for _, f := range t.Methods.List {
		if len(f.Names) == 0 {
			if named := inventory.EmbeddedName(f.Type); named != nil {
				p.kinds[named.Pos()] = RefEmbed
			}
			p.inspect(f.Type, owner)
			continue
		}
		for _, n := range f.Names {
			id, ok := p.owner(n.Pos())
			if !ok {
				return
			}
			p.inspect(f.Type, id)
		}
	}
}

// inspect records every reference one subtree makes from encl.
func (p *referencePass) inspect(node ast.Node, encl inventory.SymbolID) {
	p.inspectExcept(node, encl, nil)
}

// inspectExcept records every reference one subtree makes from encl, visiting
// nothing at or below skip, which is how a field's anonymous struct is left to
// its own members.
func (p *referencePass) inspectExcept(node ast.Node, encl inventory.SymbolID, skip ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil || n == skip || p.err != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.Ident:
			p.record(n, encl)
			p.readComparableArguments(n, encl)
			return false
		case *ast.SelectorExpr:
			p.reachThrough(n, encl)
			p.recoverSelection(n, encl)
		case *ast.AssignStmt:
			p.markAssign(n)
		case *ast.CompositeLit:
			p.markFieldKeys(n)
		case *ast.IncDecStmt, *ast.ExprStmt:
			p.markStatement(n)
		case *ast.RangeStmt:
			p.markRange(n)
		case *ast.TypeAssertExpr:
			p.markAssert(n.Type)
		case *ast.TypeSwitchStmt:
			p.markCaseAsserts(n)
		case *ast.CallExpr:
			p.markCallee(n.Fun)
			p.markDelete(n, nil)
			p.readUnsafeConversion(n, encl)
			p.readStructConversion(n, encl)
			p.readContainerKeys(n, encl)
		case *ast.BinaryExpr, *ast.SwitchStmt, *ast.MapType, *ast.IndexExpr:
			p.readComparedFields(n, encl)
		}
		return true
	})
}

// markAssign records the stores one assignment performs: every target it names,
// and the collection the append-back idiom reads only to store into.
// The statement is the write's span only where it has one target.
func (p *referencePass) markAssign(s *ast.AssignStmt) {
	var span ast.Node
	if len(s.Lhs) == 1 {
		span = s
	}
	for _, target := range s.Lhs {
		p.markWrite(target, span)
	}
	p.markAppendBack(s)
}

// markStatement records the store an increment or a delete statement performs,
// which deleting the statement deletes.
func (p *referencePass) markStatement(s ast.Node) {
	switch s := s.(type) {
	case *ast.IncDecStmt:
		p.markWrite(s.X, s)
	case *ast.ExprStmt:
		if call, isCall := s.X.(*ast.CallExpr); isCall {
			p.markDelete(call, s)
		}
	}
}

// markRange records the stores one range statement performs, which are its key and
// its value where the statement assigns to variables declared elsewhere. A range
// that declares them stores into nothing the inventory holds.
func (p *referencePass) markRange(s *ast.RangeStmt) {
	if s.Tok != token.ASSIGN {
		return
	}
	p.markWrite(s.Key, nil)
	p.markWrite(s.Value, nil)
}

// readComparedFields records the fields the comparison one node writes reads. The
// four nodes are the four shapes that compare a value of struct type: equality,
// the tag and the cases of an expression switch, the key type of a map type, and
// the key of an index into a map.
func (p *referencePass) readComparedFields(n ast.Node, encl inventory.SymbolID) {
	switch n := n.(type) {
	case *ast.BinaryExpr:
		// Only equality compares a struct: an ordered comparison is not defined
		// over one, so a value of struct type is never an operand of it.
		if n.Op == token.EQL || n.Op == token.NEQ {
			p.readComparison(encl, n.OpPos, viaComparison, n.X, n.Y)
		}
	case *ast.SwitchStmt:
		p.readSwitch(n, encl)
	case *ast.MapType:
		p.readComparison(encl, n.Key.Pos(), viaComparison, n.Key)
	case *ast.IndexExpr:
		p.readMapKey(n, encl)
	}
}

// readSwitch records the fields one expression switch reads. The tag is compared
// against every case expression, so each of them is a comparison site of its own
// and each is recorded where it is written.
//
// A switch with no tag compares each case against true and names no struct value,
// and a type switch names types rather than values, so neither reaches here: the
// language's type switch is its own node.
func (p *referencePass) readSwitch(s *ast.SwitchStmt, encl inventory.SymbolID) {
	if s.Tag == nil {
		return
	}
	p.readComparison(encl, s.Tag.Pos(), viaComparison, s.Tag)
	for _, stmt := range s.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			p.readComparison(encl, expr.Pos(), viaComparison, expr)
		}
	}
}

// readMapKey records the fields one index into a map reads. A key is compared
// against the keys the map holds, so an index is a comparison site the way an
// operand of equality is. An index into a slice or an array compares nothing, and
// an instantiation of a generic declaration is written the same way and is not an
// index at all, so the map is what decides.
func (p *referencePass) readMapKey(e *ast.IndexExpr, encl inventory.SymbolID) {
	held := p.info.TypeOf(e.X)
	if held == nil {
		return
	}
	if _, isMap := types.Unalias(held).Underlying().(*types.Map); !isMap {
		return
	}
	p.readComparison(encl, e.Index.Pos(), viaComparison, e.Index)
}

// readComparison records a read of every field the comparison at pos reads, over
// the types its operands hold.
//
// The two operands of one comparison usually hold one type, and an interface
// compared with a struct value holds two, so the reads are deduplicated per site:
// one read per field whichever operand named the type, which is what keeps the
// reference order a total one.
func (p *referencePass) readComparison(encl inventory.SymbolID, pos token.Pos, rule via, operands ...ast.Expr) {
	var held map[inventory.SymbolID]bool
	for _, operand := range operands {
		if operand == nil {
			continue
		}
		st, isStruct := comparedStruct(p.info.TypeOf(operand))
		if !isStruct {
			continue
		}
		if held == nil {
			held = make(map[inventory.SymbolID]bool)
		}
		p.readFields(st, held, encl, pos, rule)
	}
}

// readComparableArguments records the fields an instantiation at one identifier
// reads: a type argument whose type parameter's constraint is comparable, or embeds
// it, is compared by the generic code as an operand of == is. reflect.DeepEqual
// takes no type parameter, so it is no such comparison.
func (p *referencePass) readComparableArguments(id *ast.Ident, encl inventory.SymbolID) {
	instance, held := p.info.Instances[id]
	if !held || instance.TypeArgs == nil {
		return
	}
	params := typeParamsOf(p.info.Uses[id])
	if params == nil || params.Len() != instance.TypeArgs.Len() {
		return
	}
	var read map[inventory.SymbolID]bool
	for i := range params.Len() {
		if !embedsComparable(params.At(i).Constraint()) {
			continue
		}
		st, isStruct := comparedStruct(instance.TypeArgs.At(i))
		if !isStruct {
			continue
		}
		if read == nil {
			read = make(map[inventory.SymbolID]bool)
		}
		p.readFields(st, read, encl, id.Pos(), viaComparison)
	}
}

// embedsComparable reports whether a constraint is the predeclared comparable or
// an interface embedding it at any depth.
func embedsComparable(constraint types.Type) bool {
	if types.Identical(constraint, types.Universe.Lookup("comparable").Type()) {
		return true
	}
	iface, ok := types.Unalias(constraint).Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for embedded := range iface.EmbeddedTypes() {
		if embedsComparable(embedded) {
			return true
		}
	}
	return false
}

// readFields records one read at pos of every field of one struct and of every
// field those reach by value: equality (a map key, a switch tag and case included)
// compares every field. It follows struct and array fields, compared field by field
// too, and stops at a pointer, compared by address, and an interface, whose dynamic
// type is unknown here. A struct cannot hold itself by value, so the walk ends.
func (p *referencePass) readFields(st *types.Struct, held map[inventory.SymbolID]bool, encl inventory.SymbolID, pos token.Pos, rule via) {
	for field := range st.Fields() {
		if to, ok := p.resolve.Object(field); ok && !held[to] {
			held[to] = true
			p.add(encl, to, pos, RefRead, rule)
		}
		if inner, isStruct := comparedStruct(field.Type()); isStruct {
			p.readFields(inner, held, encl, pos, rule)
		}
	}
}

// comparedStruct returns the struct a compared value holds, through the arrays the
// language compares element by element, and reports whether it holds one.
//
// A pointer is not stepped through, because comparing two pointers compares
// addresses and reads no field of either value. Nothing else is stepped through
// either: a slice, a map and a function are not comparable at all, and what an
// interface comparison reads is a dynamic type this pass cannot see.
func comparedStruct(t types.Type) (*types.Struct, bool) {
	if t == nil {
		return nil, false
	}
	held := types.Unalias(t).Underlying()
	for {
		array, isArray := held.(*types.Array)
		if !isArray {
			break
		}
		held = types.Unalias(array.Elem()).Underlying()
	}
	st, isStruct := held.(*types.Struct)
	return st, isStruct
}

// record keeps the reference one identifier makes, unless the identifier names
// no object or names one whose declaration is not a symbol of the target. A field
// of a struct type literal is referenced in every identical literal as well.
func (p *referencePass) record(id *ast.Ident, encl inventory.SymbolID) {
	obj := p.info.Uses[id]
	if obj == nil {
		return
	}
	kind := p.kindOf(id, obj)
	if field, isField := obj.(*types.Var); isField && field.IsField() {
		for _, twin := range p.literals.twins(p.info, field) {
			if to, ok := p.resolve.Object(twin); ok {
				p.add(encl, to, id.Pos(), kind, viaLiteralTwin)
			}
		}
	}
	to, ok := p.resolve.Object(obj)
	if !ok {
		return
	}
	p.add(encl, to, id.Pos(), kind, viaIdentifier)
}

// reachThrough records the embedded fields one selector reaches through. A
// promoted member is selected through every embedded field on the path, and
// those fields carry no identifier at the selection, so the selector names them.
// Writing a promoted member still only reads the fields that reach it.
func (p *referencePass) reachThrough(e *ast.SelectorExpr, encl inventory.SymbolID) {
	sel := p.info.Selections[e]
	if sel == nil {
		return
	}
	p.readPath(sel.Recv(), sel.Index(), encl, e.Sel.Pos(), nil, viaPromotedPath)
}

// readPath records a read at pos of every embedded field one selection index path
// passes through from recv, the last step being the member selected. A field in
// seen is not read twice, and seen gains every field read; a nil seen keeps none.
func (p *referencePass) readPath(recv types.Type, index []int, encl inventory.SymbolID, pos token.Pos, seen map[inventory.SymbolID]bool, rule via) {
	if len(index) < 2 {
		return
	}
	held := recv
	for _, i := range index[:len(index)-1] {
		st, ok := structAt(held)
		if !ok || i >= st.NumFields() {
			return
		}
		field := st.Field(i)
		if to, ok := p.resolve.Object(field); ok && !seen[to] {
			if seen != nil {
				seen[to] = true
			}
			p.add(encl, to, pos, RefRead, rule)
		}
		held = field.Type()
	}
}

// readConversions records what each conversion one declaration writes requires of
// the value it converts, which no identifier at the site names: the embedded
// fields through which the value's type promotes a method the interface requires,
// and, for a value of another interface type, each of that interface's methods the
// destination requires. Deleting any of them breaks the conversion.
func (p *referencePass) readConversions(d ast.Decl) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if id, ok := p.owner(d.Name.Pos()); ok {
			p.readRequired(conversionsIn(p.info, d), func(token.Pos) inventory.SymbolID { return id })
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if s, ok := spec.(*ast.ValueSpec); ok {
				p.readRequiredIn(s)
			}
		}
	}
}

// readRequiredIn records the conversions one constant or variable group writes,
// each from the name whose value holds it, as walkValueSpec attributes the values.
func (p *referencePass) readRequiredIn(s *ast.ValueSpec) {
	ids := make([]inventory.SymbolID, 0, len(s.Names))
	for _, n := range s.Names {
		id, ok := p.owner(n.Pos())
		if !ok {
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	p.readRequired(conversionsIn(p.info, s), func(site token.Pos) inventory.SymbolID {
		if len(ids) == len(s.Values) {
			for i, v := range s.Values {
				if v.Pos() <= site && site < v.End() {
					return ids[i]
				}
			}
		}
		return ids[0]
	})
}

// readRequired records, for every conversion of sites, a read of what it requires
// of the value converted, once per declaration and site.
func (p *referencePass) readRequired(sites []Conversion, from func(token.Pos) inventory.SymbolID) {
	seen := make(map[token.Pos]map[inventory.SymbolID]bool)
	for i := range sites {
		c := &sites[i]
		if !types.Implements(c.From, c.To) {
			continue
		}
		if seen[c.Site] == nil {
			seen[c.Site] = make(map[inventory.SymbolID]bool)
		}
		encl, set := from(c.Site), types.NewMethodSet(c.From)
		for required := range c.To.Methods() {
			sel := set.Lookup(required.Pkg(), required.Name())
			if sel == nil {
				continue
			}
			p.readPath(sel.Recv(), sel.Index(), encl, c.Site, seen[c.Site], viaConversionRequirement)
			// A method the destination declares through the same object is
			// deleted from both at once, which breaks nothing.
			if to, ok := p.resolve.Object(sel.Obj()); ok && types.IsInterface(c.From) && sel.Obj() != required && !seen[c.Site][to] {
				seen[c.Site][to] = true
				p.add(encl, to, c.Site, RefRead, viaConversionRequirement)
			}
		}
	}
}

// recoverSelection records the member one selector names when the type checker
// recorded no type for its operand, which happens only on a value of C, and the
// operand is a conversion to a Go type, or a variable a conversion initialized:
// the conversion still names the Go type, so the member is the one that type
// declares.
func (p *referencePass) recoverSelection(e *ast.SelectorExpr, encl inventory.SymbolID) {
	if p.info.Uses[e.Sel] != nil || p.info.Selections[e] != nil {
		return
	}
	if t := p.info.TypeOf(e.X); t != nil && t != types.Typ[types.Invalid] {
		return
	}
	held := p.convertedType(e.X)
	if held == nil {
		return
	}
	var pkg *types.Package
	if named, ok := types.Unalias(derefed(held)).(*types.Named); ok {
		pkg = named.Obj().Pkg()
	}
	obj, index, _ := types.LookupFieldOrMethod(held, true, pkg, e.Sel.Name)
	if obj == nil {
		return
	}
	p.readPath(held, index, encl, e.Sel.Pos(), nil, viaCgoRecovery)
	if to, ok := p.resolve.Object(obj); ok {
		p.add(encl, to, e.Sel.Pos(), p.kindOf(e.Sel, obj), viaCgoRecovery)
	}
}

// convertedType is the Go type a conversion names for one typeless operand: the
// operand itself converted, or a variable of the walked file whose initializer is
// such a conversion. It is nil for anything else.
func (p *referencePass) convertedType(x ast.Expr) types.Type {
	switch x := ast.Unparen(x).(type) {
	case *ast.CallExpr:
		if tv, ok := p.info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return tv.Type
		}
	case *ast.Ident:
		if v, ok := p.info.Uses[x].(*types.Var); ok {
			return p.conversionsOfFile()[v]
		}
	}
	return nil
}

// conversionsOfFile keys every variable of the walked file that a single
// conversion initializes to the type the conversion names, built on first use.
func (p *referencePass) conversionsOfFile() map[*types.Var]types.Type {
	if p.converted != nil {
		return p.converted
	}
	p.converted = make(map[*types.Var]types.Type)
	ast.Inspect(p.syntax, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				p.holdConversions(definedNames(n.Lhs), n.Rhs)
			}
		case *ast.ValueSpec:
			p.holdConversions(n.Names, n.Values)
		}
		return true
	})
	return p.converted
}

// holdConversions keys each name a single conversion initializes to the type the
// conversion names.
func (p *referencePass) holdConversions(names []*ast.Ident, values []ast.Expr) {
	if len(names) != len(values) {
		return
	}
	for i, name := range names {
		v, ok := p.info.Defs[name].(*types.Var)
		if !ok {
			continue
		}
		if call, isCall := ast.Unparen(values[i]).(*ast.CallExpr); isCall {
			if t := p.convertedType(call); t != nil {
				p.converted[v] = t
			}
		}
	}
}

// definedNames lists the identifiers the left side of a short variable
// declaration spells.
func definedNames(lhs []ast.Expr) []*ast.Ident {
	names := make([]*ast.Ident, 0, len(lhs))
	for _, l := range lhs {
		if id, ok := l.(*ast.Ident); ok {
			names = append(names, id)
		}
	}
	return names
}

// derefed is the type a pointer points to, and t itself for anything else.
func derefed(t types.Type) types.Type {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

// structAt returns the struct a selector's receiver holds at one step of its
// index path, through the pointer the language dereferences for a selection.
func structAt(t types.Type) (*types.Struct, bool) {
	held := types.Unalias(t).Underlying()
	if ptr, ok := held.(*types.Pointer); ok {
		held = types.Unalias(ptr.Elem()).Underlying()
	}
	st, ok := held.(*types.Struct)
	return st, ok
}

// kindOf classifies one reference from the position a parent node fixed for the
// identifier, and otherwise from what the identifier names.
func (p *referencePass) kindOf(id *ast.Ident, obj types.Object) RefKind {
	if kind, ok := p.kinds[id.Pos()]; ok {
		return kind
	}
	_, isType := obj.(*types.TypeName)
	switch {
	case p.callees[id.Pos()] && isType:
		return RefConversion
	case p.callees[id.Pos()]:
		return RefCall
	case isType:
		return RefTypeUse
	default:
		return RefRead
	}
}

// markWrite records the identifier one assignment target writes. A selector
// writes its member, and an index expression writes the collection it stores
// into, because a slice or a map a package only ever stores into holds nothing
// anything reads. An indirection writes through a value it reads: the pointer's
// own value is read to find the pointee, and the pointee is not a declaration.
// markIndexed and markHeldIn own a store through a struct field. A non-nil span is
// what deleting the write the identifier makes deletes; a held-in write has none.
func (p *referencePass) markWrite(target ast.Expr, span ast.Node) {
	switch t := ast.Unparen(target).(type) {
	case *ast.Ident:
		p.write(t, span)
	case *ast.SelectorExpr:
		p.write(t.Sel, span)
		p.markHeldIn(t.X)
	case *ast.IndexExpr:
		p.markIndexed(t.X, span)
	}
}

// write records one identifier as written, with its span where it has one.
func (p *referencePass) write(id *ast.Ident, span ast.Node) {
	p.kinds[id.Pos()] = RefWrite
	if span != nil {
		p.spans[id.Pos()] = span
	}
}

// markIndexed records the collection one index store lands in: a field holding a
// map, a slice or a pointer is read, and any other collection is written.
func (p *referencePass) markIndexed(collection ast.Expr, span ast.Node) {
	if t, ok := ast.Unparen(collection).(*ast.SelectorExpr); ok {
		if field, isField := p.fieldOf(t); isField && holdsByReference(field) {
			return
		}
	}
	p.markWrite(collection, span)
}

// markHeldIn records the storage one selector store lands in beyond the member it
// writes: a field holding a struct, or an element of a field holding an array, is
// written, and a store through anything else reads it.
func (p *referencePass) markHeldIn(holder ast.Expr) {
	switch t := ast.Unparen(holder).(type) {
	case *ast.SelectorExpr:
		if field, ok := p.fieldOf(t); ok && holdsByValue(field) {
			p.kinds[t.Sel.Pos()] = RefWrite
			p.markHeldIn(t.X)
		}
	case *ast.IndexExpr:
		if field, ok := p.fieldOf(ast.Unparen(t.X)); ok && holdsArray(field) {
			p.markHeldIn(t.X)
		}
	}
}

// fieldOf returns the struct field one expression selects, and false for anything
// else.
func (p *referencePass) fieldOf(e ast.Expr) (*types.Var, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	field, ok := p.info.Uses[sel.Sel].(*types.Var)
	return field, ok && field.IsField()
}

// holdsByReference reports whether a field's value refers to storage other values
// share: a map, a slice or a pointer.
func holdsByReference(field *types.Var) bool {
	switch field.Type().Underlying().(type) {
	case *types.Map, *types.Slice, *types.Pointer:
		return true
	default:
		return false
	}
}

// holdsByValue reports whether a field's value is storage of its own, a struct or
// an array.
func holdsByValue(field *types.Var) bool {
	_, isStruct := field.Type().Underlying().(*types.Struct)
	return isStruct || holdsArray(field)
}

// holdsArray reports whether a field holds an array.
func holdsArray(field *types.Var) bool {
	_, isArray := field.Type().Underlying().(*types.Array)
	return isArray
}

// markFieldKeys records the fields one composite literal writes. A keyed field
// of a struct literal is an initialising store, and a field a package only ever
// sets in a literal holds nothing anything reads. A key of a map, an array or a
// slice literal names no field and keeps the kind what it names decides.
func (p *referencePass) markFieldKeys(lit *ast.CompositeLit) {
	held := p.info.Types[lit].Type
	if held == nil {
		return
	}
	if _, ok := structAt(held); !ok {
		return
	}
	for _, element := range lit.Elts {
		keyed, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := ast.Unparen(keyed.Key).(*ast.Ident); ok {
			p.write(key, keyed)
		}
	}
}

// markDelete records the collection one delete stores into. Removing an entry
// changes what the map holds, the same way an assignment through a key does. The
// span is the statement where the delete is one; a deferred delete has none.
func (p *referencePass) markDelete(call *ast.CallExpr, span ast.Node) {
	if len(call.Args) == 0 || !p.builtin(call.Fun, deleteBuiltin) {
		return
	}
	p.markIndexed(call.Args[0], span)
}

// markAppendBack records the store the append-back idiom performs: in
// x = append(x, v...) and c.f = append(c.f, v...) the read inside the call is how
// the store is written, so both sides write it; an append landing anywhere else
// reads its first argument. The sides are matched by the name chain they are
// spelled as, which in one assignment's scope names one collection.
func (p *referencePass) markAppendBack(s *ast.AssignStmt) {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return
	}
	call, ok := ast.Unparen(s.Rhs[0]).(*ast.CallExpr)
	if !ok || len(call.Args) == 0 || !p.builtin(call.Fun, appendBuiltin) {
		return
	}
	target := spelling(s.Lhs[0])
	first := ast.Unparen(call.Args[0])
	if target == "" || spelling(first) != target {
		return
	}
	switch f := first.(type) {
	case *ast.Ident:
		p.kinds[f.Pos()] = RefWrite
	case *ast.SelectorExpr:
		p.kinds[f.Sel.Pos()] = RefWrite
	}
}

// spelling returns the name chain one identifier or one selector over
// identifiers is written as, and the empty string for anything else, so two
// expressions agree only where the source spells them the same.
func spelling(e ast.Expr) string {
	switch t := ast.Unparen(e).(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		if held := spelling(t.X); held != "" {
			return held + "." + t.Sel.Name
		}
	}
	return ""
}

// builtin reports whether one call names the language's own function of that
// name, so a declaration shadowing the name is not mistaken for it.
func (p *referencePass) builtin(fun ast.Expr, name string) bool {
	id, ok := ast.Unparen(fun).(*ast.Ident)
	if !ok {
		return false
	}
	declared, ok := p.info.Uses[id].(*types.Builtin)
	return ok && declared.Name() == name
}

// markAssert records the identifier that names an asserted type. A composite
// type expression names no single type, so the identifiers inside one stay type
// uses.
func (p *referencePass) markAssert(typ ast.Expr) {
	switch t := ast.Unparen(typ).(type) {
	case *ast.Ident:
		p.kinds[t.Pos()] = RefAssert
	case *ast.SelectorExpr:
		p.kinds[t.Sel.Pos()] = RefAssert
	case *ast.StarExpr:
		p.markAssert(t.X)
	}
}

// markCaseAsserts records the types one type switch names in its cases.
func (p *referencePass) markCaseAsserts(s *ast.TypeSwitchStmt) {
	for _, stmt := range s.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, typ := range clause.List {
			p.markAssert(typ)
		}
	}
}

// markCallee records the identifier one call names. Whether the call invokes or
// converts is decided by what that identifier denotes.
func (p *referencePass) markCallee(fun ast.Expr) {
	switch t := ast.Unparen(fun).(type) {
	case *ast.Ident:
		p.callees[t.Pos()] = true
	case *ast.SelectorExpr:
		p.callees[t.Sel.Pos()] = true
	case *ast.IndexExpr:
		p.markCallee(t.X)
	case *ast.IndexListExpr:
		p.markCallee(t.X)
	}
}

// add keeps one reference, rendering the referencing identifier's position against
// the root of the module that made it.
func (p *referencePass) add(from, to inventory.SymbolID, pos token.Pos, kind RefKind, rule via) {
	position, err := p.at.Render(pos)
	if err != nil {
		p.fail(err)
		return
	}
	r := Reference{
		From: from, To: to, Consumer: p.consumer, Pos: position, Site: p.at.Offset(pos),
		Kind: kind, Via: rule, Test: p.test,
	}
	if node := p.spans[pos]; node != nil && kind == RefWrite && p.consumer == "" {
		r.Span = p.at.Range(node)
	}
	if p.annotated != "" && from == p.annotated && p.consumer == "" {
		r.Carriers = []inventory.SymbolID{from}
	}
	p.refs = append(p.refs, r)
}

// owner returns the declaration a subtree references from, and whether the subtree
// is walked at all.
//
// In the target, a declaration the inventory does not hold owns nothing and its
// subtree is left alone, because a reference has to come from somewhere the
// analysis can name. In a consumer, no declaration is in the inventory and every
// subtree is walked with no owner: what a consumer's declaration is called decides
// nothing, and what it references decides everything.
func (p *referencePass) owner(pos token.Pos) (inventory.SymbolID, bool) {
	if p.consumer != "" {
		return "", true
	}
	return p.resolve.At(pos)
}

// fail keeps the first failure of the walk, which is the one a caller is told.
func (p *referencePass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

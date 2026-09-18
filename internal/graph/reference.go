package graph

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
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

// Reference is one use of a target symbol by one declaration.
type Reference struct {
	// From is the declaration that encloses the referencing identifier, never
	// the file that holds it. A declaration the walk's symbol set does not hold
	// makes no reference, so nothing outside that set is the From of a reference
	// the walk returns; a caller that widens the set past the target's own
	// declarations widens what a From can name.
	From SymbolID
	To   SymbolID

	// Pos is the referencing identifier's rendered position: Filename is
	// target-relative with forward slashes and Column counts UTF-16 code units.
	Pos  token.Position
	Kind RefKind
	Test bool // the referencing file is a test file
}

// References walks every loaded package of one configuration once and returns
// every reference to a symbol in symbols, in a deterministic order, plus the
// test-file rules applied. It issues no per-symbol query: one walk per source
// file resolves every identifier the file holds through the type information the
// load already carries.
//
// A source file that several package variants type-check is walked once, from
// the first variant that reaches it, so a reference a production file makes is
// recorded once whatever the number of test variants that compile that file, and
// a reference a test file makes comes from the variant that compiles it.
//
// Every reference a test file makes carries Test, which is the flag a caller
// filters on to count production references alone. References itself counts
// both.
//
// A reference names a declaration of a package this configuration loaded, so a
// use of another module's declaration and of a name the language itself declares
// is no reference. A file targetRoot does not hold ends the walk with [ErrSource]
// rather than being passed over.
func References(r *load.Result, targetRoot string, read ReadFile, symbols []Symbol) ([]Reference, []TestFileRule, error) {
	p, err := newReferencePass(r, targetRoot, read, symbols)
	if err != nil {
		return nil, nil, err
	}
	if err := p.walk(r.Packages); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(p.refs, byUse)
	return p.refs, []TestFileRule{{Rule: goTestSuffixRule, Matched: p.testFiles}}, nil
}

// byUse orders two references by the position of the referencing identifier,
// then by the symbol referenced, the declaration referencing it and the kind. It
// is a total order because one identifier never references one symbol twice from
// one declaration, while a selector that reaches through an embedded field does
// reference two symbols at one position.
//
//nolint:gocritic // slices.SortFunc fixes a comparator's parameters to values.
func byUse(a, b Reference) int {
	if c := strings.Compare(a.Pos.Filename, b.Pos.Filename); c != 0 {
		return c
	}
	if c := a.Pos.Line - b.Pos.Line; c != 0 {
		return c
	}
	if c := a.Pos.Column - b.Pos.Column; c != 0 {
		return c
	}
	if c := strings.Compare(string(a.To), string(b.To)); c != 0 {
		return c
	}
	if c := strings.Compare(string(a.From), string(b.From)); c != 0 {
		return c
	}
	return int(a.Kind) - int(b.Kind)
}

// site is one declaration's rendered position, which is what a symbol
// identifier spells and what resolves a types.Object to the symbol declared
// where the object is.
type site struct {
	file string
	line int
	col  int
}

// referencePass accumulates the references of one loaded configuration.
type referencePass struct {
	pos       *positions
	info      *types.Info // the type information of the variant that compiles the file being walked
	symbols   map[site]SymbolID
	ids       map[token.Pos]SymbolID // one resolved position, resolved once; empty for a position no symbol declares
	kinds     map[token.Pos]RefKind  // the kind a parent node fixes for an identifier below it
	callees   map[token.Pos]bool     // the identifiers a call expression names as its callee
	reached   map[string]int         // per source file, the variants that compile it
	declaring map[string]bool        // the import paths of the packages this walk enumerates
	err       error
	refs      []Reference
	testFiles int
	test      bool // the file being walked is a test file
}

// newReferencePass prepares one configuration's walk, and refuses a result
// missing the file set or the source reader the rendering needs.
func newReferencePass(r *load.Result, targetRoot string, read ReadFile, symbols []Symbol) (*referencePass, error) {
	if r == nil || r.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", ErrIncompleteLoad)
	}
	if read == nil {
		return nil, fmt.Errorf("%w: no source reader", ErrIncompleteLoad)
	}

	p := &referencePass{
		pos:     newPositions(r.Fset, targetRoot, read),
		symbols: make(map[site]SymbolID, len(symbols)),
		ids:     make(map[token.Pos]SymbolID),
		kinds:   make(map[token.Pos]RefKind),
		callees: make(map[token.Pos]bool),
		reached: make(map[string]int),
	}
	for i := range symbols {
		s := &symbols[i]
		p.symbols[site{file: s.Pos.Filename, line: s.Pos.Line, col: s.Pos.Column}] = s.ID
	}
	return p, nil
}

// walk visits every file of every variant, in one order, and returns the first
// failure the walk met.
func (p *referencePass) walk(pkgs []*packages.Package) error {
	groups := groupVariants(pkgs, p.pos)
	p.declaring = make(map[string]bool, len(groups))
	for _, g := range groups {
		p.declaring[g.pkgPath] = true
	}
	for _, g := range groups {
		for _, pkg := range g.pkgs {
			for _, f := range syntaxFiles(pkg, p.pos) {
				if err := p.walkFile(pkg, f); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// walkFile walks one source file, unless a variant reached it first.
func (p *referencePass) walkFile(pkg *packages.Package, f *ast.File) error {
	position, err := p.pos.render(f.FileStart)
	if err != nil {
		return err
	}
	p.reached[position.Filename]++
	if p.reached[position.Filename] > 1 {
		return nil
	}

	p.info = pkg.TypesInfo
	_, p.test = IsTestFile(position.Filename)
	if p.test {
		p.testFiles++
	}

	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			p.walkFunc(d)
		case *ast.GenDecl:
			p.walkGenDecl(d)
		}
		if p.err != nil {
			return p.err
		}
	}
	return nil
}

// walkFunc walks one function or method. Its receiver, its signature and its
// body reference from the function itself, while a type parameter's constraint
// references from that parameter, because deleting the parameter deletes the
// constraint with it.
func (p *referencePass) walkFunc(d *ast.FuncDecl) {
	id, ok := p.symbolAt(d.Name.Pos())
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
		id, ok := p.symbolAt(n.Pos())
		if !ok {
			return
		}
		p.inspect(f.Type, id)
	}
}

// walkGenDecl walks one declaration group. An import declares a package name
// and references nothing, because an import path is not an identifier.
func (p *referencePass) walkGenDecl(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			p.walkTypeSpec(s)
		case *ast.ValueSpec:
			p.walkValueSpec(s)
		}
	}
}

// walkTypeSpec walks one type declaration. A type's own type parameters belong
// to the type rather than being subjects of their own, so their constraints
// reference from the type.
func (p *referencePass) walkTypeSpec(s *ast.TypeSpec) {
	id, ok := p.symbolAt(s.Name.Pos())
	if !ok {
		return
	}
	if s.TypeParams != nil {
		p.inspect(s.TypeParams, id)
	}
	switch t := s.Type.(type) {
	case *ast.StructType:
		p.walkStruct(t)
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
	ids := make([]SymbolID, 0, len(s.Names))
	for _, n := range s.Names {
		id, ok := p.symbolAt(n.Pos())
		if !ok {
			return
		}
		ids = append(ids, id)
		if s.Type != nil {
			p.inspect(s.Type, id)
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
// own, so nothing in a struct body references from the type that declares it.
func (p *referencePass) walkStruct(t *ast.StructType) {
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
	named := embeddedName(f.Type)
	if named == nil {
		return
	}
	id, ok := p.symbolAt(named.Pos())
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
	inner := anonymousStruct(f.Type)
	members := inner
	for _, n := range f.Names {
		id, ok := p.symbolAt(n.Pos())
		if !ok {
			return
		}
		p.inspectExcept(f.Type, id, inner)
		if members == nil {
			continue
		}
		p.walkStruct(members)
		members = nil
	}
}

// walkInterface walks one interface type. A declared method references from
// itself; an embedded interface and a type-set element declare nothing, so they
// reference from the interface.
func (p *referencePass) walkInterface(t *ast.InterfaceType, owner SymbolID) {
	for _, f := range t.Methods.List {
		if len(f.Names) == 0 {
			if named := embeddedName(f.Type); named != nil {
				p.kinds[named.Pos()] = RefEmbed
			}
			p.inspect(f.Type, owner)
			continue
		}
		for _, n := range f.Names {
			id, ok := p.symbolAt(n.Pos())
			if !ok {
				return
			}
			p.inspect(f.Type, id)
		}
	}
}

// inspect records every reference one subtree makes from encl.
func (p *referencePass) inspect(node ast.Node, encl SymbolID) {
	p.inspectExcept(node, encl, nil)
}

// inspectExcept records every reference one subtree makes from encl, visiting
// nothing at or below skip, which is how a field's anonymous struct is left to
// its own members.
func (p *referencePass) inspectExcept(node ast.Node, encl SymbolID, skip ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil || n == skip || p.err != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.Ident:
			p.record(n, encl)
			return false
		case *ast.SelectorExpr:
			p.reachThrough(n, encl)
		case *ast.AssignStmt:
			for _, target := range n.Lhs {
				p.markWrite(target)
			}
			p.markAppendBack(n)
		case *ast.CompositeLit:
			p.markFieldKeys(n)
		case *ast.IncDecStmt:
			p.markWrite(n.X)
		case *ast.RangeStmt:
			if n.Tok == token.ASSIGN {
				p.markWrite(n.Key)
				p.markWrite(n.Value)
			}
		case *ast.TypeAssertExpr:
			p.markAssert(n.Type)
		case *ast.TypeSwitchStmt:
			p.markCaseAsserts(n)
		case *ast.CallExpr:
			p.markCallee(n.Fun)
			p.markDelete(n)
		}
		return true
	})
}

// record keeps the reference one identifier makes, unless the identifier names
// no object or names one whose declaration is not a symbol of the target.
func (p *referencePass) record(id *ast.Ident, encl SymbolID) {
	obj := p.info.Uses[id]
	if obj == nil {
		return
	}
	to, ok := p.symbolOf(obj)
	if !ok {
		return
	}
	p.add(encl, to, id.Pos(), p.kindOf(id, obj))
}

// reachThrough records the embedded fields one selector reaches through. A
// promoted member is selected through every embedded field on the path, and
// those fields carry no identifier at the selection, so the selector names them.
// Writing a promoted member still only reads the fields that reach it.
func (p *referencePass) reachThrough(e *ast.SelectorExpr, encl SymbolID) {
	sel := p.info.Selections[e]
	if sel == nil {
		return
	}
	index := sel.Index()
	if len(index) < 2 {
		return
	}
	held := sel.Recv()
	for _, i := range index[:len(index)-1] {
		st, ok := structAt(held)
		if !ok || i >= st.NumFields() {
			return
		}
		field := st.Field(i)
		if to, ok := p.symbolOf(field); ok {
			p.add(encl, to, e.Sel.Pos(), RefRead)
		}
		held = field.Type()
	}
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
func (p *referencePass) markWrite(target ast.Expr) {
	switch t := ast.Unparen(target).(type) {
	case *ast.Ident:
		p.kinds[t.Pos()] = RefWrite
	case *ast.SelectorExpr:
		p.kinds[t.Sel.Pos()] = RefWrite
	case *ast.IndexExpr:
		p.markWrite(t.X)
	}
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
			p.kinds[key.Pos()] = RefWrite
		}
	}
}

// markDelete records the collection one delete stores into. Removing an entry
// changes what the map holds, the same way an assignment through a key does.
func (p *referencePass) markDelete(call *ast.CallExpr) {
	if len(call.Args) == 0 || !p.builtin(call.Fun, deleteBuiltin) {
		return
	}
	p.markWrite(call.Args[0])
}

// markAppendBack records the store the append-back idiom performs. In
// x = append(x, v...) and in c.f = append(c.f, v...) the read inside the call is
// how the store is written rather than a use of what the collection holds, so
// both sides write it. An append whose result lands anywhere else, c.f included,
// reads its first argument.
//
// The two sides are matched by the name chain they are written as, which is what
// the language resolves them by: the call is evaluated in the scope the
// assignment is written in, so one spelling names one collection there.
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

// add keeps one reference, rendering the referencing identifier's position.
func (p *referencePass) add(from, to SymbolID, pos token.Pos, kind RefKind) {
	position, err := p.pos.render(pos)
	if err != nil {
		p.fail(err)
		return
	}
	p.refs = append(p.refs, Reference{From: from, To: to, Pos: position, Kind: kind, Test: p.test})
}

// symbolOf resolves the declaration of one object a walked file names. Only a
// package this walk enumerates declares a symbol of the target, so an object of
// another module, an object of a package outside the scope and a name the
// language itself declares resolve to nothing without a position being read.
func (p *referencePass) symbolOf(obj types.Object) (SymbolID, bool) {
	if obj.Pkg() == nil || !p.declaring[obj.Pkg().Path()] {
		return "", false
	}
	return p.symbolAt(obj.Pos())
}

// symbolAt resolves one position of the target to the symbol declared there,
// resolving each position once. A position no symbol declares resolves to
// nothing; a position that does not render fails the walk.
func (p *referencePass) symbolAt(pos token.Pos) (SymbolID, bool) {
	if id, ok := p.ids[pos]; ok {
		return id, id != ""
	}
	position, err := p.pos.render(pos)
	if err != nil {
		p.fail(err)
		return "", false
	}
	id := p.symbols[site{file: position.Filename, line: position.Line, col: position.Column}]
	p.ids[pos] = id
	return id, id != ""
}

// fail keeps the first failure of the walk, which is the one a caller is told.
func (p *referencePass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

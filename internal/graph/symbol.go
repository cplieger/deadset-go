// Package graph builds the graph of one loaded build configuration: the symbols
// a target declares, the references between them, and what the roots keep alive.
//
// A declaration is identified by where it is written, never by the identity of
// the types.Object that carries it. A package and its in-package test variant
// type-check the same file independently and produce distinct objects for one
// declaration, so an object-keyed graph would split the declaration from the
// references its own tests make.
package graph

import (
	"errors"
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

var (
	// ErrIncompleteLoad reports a result missing the file set or the source
	// reader the enumeration needs.
	ErrIncompleteLoad = errors.New("graph: incomplete load result")

	// ErrNoTargetPackage reports a load carrying no package the enumeration can
	// walk, which leaves the target without a declaration to reason about.
	ErrNoTargetPackage = errors.New("graph: the load carries no package to enumerate")
)

// SymbolKind names what a symbol is. String is the spelling a report and a
// message carry.
type SymbolKind uint8

// The kinds a Go analysis enumerates.
const (
	KindFunc SymbolKind = iota
	KindMethod
	KindType
	KindInterface
	KindInterfaceMethod
	KindField
	KindConst
	KindVar
	KindTypeParam
	KindPackage
	KindFile
)

var kindNames = [...]string{
	KindFunc:            "function",
	KindMethod:          "method",
	KindType:            "type",
	KindInterface:       "interface",
	KindInterfaceMethod: "interface-method",
	KindField:           "field",
	KindConst:           "constant",
	KindVar:             "variable",
	KindTypeParam:       "type-parameter",
	KindPackage:         "package",
	KindFile:            "file",
}

// String returns the kind's spelling, and a numbered form for a value outside
// the set so a message never loses the number it was given.
func (k SymbolKind) String() string {
	if int(k) >= len(kindNames) {
		return "SymbolKind(" + strconv.Itoa(int(k)) + ")"
	}
	return kindNames[k]
}

// SymbolID identifies one declaration across build configurations and in every
// report: the target-relative path of the file that declares it, its line and
// its column. Inside one configuration the enumeration keys on the raw token.Pos
// instead, which needs no allocation.
type SymbolID string

// Symbol is one declaration of the target.
type Symbol struct {
	ID      SymbolID
	Ref     string
	Name    string
	PkgPath string
	Parent  SymbolID // the container's ID; empty for a package

	// Pos is rendered: Filename is target-relative with forward slashes and
	// Column counts UTF-16 code units rather than the bytes Go reports.
	Pos      token.Position
	EndLine  int
	Kind     SymbolKind
	Exported bool
	Blank    bool // declared with the blank identifier
}

// ReadFile returns the bytes of one file the load compiled. A loaded package
// carries no source text, and converting Go's byte column into the UTF-16 code
// units a position counts needs the line a declaration starts on; the
// composition root passes os.ReadFile.
type ReadFile func(name string) ([]byte, error)

// Symbols enumerates every declaration of one loaded configuration, one Symbol
// per source site whatever the number of package variants that type-check the
// site. Every position is rendered relative to targetRoot, and a file that root
// does not hold ends the enumeration with [ErrSource] rather than being passed
// over. The order is by rendered position and then by Ref, so
// two calls over one result return the same slice. Symbols reaches the
// filesystem only through read.
func Symbols(r *load.Result, targetRoot string, read ReadFile) ([]Symbol, error) {
	if r == nil || r.Fset == nil {
		return nil, fmt.Errorf("%w: no file set", ErrIncompleteLoad)
	}
	if read == nil {
		return nil, fmt.Errorf("%w: no source reader", ErrIncompleteLoad)
	}

	e := &enumeration{
		pos:     newPositions(r.Fset, targetRoot, read),
		seen:    make(map[token.Pos]SymbolID),
		refs:    make(map[SymbolID]string),
		typeIDs: make(map[string]SymbolID),
	}
	for _, g := range groupVariants(r.Packages, e.pos) {
		if err := e.walkPackage(g); err != nil {
			return nil, err
		}
	}
	if len(e.symbols) == 0 {
		if len(r.Packages) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNoTargetPackage, targetRoot)
	}

	e.resolveReceivers()
	slices.SortFunc(e.symbols, bySite)
	return e.symbols, nil
}

// bySite orders two symbols by rendered position and then by reference. It is a
// total order because two symbols never share a position.
//
//nolint:gocritic // slices.SortFunc fixes a comparator's parameters to values.
func bySite(a, b Symbol) int {
	if c := strings.Compare(a.Pos.Filename, b.Pos.Filename); c != 0 {
		return c
	}
	if c := a.Pos.Line - b.Pos.Line; c != 0 {
		return c
	}
	if c := a.Pos.Column - b.Pos.Column; c != 0 {
		return c
	}
	return strings.Compare(a.Ref, b.Ref)
}

// variantGroup holds every loaded package that shares one import path: the
// package itself and the test variants that type-check its files again.
type variantGroup struct {
	anchor  *ast.File
	pkgPath string
	pkgs    []*packages.Package
}

// groupVariants gathers the loaded packages by import path, and orders the groups
// and the variants inside each so one configuration is walked in one order. A
// package the type checker did not check and a package with no syntax tree carry
// no declaration and are left out. The anchor is the file whose name sorts first
// across every variant, so the position of the package itself does not depend on
// which variant reached it.
func groupVariants(pkgs []*packages.Package, pos *positions) []variantGroup {
	byPath := make(map[string]*variantGroup)
	for _, p := range pkgs {
		if p.PkgPath == "" || p.TypesInfo == nil {
			continue
		}
		files := syntaxFiles(p, pos)
		if len(files) == 0 {
			continue
		}
		g := byPath[p.PkgPath]
		if g == nil {
			g = &variantGroup{pkgPath: p.PkgPath}
			byPath[p.PkgPath] = g
		}
		g.pkgs = append(g.pkgs, p)
		for _, f := range files {
			if g.anchor == nil || pos.base(f.FileStart) < pos.base(g.anchor.FileStart) {
				g.anchor = f
			}
		}
	}

	groups := make([]variantGroup, 0, len(byPath))
	for _, g := range byPath {
		slices.SortFunc(g.pkgs, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
		groups = append(groups, *g)
	}
	slices.SortFunc(groups, func(a, b variantGroup) int { return strings.Compare(a.pkgPath, b.pkgPath) })
	return groups
}

// syntaxFiles returns the package's syntax trees ordered by file name, so one
// configuration is walked in one order whatever order the load reported.
func syntaxFiles(p *packages.Package, pos *positions) []*ast.File {
	files := slices.Clone(p.Syntax)
	slices.SortFunc(files, func(a, b *ast.File) int {
		return strings.Compare(pos.base(a.FileStart), pos.base(b.FileStart))
	})
	return files
}

// enumeration accumulates the symbols of one loaded configuration.
type enumeration struct {
	pos     *positions
	seen    map[token.Pos]SymbolID // one declaration per source site
	refs    map[SymbolID]string    // a symbol's reference, for a container to lend
	typeIDs map[string]SymbolID    // import path and type name to that type's symbol
	pending []receiverOwner
	symbols []Symbol
}

// receiverOwner records a method whose container is a type the walk may not have
// reached yet.
type receiverOwner struct {
	owner string
	index int
}

// declaration is one symbol the walk found, before its position is rendered.
type declaration struct {
	name     string // the display name a text line renders
	pkgPath  string
	parent   SymbolID
	chain    []string // the name chain from the package inward, own name last
	pos, end token.Pos
	kind     SymbolKind
}

// append renders one declaration's position and keeps it, unless a variant of
// the same package already contributed the same source site.
func (e *enumeration) append(d *declaration) (SymbolID, error) {
	if id, ok := e.seen[d.pos]; ok {
		return id, nil
	}
	position, err := e.pos.render(d.pos)
	if err != nil {
		return "", err
	}
	end, err := e.pos.render(d.end)
	if err != nil {
		return "", err
	}

	own := ""
	if len(d.chain) > 0 {
		own = d.chain[len(d.chain)-1]
	}
	named := d.kind != KindPackage && d.kind != KindFile
	blank := named && own == "_"
	id := e.pos.symbolID(position)

	// A package may hold any number of blank declarations, so the name identifies
	// none of them and the declaration takes its container's reference.
	ref := Ref(d.kind, d.pkgPath, d.chain)
	if blank {
		ref = e.refs[d.parent]
	}

	e.seen[d.pos] = id
	e.refs[id] = ref
	e.symbols = append(e.symbols, Symbol{
		ID:       id,
		Ref:      ref,
		Name:     d.name,
		PkgPath:  d.pkgPath,
		Parent:   d.parent,
		Pos:      position,
		EndLine:  end.Line,
		Kind:     d.kind,
		Exported: named && ast.IsExported(own),
		Blank:    blank,
	})
	return id, nil
}

// walkPackage enumerates one import path: the package itself, then every file of
// every variant that type-checks it.
func (e *enumeration) walkPackage(g variantGroup) error {
	pkgID, err := e.append(&declaration{
		pos:     g.anchor.Name.Pos(),
		end:     g.anchor.Name.End(),
		kind:    KindPackage,
		pkgPath: g.pkgPath,
		name:    g.pkgPath,
	})
	if err != nil {
		return err
	}
	for _, p := range g.pkgs {
		for _, f := range syntaxFiles(p, e.pos) {
			if err := e.walkFile(p, f, pkgID); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkFile enumerates one source file and the declarations it holds.
func (e *enumeration) walkFile(p *packages.Package, f *ast.File, pkgID SymbolID) error {
	base := e.pos.base(f.FileStart)
	if _, err := e.append(&declaration{
		pos:     f.FileStart,
		end:     f.FileEnd,
		kind:    KindFile,
		pkgPath: p.PkgPath,
		chain:   []string{base},
		name:    base,
		parent:  pkgID,
	}); err != nil {
		return err
	}

	for _, d := range f.Decls {
		var err error
		switch d := d.(type) {
		case *ast.FuncDecl:
			err = e.walkFunc(p, d, pkgID)
		case *ast.GenDecl:
			err = e.walkGenDecl(p, d, pkgID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// walkFunc enumerates one function or method and the type parameters it
// declares. A method's container is resolved once every type is known.
func (e *enumeration) walkFunc(p *packages.Package, d *ast.FuncDecl, pkgID SymbolID) error {
	decl := declaration{
		pos:     d.Name.Pos(),
		end:     d.End(),
		kind:    KindFunc,
		pkgPath: p.PkgPath,
		chain:   []string{d.Name.Name},
		name:    d.Name.Name,
		parent:  pkgID,
	}
	owner := ""
	if d.Recv != nil {
		base, pointer := receiverBase(d.Recv)
		if base == "" {
			return nil
		}
		owner = base
		decl.kind = KindMethod
		decl.chain = []string{base, d.Name.Name}
		decl.name = base + "." + d.Name.Name
		if pointer {
			decl.name = "(*" + base + ")." + d.Name.Name
		}
		decl.parent = ""
	}

	before := len(e.symbols)
	id, err := e.append(&decl)
	if err != nil {
		return err
	}
	// A second variant of the package resolves this site to the symbol the first
	// variant contributed and appends nothing, so recording a receiver then would
	// name whichever symbol was appended last.
	if owner != "" && len(e.symbols) > before {
		e.pending = append(e.pending, receiverOwner{index: len(e.symbols) - 1, owner: owner})
	}
	return e.walkTypeParams(d.Type.TypeParams, &decl, id)
}

// walkGenDecl enumerates the types, constants and variables of one declaration
// group. An import declares no symbol of the target.
func (e *enumeration) walkGenDecl(p *packages.Package, d *ast.GenDecl, pkgID SymbolID) error {
	switch d.Tok {
	case token.TYPE:
		for _, spec := range d.Specs {
			s, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if err := e.walkTypeSpec(p, s, pkgID); err != nil {
				return err
			}
		}
		return nil
	case token.CONST:
		return e.walkValueSpecs(p, d.Specs, KindConst, pkgID)
	case token.VAR:
		return e.walkValueSpecs(p, d.Specs, KindVar, pkgID)
	default:
		return nil
	}
}

// walkValueSpecs enumerates the names one constant or variable group declares.
func (e *enumeration) walkValueSpecs(p *packages.Package, specs []ast.Spec, kind SymbolKind, pkgID SymbolID) error {
	for _, spec := range specs {
		s, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, n := range s.Names {
			if _, err := e.append(&declaration{
				pos: n.Pos(), end: s.End(), kind: kind, pkgPath: p.PkgPath,
				chain: []string{n.Name}, name: n.Name, parent: pkgID,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkTypeSpec enumerates one type declaration and the members it declares. A
// type's own type parameters belong to the type rather than being subjects of
// their own, so they are not enumerated.
func (e *enumeration) walkTypeSpec(p *packages.Package, s *ast.TypeSpec, pkgID SymbolID) error {
	kind := KindType
	if isInterface(p.TypesInfo.Defs[s.Name]) {
		kind = KindInterface
	}
	chain := []string{s.Name.Name}
	id, err := e.append(&declaration{
		pos: s.Name.Pos(), end: s.End(), kind: kind, pkgPath: p.PkgPath,
		chain: chain, name: s.Name.Name, parent: pkgID,
	})
	if err != nil {
		return err
	}
	e.typeIDs[typeKey(p.PkgPath, s.Name.Name)] = id

	switch t := s.Type.(type) {
	case *ast.StructType:
		return e.walkStruct(p, t, chain, id)
	case *ast.InterfaceType:
		return e.walkInterface(p, t, chain, id)
	}
	return nil
}

// walkStruct enumerates the fields of one struct type.
func (e *enumeration) walkStruct(p *packages.Package, t *ast.StructType, chain []string, parent SymbolID) error {
	for _, f := range t.Fields.List {
		if err := e.walkField(p, f, chain, parent); err != nil {
			return err
		}
	}
	return nil
}

// walkField enumerates one field group, stepping into an anonymous struct
// through the field that carries it. An embedded field is named by the
// unqualified name of the type it embeds.
func (e *enumeration) walkField(p *packages.Package, f *ast.Field, chain []string, parent SymbolID) error {
	if len(f.Names) == 0 {
		embedded := embeddedName(f.Type)
		if embedded == nil {
			return nil
		}
		_, err := e.appendField(p, embedded, f.End(), chain, parent)
		return err
	}
	for _, n := range f.Names {
		id, err := e.appendField(p, n, f.End(), chain, parent)
		if err != nil {
			return err
		}
		inner := anonymousStruct(f.Type)
		if inner == nil {
			continue
		}
		if err := e.walkStruct(p, inner, append(slices.Clone(chain), n.Name), id); err != nil {
			return err
		}
	}
	return nil
}

// appendField keeps one struct field, whose reference always carries the chain
// of structs that reaches it.
func (e *enumeration) appendField(p *packages.Package, n *ast.Ident, end token.Pos, chain []string, parent SymbolID) (SymbolID, error) {
	own := append(slices.Clone(chain), n.Name)
	return e.append(&declaration{
		pos: n.Pos(), end: end, kind: KindField, pkgPath: p.PkgPath,
		chain: own, name: strings.Join(own, "."), parent: parent,
	})
}

// walkInterface enumerates the methods one interface type declares. An embedded
// interface and a type-set element declare no method of this interface.
func (e *enumeration) walkInterface(p *packages.Package, t *ast.InterfaceType, chain []string, parent SymbolID) error {
	for _, f := range t.Methods.List {
		for _, n := range f.Names {
			own := append(slices.Clone(chain), n.Name)
			if _, err := e.append(&declaration{
				pos: n.Pos(), end: f.End(), kind: KindInterfaceMethod, pkgPath: p.PkgPath,
				chain: own, name: strings.Join(own, "."), parent: parent,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkTypeParams enumerates the type parameters of one function or method.
func (e *enumeration) walkTypeParams(list *ast.FieldList, owner *declaration, ownerID SymbolID) error {
	if list == nil {
		return nil
	}
	for _, f := range list.List {
		for _, n := range f.Names {
			if _, err := e.append(&declaration{
				pos: n.Pos(), end: f.End(), kind: KindTypeParam, pkgPath: owner.pkgPath,
				chain: append(slices.Clone(owner.chain), n.Name),
				name:  owner.name + "[" + n.Name + "]", parent: ownerID,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveReceivers sets each method's container now that every type of every
// package is known.
func (e *enumeration) resolveReceivers() {
	for _, m := range e.pending {
		if id, ok := e.typeIDs[typeKey(e.symbols[m.index].PkgPath, m.owner)]; ok {
			e.symbols[m.index].Parent = id
		}
	}
}

// typeKey names one type of one package.
func typeKey(pkgPath, name string) string { return pkgPath + " " + name }

// isInterface reports whether a type name denotes an interface, however the
// declaration spells it: a literal interface type, a defined type over one, or
// an alias of one.
func isInterface(obj types.Object) bool {
	tn, ok := obj.(*types.TypeName)
	return ok && types.IsInterface(tn.Type())
}

// receiverBase returns the name of a method receiver's base type and whether the
// receiver is a pointer. Type arguments carry nothing and are dropped.
func receiverBase(recv *ast.FieldList) (name string, pointer bool) {
	if recv == nil || len(recv.List) != 1 {
		return "", false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr, pointer = star.X, true
	}
	switch t := expr.(type) {
	case *ast.IndexExpr:
		expr = t.X
	case *ast.IndexListExpr:
		expr = t.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name, pointer
	}
	return "", pointer
}

// embeddedName returns the identifier that names an embedded field, which the
// language defines as the unqualified name of the embedded type.
func embeddedName(expr ast.Expr) *ast.Ident {
	switch t := expr.(type) {
	case *ast.Ident:
		return t
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return nil
}

// anonymousStruct returns the anonymous struct type a field carries, reached
// through a pointer, a slice, an array, a channel or a map value. A map key, a
// signature and an interface method's parameters are not stepped into, because
// two fields of one name would then share one reference.
func anonymousStruct(expr ast.Expr) *ast.StructType {
	switch t := expr.(type) {
	case *ast.StructType:
		return t
	case *ast.StarExpr:
		return anonymousStruct(t.X)
	case *ast.ArrayType:
		return anonymousStruct(t.Elt)
	case *ast.ChanType:
		return anonymousStruct(t.Value)
	case *ast.MapType:
		return anonymousStruct(t.Value)
	}
	return nil
}

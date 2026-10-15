package graph

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// RootKind names why a symbol is a root.
type RootKind uint8

// The classes of root a Go analysis detects, then the two a maintainer configures.
const (
	RootMain         RootKind = iota // main in a main package
	RootInit                         // every init
	RootTest                         // a test, benchmark, example or fuzz function of a test file, or a method a suite runner calls
	RootLinkname                     // a name a go:linkname directive gives the linker
	RootCgoExport                    // a function under an //export directive in a file importing "C"
	RootBlank                        // a declaration with the blank identifier
	RootPublishedAPI                 // an exported symbol a consumer outside the module can name
	RootConfigured                   // an exact reference from the configuration
	RootPattern                      // a pattern from the configuration
	RootTypeError                    // a declaration a type error skipped, or what its unresolved expression could reach
	RootProgram                      // a declaration a file run on its own under the ignore tag names
	RootGenerate                     // a declaration a go:generate directive names as a generator's input
	RootFuzz                         // a go-fuzz entry point of a file a go-fuzz build may compile
	RootSwagger                      // a declaration a go-swagger directive of its doc comment names
	RootSwagRoute                    // a function a swag route annotation of its doc comment documents
)

var rootNames = [...]string{
	RootMain:         "main",
	RootInit:         "init",
	RootTest:         "test",
	RootLinkname:     "linkname",
	RootCgoExport:    "cgo-export",
	RootBlank:        "blank",
	RootPublishedAPI: "published-api",
	RootConfigured:   "configured",
	RootPattern:      "pattern",
	RootTypeError:    "type-error",
	RootProgram:      "program",
	RootGenerate:     "go-generate",
	RootFuzz:         "go-fuzz",
	RootSwagger:      "swagger",
	RootSwagRoute:    "swag-route",
}

// String returns the kind's spelling, and a numbered form for a value outside the
// set so a message never loses the number it was given.
func (k RootKind) String() string {
	if int(k) >= len(rootNames) {
		return "RootKind(" + strconv.Itoa(int(k)) + ")"
	}
	return rootNames[k]
}

// Root is one symbol the analysis keeps live under reachability, and why.
type Root struct {
	ID     SymbolID
	Source string // the configured string that named it, empty for a detected class

	// Config is the place in the matrix of the build configuration the root was
	// detected in. One detection fills none of it, because a detection is of one
	// configuration; [Merge] is what fills it.
	Config int
	Kind   RootKind
}

// RootOptions is the configured half of the root set.
//
// A pattern is matched against a symbol's reference. Two characters are special
// and nothing else is: * matches any run of characters, the solidus included, and
// ? matches exactly one character. A string holding neither is exact and names
// the symbol whose reference it spells.
type RootOptions struct {
	// CheckedElsewhere holds the [load.Result.PackageKey] of every package whose type
	// errors root nothing, because another configuration of a derived matrix
	// type-checks it ([CheckedElsewhere]).
	CheckedElsewhere map[string]bool

	Patterns     []string // roots.patterns from the resolved configuration
	PublishedAPI bool     // the target is a library, so its published API is rooted
}

// Unmatched is a configured root or pattern that names no symbol. Source is the
// configured string, spelled as Root.Source spells the one that did name a
// symbol.
type Unmatched struct {
	Source string
}

// Roots returns every root of one configuration, and every configured string
// that named no symbol in the order the configuration lists them.
//
// The order is by site, then kind, then configured string, and a symbol that is
// a root for more than one reason appears once per reason, so a report can name
// every reason one symbol is live. A root makes its symbol live under
// reachability, and every root but the published API makes it live under
// reference counting too. Roots reaches the filesystem only through read.
func Roots(r *load.Result, targetRoot string, read ReadFile, symbols []Symbol, opts RootOptions) ([]Root, []Unmatched, error) {
	if r == nil || r.Fset == nil {
		return nil, nil, fmt.Errorf("%w: no file set", ErrIncompleteLoad)
	}
	if read == nil {
		return nil, nil, fmt.Errorf("%w: no source reader", ErrIncompleteLoad)
	}

	d := &rootDetection{
		pos:        newPositions(r.Fset, targetRoot, read),
		byPosition: make(map[token.Position]SymbolID, len(symbols)),
		byID:       make(map[SymbolID]Symbol, len(symbols)),
		classes:    make(map[string][]packageClass),
		byPath:     make(map[string][]*packages.Package),
		found:      make(map[Root]struct{}),
		inputDirs:  make(map[string]bool),
	}
	for i := range symbols {
		d.byPosition[symbols[i].Pos] = symbols[i].ID
		d.byID[symbols[i].ID] = symbols[i]
	}

	if err := d.walk(r); err != nil {
		return nil, nil, err
	}
	d.typeErrors(r, opts.CheckedElsewhere)
	d.programs(r)
	if !opts.PublishedAPI {
		unlinked := UnlinkedPackages(r)
		d.unlinked = func(s *Symbol) bool {
			return unlinked[r.PackageKey(s.PkgPath, filepath.Join(targetRoot, filepath.FromSlash(path.Dir(s.Pos.Filename))))]
		}
	}
	d.declared(symbols)
	d.generatorPackageInputs(symbols)
	if opts.PublishedAPI {
		d.publishedAPI(symbols)
		if err := d.promoted(); err != nil {
			return nil, nil, err
		}
	}
	unmatched := d.configured(symbols, opts.Patterns)

	return slices.SortedFunc(maps.Keys(d.found), rootOrder(symbols)), unmatched, nil
}

// rootOrder is the total order Roots returns its result in: the site of the
// symbol each root names, then the kind, then the configured string. It is the
// order the enumeration and the reference pass return their own results in, so a
// root set reads in file order beside them.
//
// Two roots of one symbol are ordered by kind, and two configured roots of one
// symbol and kind by the string that named each.
func rootOrder(symbols []Symbol) func(a, b Root) int {
	sites := make(map[SymbolID]Symbol, len(symbols))
	for i := range symbols {
		sites[symbols[i].ID] = symbols[i]
	}
	return func(a, b Root) int {
		return cmp.Or(
			bySite(sites[a.ID], sites[b.ID]),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Source, b.Source),
		)
	}
}

// rootDetection accumulates the roots of one loaded configuration.
type rootDetection struct {
	unlinked   func(*Symbol) bool // whether no build links an application's package declaring a symbol
	pos        *positions
	byPosition map[token.Position]SymbolID // a rendered position to the declaration at it
	byID       map[SymbolID]Symbol
	classes    map[string][]packageClass      // by import path, one per variant group
	byPath     map[string][]*packages.Package // import path to its type-checked variants
	inputDirs  map[string]bool                // target-relative directories of generatorPackages
	found      map[Root]struct{}
}

// add keeps one root, once per symbol, kind and configured string.
func (d *rootDetection) add(id SymbolID, kind RootKind, source string) {
	d.found[Root{ID: id, Kind: kind, Source: source}] = struct{}{}
}

// addAt keeps the declaration at pos as a root of kind. A position no enumerated
// declaration holds is not a root: the enumeration is what decides which
// declarations the analysis reasons about.
func (d *rootDetection) addAt(pos token.Pos, kind RootKind) error {
	rendered, err := d.pos.render(pos)
	if err != nil {
		return err
	}
	if id, ok := d.byPosition[rendered]; ok {
		d.add(id, kind, "")
	}
	return nil
}

// programs roots every declaration of the target a program names: a program's main
// is a root of its own, and its own declarations are no symbols, so what any of
// them names is reached from it.
func (d *rootDetection) programs(r *load.Result) {
	for _, program := range r.Programs {
		for _, obj := range program.Info.Uses {
			// A position outside the target renders to no declaration of it.
			if rendered, err := d.pos.render(obj.Pos()); err == nil {
				if id, ok := d.byPosition[rendered]; ok {
					d.add(id, RootProgram, "")
				}
			}
		}
	}
}

// walk classifies every import path of the load and reads the directives and the
// signatures the file's own syntax carries. Variants of one package are walked in
// the order the enumeration walks them, so one configuration yields one order.
func (d *rootDetection) walk(r *load.Result) error {
	groups := groupVariants(r.Packages, d.pos)
	d.index(groups)
	for _, g := range groups {
		d.classify(g)
		for _, p := range g.pkgs {
			for _, f := range syntaxFiles(p, d.pos) {
				if err := d.walkFile(p, f); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// index keeps every type-checked variant by the import path a linkname directive
// of another package spells.
func (d *rootDetection) index(groups []variantGroup) {
	for _, g := range groups {
		for _, p := range g.pkgs {
			if p.Types != nil {
				d.byPath[g.pkgPath] = append(d.byPath[g.pkgPath], p)
			}
		}
	}
}

// classify records what one variant group's package is: a main package, whose
// declarations no consumer can import, or an importable package of the module.
//
// A path holding an internal element is importable only inside the module, so
// nothing outside decides its liveness.
func (d *rootDetection) classify(g variantGroup) {
	var checked *types.Package
	for _, p := range g.pkgs {
		if p.Types != nil {
			checked = p.Types
			break
		}
	}
	class := packageClass{
		types: checked,
		dir:   path.Dir(d.pos.relative(d.pos.fset.Position(g.anchor.FileStart).Filename)),
		main:  checked != nil && checked.Name() == mainPackage,
	}
	class.published = !class.main && !slices.Contains(strings.Split(g.pkgPath, "/"), internalElement)
	d.classes[g.pkgPath] = append(d.classes[g.pkgPath], class)
}

// packageClass is what one variant group's package is.
type packageClass struct {
	types     *types.Package
	dir       string // the target-relative directory of the group's anchor file
	main      bool   // a main package, whose declarations no consumer can import
	published bool   // a package a consumer outside the module can import
}

// classOf is the class of the package declaring s. Where two packages share s's
// import path, it is the one whose directory holds s's file.
func (d *rootDetection) classOf(s *Symbol) packageClass {
	classes := d.classes[s.PkgPath]
	if len(classes) == 1 {
		return classes[0]
	}
	for _, class := range classes {
		if class.dir == path.Dir(s.Pos.Filename) {
			return class
		}
	}
	return packageClass{}
}

// walkFile reads one file's test functions and suite runs, linkname, generate and
// swagger directives, swag routes, cgo exports and go-fuzz entry points.
func (d *rootDetection) walkFile(p *packages.Package, f *ast.File) error {
	if err := d.linknames(p, f); err != nil {
		return err
	}
	if err := d.generatorInputs(p, f); err != nil {
		return err
	}
	if err := d.swaggerDeclarations(f); err != nil {
		return err
	}
	if err := d.suiteMethods(p, f); err != nil {
		return err
	}
	d.generatorPackages(f)
	return d.functionRoots(p, f)
}

// functionRoots reads one file's test functions, swag routes, cgo exports and go-fuzz
// entry points.
func (d *rootDetection) functionRoots(p *packages.Package, f *ast.File) error {
	fuzz := goFuzzFile(f)
	_, test := IsTestFile(d.pos.base(f.FileStart))
	cgo := importsPath(f, cgoImport)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var err error
		if swagRoute(fn.Doc) {
			err = d.addAt(fn.Name.Pos(), RootSwagRoute)
		}
		switch {
		case err != nil:
		// None of the classes below reaches a method: a test function is called by
		// the generated test main, an exported function by C and an entry point by
		// the fuzzer, and none has a receiver to call it on.
		case fn.Recv != nil:
		case test && testFunction(p, fn):
			err = d.addAt(fn.Name.Pos(), RootTest)
		case cgo && cgoExported(fn):
			err = d.addAt(fn.Name.Pos(), RootCgoExport)
		case fuzz && goFuzzEntry(p, fn):
			err = d.addAt(fn.Name.Pos(), RootFuzz)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// linknames keeps every function and variable a //go:linkname directive of this
// file names, on either side of the directive.
//
// The directive is enabled only in a file that imports "unsafe", and its position
// does not decide which declaration it names, so the local name is resolved in
// the package's scope rather than by adjacency. The qualified name it joins the
// local one to is resolved in every loaded variant of the package whose import
// path it spells, the last full stop separating the path from the name.
func (d *rootDetection) linknames(p *packages.Package, f *ast.File) error {
	if p.Types == nil || !importsPath(f, unsafeImport) {
		return nil
	}
	for _, group := range f.Comments {
		for _, c := range group.List {
			local, qualified, ok := linknameDirective(c.Text)
			if !ok {
				continue
			}
			if err := d.linked(d.joined(p, local, qualified)); err != nil {
				return err
			}
		}
	}
	return nil
}

// joined returns what one directive names: the local name in p's scope, and the
// qualified name in every loaded variant of the package whose path it spells.
func (d *rootDetection) joined(p *packages.Package, local, qualified string) []types.Object {
	named := []types.Object{p.Types.Scope().Lookup(local)}
	if at := strings.LastIndex(qualified, "."); at > 0 {
		for _, remote := range d.byPath[qualified[:at]] {
			named = append(named, remote.Types.Scope().Lookup(qualified[at+1:]))
		}
	}
	return named
}

// linked keeps each function and variable of objs as a linkname root; the linker
// joins no other kind of declaration.
func (d *rootDetection) linked(objs []types.Object) error {
	for _, obj := range objs {
		switch obj.(type) {
		case *types.Func, *types.Var:
			if err := d.addAt(obj.Pos(), RootLinkname); err != nil {
				return err
			}
		}
	}
	return nil
}

// declared keeps the classes the enumeration alone decides. An init function and a
// blank declaration run because their package is linked, so in a package no build
// links neither is a root.
func (d *rootDetection) declared(symbols []Symbol) {
	for i := range symbols {
		s := &symbols[i]
		linked := d.unlinked == nil || !d.unlinked(s)
		if s.Blank && linked {
			d.add(s.ID, RootBlank, "")
		}
		if s.Kind != KindFunc {
			continue
		}
		switch {
		case s.Name == initFunction && linked:
			d.add(s.ID, RootInit, "")
		case s.Name == mainFunction && d.classOf(s).main:
			d.add(s.ID, RootMain, "")
		}
	}
}

// publishedAPI keeps every symbol a consumer outside the module can name: an
// exported declaration of an importable package, in a file that package compiles,
// whose every container is exported too.
//
// A type parameter is not in that set. It is exported by its name and no consumer
// can write it, because a caller supplies a type argument by position.
func (d *rootDetection) publishedAPI(symbols []Symbol) {
	for i := range symbols {
		s := &symbols[i]
		_, test := IsTestFile(s.Pos.Filename)
		switch {
		case !s.Exported || s.Kind == KindTypeParam:
		case !d.classOf(s).published || test:
		case !d.exportedContainers(s.Parent):
		default:
			d.add(s.ID, RootPublishedAPI, "")
		}
	}
}

// promoted keeps every exported method a value of an exported type of an importable
// package selects through an embedded field of any visibility: a consumer calls it
// as it calls a method the type declares.
func (d *rootDetection) promoted() error {
	for _, pkgPath := range slices.Sorted(maps.Keys(d.classes)) {
		for _, class := range d.classes[pkgPath] {
			if class.published && class.types != nil {
				if err := d.promotedIn(class.types.Scope()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// promotedIn is [rootDetection.promoted] over one package's scope.
func (d *rootDetection) promotedIn(scope *types.Scope) error {
	for _, name := range scope.Names() {
		typeName, isType := scope.Lookup(name).(*types.TypeName)
		if !isType || !typeName.Exported() || types.IsInterface(typeName.Type()) {
			continue
		}
		if err := d.promotedBy(typeName.Type()); err != nil {
			return err
		}
	}
	return nil
}

// promotedBy keeps every exported method of the target a value of t or a pointer
// to it selects through an embedded field.
func (d *rootDetection) promotedBy(t types.Type) error {
	for selection := range types.NewMethodSet(types.NewPointer(t)).Methods() {
		obj := selection.Obj()
		if len(selection.Index()) < 2 || !obj.Exported() || obj.Pkg() == nil || d.byPath[obj.Pkg().Path()] == nil {
			continue
		}
		if err := d.addAt(obj.Pos(), RootPublishedAPI); err != nil {
			return err
		}
	}
	return nil
}

// exportedContainers reports whether parent and every container above it is
// exported. A container the enumeration does not hold is not evidence that
// nothing outside reaches the symbol below it, so it counts as exported.
func (d *rootDetection) exportedContainers(parent SymbolID) bool {
	for parent != "" {
		container, ok := d.byID[parent]
		if !ok || container.Kind == KindPackage || container.Kind == KindFile {
			return true
		}
		if !container.Exported {
			return false
		}
		parent = container.Parent
	}
	return true
}

// configured keeps every symbol the configuration names and returns every string
// that named none. A string the configuration lists twice is one configured root.
func (d *rootDetection) configured(symbols []Symbol, patterns []string) []Unmatched {
	var unmatched []Unmatched
	seen := make(map[string]bool, len(patterns))
	for _, pattern := range patterns {
		if seen[pattern] {
			continue
		}
		seen[pattern] = true

		kind := RootConfigured
		if isPattern(pattern) {
			kind = RootPattern
		}
		matched := false
		for i := range symbols {
			// A blank declaration carries its container's reference rather than
			// one of its own, so no configured string names it.
			if symbols[i].Blank || !matchRef(pattern, symbols[i].Ref) {
				continue
			}
			d.add(symbols[i].ID, kind, pattern)
			matched = true
		}
		if !matched {
			unmatched = append(unmatched, Unmatched{Source: pattern})
		}
	}
	return unmatched
}

// The names and the paths a root class is spelled with.
const (
	mainPackage     = "main"
	mainFunction    = "main"
	initFunction    = "init"
	internalElement = "internal"
	testingPath     = "testing"
	cgoImport       = `"C"`
	unsafeImport    = `"unsafe"`
	exportDirective = "//export "
	testPrefix      = "Test"
	benchmarkPrefix = "Benchmark"
)

// testFamilies are the name prefixes the toolchain runs from a test file, each
// with the testing type its function takes a pointer to. TestMain takes M, and
// the toolchain runs it as an ordinary test when it takes T instead.
var testFamilies = [...]struct{ prefix, param string }{
	{testPrefix, "T"},
	{benchmarkPrefix, "B"},
	{"Fuzz", "F"},
}

// importsPath reports whether the file imports the given quoted path, a blank
// import included.
func importsPath(f *ast.File, quoted string) bool {
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == quoted {
			return true
		}
	}
	return false
}

// cgoExported reports whether fn carries the //export directive that gives C a
// name for it. The directive names the function it precedes, so a name that
// disagrees is refused by the toolchain rather than exporting something else.
func cgoExported(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	for _, c := range fn.Doc.List {
		if name, ok := strings.CutPrefix(c.Text, exportDirective); ok && strings.TrimSpace(name) == fn.Name.Name {
			return true
		}
	}
	return false
}

// testFunction reports whether fn is a function of a test family, under the rule
// the toolchain applies: the name carries the family's prefix and is not a longer
// word, and the signature takes a pointer to that family's testing type and
// returns nothing. An example takes nothing and returns nothing, and the
// toolchain compiles one into the test binary whether or not it declares output.
func testFunction(p *packages.Package, fn *ast.FuncDecl) bool {
	if fn.Body == nil || fn.Type.TypeParams.NumFields() > 0 {
		return false
	}
	name := fn.Name.Name
	if name == "TestMain" {
		return testSignature(p, fn, "M") || testSignature(p, fn, "T")
	}
	for _, family := range testFamilies {
		if hasTestPrefix(name, family.prefix) {
			return testSignature(p, fn, family.param)
		}
	}
	return hasTestPrefix(name, "Example") &&
		fn.Type.Params.NumFields() == 0 && fn.Type.Results.NumFields() == 0
}

// hasTestPrefix reports whether name is the family prefix or the prefix followed
// by something that is not a lower-case letter, so Testing is not a test.
func hasTestPrefix(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(first)
}

// testSignature reports whether fn takes one pointer to the named type of package
// testing and returns nothing.
func testSignature(p *packages.Package, fn *ast.FuncDecl, param string) bool {
	obj, _ := p.TypesInfo.Defs[fn.Name].(*types.Func)
	if obj == nil {
		return false
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 1 || sig.Results().Len() != 0 {
		return false
	}
	pointer, ok := types.Unalias(sig.Params().At(0).Type()).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(pointer.Elem()).(*types.Named)
	if !ok {
		return false
	}
	declared := named.Obj()
	return declared.Name() == param && declared.Pkg() != nil && declared.Pkg().Path() == testingPath
}

// isPattern reports whether a configured string carries either special character.
func isPattern(s string) bool {
	return strings.ContainsAny(s, "*?")
}

// matchRef reports whether one configured string names the symbol reference ref.
func matchRef(pattern, ref string) bool {
	if !isPattern(pattern) {
		return pattern == ref
	}
	return matchGlob([]rune(pattern), []rune(ref))
}

// matchGlob matches ref against a pattern whose only special characters are * and
// ?, counting a character as one code point. A star keeps the position it last
// stood at so the shortest run it can stand for is tried first.
func matchGlob(pattern, ref []rune) bool {
	next, star, resume := 0, -1, 0
	for at := 0; at < len(ref); {
		switch {
		case next < len(pattern) && (pattern[next] == '?' || pattern[next] == ref[at]):
			next++
			at++
		case next < len(pattern) && pattern[next] == '*':
			star, resume = next, at
			next++
		case star >= 0:
			resume++
			next, at = star+1, resume
		default:
			return false
		}
	}
	for next < len(pattern) && pattern[next] == '*' {
		next++
	}
	return next == len(pattern)
}

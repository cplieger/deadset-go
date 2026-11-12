package findings

import (
	"maps"
	"path"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/roots"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// pass is what every kind of one pass reads: the input and the facts derived
// from it, each computed once by [newPass] before any kind runs, so no kind's
// answer depends on which kind ran first.
type pass struct {
	*Input

	symbols    map[inventory.SymbolID]*inventory.Symbol
	candidates map[inventory.SymbolID]*liveness.Candidate
	components map[inventory.SymbolID]int // each member's component, by its place in the sweep's order
	retained   map[inventory.SymbolID]bool
	byRef      map[string]inventory.SymbolID
	mains      map[string]bool     // by [pass.packageKey]
	dirs       map[string][]string // each import path's target-relative package directories
	children   map[inventory.SymbolID][]inventory.SymbolID

	// unlinked is every package no build links in any configuration that loads it,
	// by [pass.packageKey], unanalysed every unit by its target-relative
	// directory, flagged where it has an unanalysed consumer, and exposed every type
	// the published API reaches: the facts the reachability class reads.
	unlinked   map[string]bool
	unanalysed map[string]bool
	exposed    map[inventory.SymbolID]bool

	// obscured is every target-relative package directory holding code the
	// analysis does not see ([load.Result.Obscured]).
	obscured map[string]bool

	deprecated map[inventory.SymbolID]bool
	enumerated map[inventory.SymbolID]string
	literals   map[string][]literalSite
	constTypes map[inventory.SymbolID]inventory.SymbolID
	twins      *twins
	carriers   map[inventory.SymbolID][]carrier // [readCarriers]
	satisfying map[inventory.SymbolID]bool      // [pass.readSatisfying]
	cascades   *cascades
	artifacts  *artifactFacts

	// seenLiterals is every literal site [readLiterals] kept, so a file two
	// configurations compile counts its literals once.
	seenLiterals map[inventory.Offset]bool
	interfaces   *interfaceFacts
	fixed        map[inventory.SymbolID]bool          // [readFixed]
	exempted     map[inventory.SymbolID]bool          // every declaration of Exempt
	unused       map[vocab.Code][]*liveness.Candidate // [unusedByCode]
	referenced   map[inventory.SymbolID]bool          // every declaration any reference of any file names
	counted      map[inventory.SymbolID]*uses
	writeOnly    map[inventory.SymbolID]*uses // the write-only kind's subjects
	falling      *annotationFalls
	narrowed     *narrowing
	intra        *intrafunc
	listed       map[int][]positioned        // each dead component's member listing, where the run lists them in full
	skipped      map[inventory.SymbolID]bool // every declaration a type error skipped

	// dialed is the dials' answer over the pass's findings, empty until [pass.dial]
	// gives it.
	dialed *dialed

	swept []liveness.Component

	// withdrawnMarks marks the pass over the sweep with every mark withdrawn
	// ([pass.withdrawn]), which reports a member of a dead container on its own.
	withdrawnMarks bool
}

// classesOf is the facts the reachability class reads, which need no sweep, read
// in one visit per configuration.
func classesOf(in *Input) (*pass, error) {
	a := &pass{
		Input:      in,
		symbols:    make(map[inventory.SymbolID]*inventory.Symbol),
		candidates: make(map[inventory.SymbolID]*liveness.Candidate),
		components: make(map[inventory.SymbolID]int),
		retained:   make(map[inventory.SymbolID]bool),
		byRef:      make(map[string]inventory.SymbolID),
		mains:      make(map[string]bool),
		dirs:       make(map[string][]string),
		children:   make(map[inventory.SymbolID][]inventory.SymbolID),
		unlinked:   make(map[string]bool),
		unanalysed: make(map[string]bool),
		exposed:    make(map[inventory.SymbolID]bool),
		obscured:   make(map[string]bool),
	}
	a.holdInventory(in.Merged)
	a.holdSweep(in.Sweep)
	var held packageFacts
	if err := each(in.Per, func(_ int, one *Configured) error {
		a.readPackages(one, &held)
		return nil
	}); err != nil {
		return nil, err
	}
	a.keyPackages(&held)
	return a, nil
}

// newPass computes every fact of one pass, each from the ones before it. The facts
// read from syntax and types are read in three visits per configuration, the
// packages ([classesOf]), the declarations ([pass.readDeclarations]) and the bodies
// ([pass.readBodies]), so a run whose loads memory pressure drops loads each
// configuration again three times rather than once per fact.
func newPass(in *Input) (*pass, error) {
	a, err := classesOf(in)
	if err != nil {
		return nil, err
	}
	a.deprecated = make(map[inventory.SymbolID]bool)
	a.enumerated = make(map[inventory.SymbolID]string)
	a.literals = make(map[string][]literalSite)
	a.seenLiterals = make(map[inventory.Offset]bool)
	a.constTypes = make(map[inventory.SymbolID]inventory.SymbolID)
	a.fixed = make(map[inventory.SymbolID]bool)
	a.twins = newTwins()
	a.carriers = make(map[inventory.SymbolID][]carrier)
	a.satisfying = make(map[inventory.SymbolID]bool)
	a.interfaces = newInterfaceFacts(a)
	if err := each(in.Per, a.readDeclarations); err != nil {
		return nil, err
	}
	sortLiterals(a.literals)
	a.interfaces.settle()
	a.exempted = make(map[inventory.SymbolID]bool, len(in.Exempt))
	for i := range in.Exempt {
		a.exempted[in.Exempt[i].ID] = true
	}
	if in.Merged != nil {
		a.referenced = referencedSymbols(a)
		a.counted = writesAndReads(a)
		a.writeOnly = writeOnlySubjects(a)
	}
	a.skipped = make(map[inventory.SymbolID]bool)
	for i := range in.TypeErrorSkips {
		for _, id := range in.TypeErrorSkips[i].Declarations {
			a.skipped[id] = true
		}
	}
	a.dialed = &dialed{}
	a.falling = writeFalls(a)
	a.unused = unusedByCode(a)
	a.narrowed = newNarrowing(a)
	a.intra = newIntrafunc(a)
	a.cascades = newCascades(a)
	a.artifacts = newArtifactFacts(a)
	if err := each(in.Per, a.readBodies); err != nil {
		return nil, err
	}
	a.intra.settle()
	if in.Config != nil && in.Config.Reporters.CascadeFull {
		a.listed = make(map[int][]positioned, len(a.swept))
		for at := range a.swept {
			a.listed[at] = a.listing(a.swept[at].Members)
		}
	}
	return a, nil
}

// readDeclarations reads what one configuration's declarations say: deprecation
// markers, enumerated types, name literals and constant types, interfaces and
// satisfaction assertions, methods an outside interface fixes, platform twins, and
// each type's carriers and implementors for the narrowing kinds.
func (in *pass) readDeclarations(_ int, one *Configured) error {
	files := sourcesOf(one)
	for _, typed := range files {
		markDeprecated(typed.file, typed.one.Resolve, in.deprecated)
	}
	maps.Copy(in.enumerated, exempt.EnumeratedMembers(one.Resolve, one.Facts))
	readLiterals(files, in.literals, in.constTypes, in.seenLiterals)
	in.interfaces.configuration(in, one, files)
	readFixed(one, in.fixed)
	in.twins.read(one, files)
	readCarriers(one, in.carriers)
	in.readSatisfying(one)
	return nil
}

// readBodies reads what one configuration's function bodies say, for the kinds that
// read them ([intrafunc]), the write cascades and the module artifacts.
func (in *pass) readBodies(config int, one *Configured) error {
	files := sourcesOf(one)
	if err := in.intra.read(config, one, files); err != nil {
		return err
	}
	in.cascades.read(one, files)
	in.artifacts.read(one)
	return nil
}

// component is the dead component one declaration is a member of with its place in
// the sweep's order, and nil where none holds it.
func (in *pass) component(id inventory.SymbolID) (c *liveness.Component, at int) {
	at, held := in.components[id]
	if !held {
		return nil, -1
	}
	return &in.swept[at], at
}

// holdInventory keys every declaration of the matrix inventory by its identifier,
// and by its reference for an edge or a part's subject, which names no identifier.
// Two declarations share one reference where one is blank or both are init
// functions, and the first of them answers for it.
func (in *pass) holdInventory(merged *liveness.Merged) {
	if merged == nil {
		return
	}
	for i := range merged.Symbols {
		symbol := &merged.Symbols[i]
		in.symbols[symbol.ID] = symbol
		for _, parent := range symbol.Parents {
			in.children[parent] = append(in.children[parent], symbol.ID)
		}
		if _, collides := in.byRef[symbol.Ref]; !collides {
			in.byRef[symbol.Ref] = symbol.ID
		}
	}
}

// holdSweep keys the sweep's answers by declaration: the candidates, the dead
// component each member belongs to, and the symbols an exemption held back.
func (in *pass) holdSweep(swept *liveness.Result) {
	if swept == nil {
		return
	}
	for i := range swept.Candidates {
		in.candidates[swept.Candidates[i].ID] = &swept.Candidates[i]
	}
	in.swept = swept.Components
	for i := range swept.Components {
		for _, member := range swept.Components[i].Members {
			in.components[member] = i
		}
	}
	for ix := range swept.Retained {
		exemption := &swept.Retained[ix]
		in.retained[exemption.ID] = true
	}
}

// packageAt is one package of one configuration by its import path and its
// target-relative directory, which [pass.packageKey] keys once every
// configuration's directories are known.
type packageAt struct {
	path, dir string
}

// packageFacts is what [pass.readPackages] reads of the configurations' packages
// before their keys exist: the main packages, the packages some build links, and
// the packages a configuration loads and links in no build.
type packageFacts struct {
	mains, linked, unlinked []packageAt
}

// readPackages reads one configuration's packages: the directories of each import
// path, its main packages, which nothing outside the module can import, which of
// its packages no build links, its units, flagged where it gives one an
// unanalysed consumer, the directories holding code it does not see, and the types
// its published API reaches ([facts.Published]).
func (in *pass) readPackages(one *Configured, held *packageFacts) {
	if one.Result != nil {
		in.readPackageDirs(one.Result, held)
		in.readUnits(one.Result)
	}
	if one.Facts != nil {
		for _, name := range one.Facts.Published().Names() {
			if id, inInventory := one.Resolve.Object(name); inInventory {
				in.exposed[id] = true
			}
		}
	}
}

// readUnits reads which of r's units have an unanalysed consumer and which
// directories hold code r does not see, each by its target-relative path.
func (in *pass) readUnits(r *load.Result) {
	root := scope.RootOf(in.Root)
	for _, unit := range r.Units {
		if dir, inside := root.Place(unit.Dir); inside {
			in.unanalysed[dir] = in.unanalysed[dir] || unit.UnanalysedConsumer
		}
	}
	for _, dir := range r.Obscured {
		if rel, inside := root.Place(dir); inside {
			in.obscured[rel] = true
		}
	}
}

// readPackageDirs reads where each package of one configuration's load is
// declared, whether it is a main package, and whether some build links it.
func (in *pass) readPackageDirs(r *load.Result, held *packageFacts) {
	none, root := roots.UnlinkedPackages(r), scope.RootOf(in.Root)
	for _, p := range r.Packages {
		at := packageAt{path: p.PkgPath, dir: packageDir(p, root)}
		if at.dir != "" && !slices.Contains(in.dirs[at.path], at.dir) {
			in.dirs[at.path] = append(in.dirs[at.path], at.dir)
		}
		if p.Name == mainPackageName {
			held.mains = append(held.mains, at)
		}
		switch {
		case p.ForTest != "":
		case none[r.ID(p)]:
			held.unlinked = append(held.unlinked, at)
		default:
			held.linked = append(held.linked, at)
		}
	}
}

// keyPackages keys what [pass.readPackages] read once every directory is known: a
// package is unlinked where no configuration that loads it links it in a build.
func (in *pass) keyPackages(held *packageFacts) {
	for _, at := range held.mains {
		in.mains[in.packageKey(at.path, at.dir)] = true
	}
	for _, at := range held.unlinked {
		in.unlinked[in.packageKey(at.path, at.dir)] = true
	}
	for _, at := range held.linked {
		delete(in.unlinked, in.packageKey(at.path, at.dir))
	}
}

// packageDir is the directory of p's first file relative to root where both
// resolve, with forward slashes, as a symbol's File names its file, and empty
// where p has no file or the root does not hold it.
func packageDir(p *packages.Package, root scope.Root) string {
	if len(p.GoFiles) == 0 {
		return ""
	}
	rel, inside := root.Place(filepath.Dir(p.GoFiles[0]))
	if !inside {
		return ""
	}
	return rel
}

// packageKey names one package of the target: its import path, and where two
// modules of the target declare that path, its directory too.
func (in *pass) packageKey(pkgPath, dir string) string {
	if len(in.dirs[pkgPath]) <= 1 {
		return pkgPath
	}
	return pkgPath + "\x00" + dir
}

// packageOf is the [pass.packageKey] of the package declaring symbol.
func (in *pass) packageOf(symbol *inventory.Symbol) string {
	return in.packageKey(symbol.PkgPath, path.Dir(symbol.File))
}

// symbol is the declaration of the inventory one identifier names, or nil where the
// inventory holds none.
func (in *pass) symbol(id inventory.SymbolID) *inventory.Symbol {
	return in.symbols[id]
}

// container is the declaration one declaration is declared in, or nil for a
// package and for a declaration with several: only a method has several, one
// receiver type per configuration that declares the type in a file of its own.
func (in *pass) container(symbol *inventory.Symbol) *inventory.Symbol {
	if len(symbol.Parents) != 1 {
		return nil
	}
	return in.symbols[symbol.Parents[0]]
}

// candidateOf is the sweep's candidate for one declaration, or nil where the sweep
// judged the declaration live.
func (in *pass) candidateOf(id inventory.SymbolID) *liveness.Candidate {
	return in.candidates[id]
}

// skips reports whether a type error skipped the declaration one finding's subject
// is or is a part of ([pass.skippedWithin]): nothing is reported about such a
// declaration. A finding about a row of a document is about the row, whatever
// declaration the row names, so a refused entry naming one is still reported.
func (in *pass) skips(found *Finding) bool {
	if len(in.skipped) == 0 || aboutRow(found) {
		return false
	}
	id := found.id
	if id == "" {
		id = found.decl
	}
	return in.skippedWithin(id)
}

// skippedWithin reports whether a type error skipped one declaration or the
// declaration it is declared inside. A method is a declaration of its own.
func (in *pass) skippedWithin(id inventory.SymbolID) bool {
	for symbol := in.symbols[id]; symbol != nil; symbol = in.container(symbol) {
		if in.skipped[symbol.ID] {
			return true
		}
		switch symbol.Kind {
		case inventory.KindField, inventory.KindInterfaceMethod, inventory.KindTypeParam:
		default:
			return false
		}
	}
	return false
}

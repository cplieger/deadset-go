package findings

import (
	"path"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/liveness"
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

	deprecated map[inventory.SymbolID]bool
	enumerated map[inventory.SymbolID]string
	literals   map[string][]literalSite
	constTypes map[inventory.SymbolID]inventory.SymbolID
	interfaces *interfaceFacts
	fixed      map[inventory.SymbolID]bool          // [pass.fixedByOutsideInterface]
	exempted   map[inventory.SymbolID]bool          // every declaration of Exempt
	unused     map[vocab.Code][]*liveness.Candidate // [unusedByCode]
	referenced map[inventory.SymbolID]bool          // every declaration any reference of any file names
	counted    map[inventory.SymbolID]*uses
	writeOnly  map[inventory.SymbolID]*uses // the write-only kind's subjects
	falling    *annotationFalls
	narrowed   *narrowing
	intra      *intrafunc
	listed     map[int][]positioned        // each dead component's member listing, where the run lists them in full
	skipped    map[inventory.SymbolID]bool // every declaration a type error skipped

	// dialed is the dials' answer over the pass's findings, empty until [pass.dial]
	// gives it.
	dialed *dialed

	swept   []liveness.Component
	sources []typedFile // [sourcesOf]

	// withdrawnMarks marks the pass over the sweep with every mark withdrawn
	// ([pass.withdrawn]), which reports a member of a dead container on its own.
	withdrawnMarks bool
}

// classesOf is the facts the reachability class reads, which need no sweep.
func classesOf(in *Input) *pass {
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
	}
	a.sources = sourcesOf(in)
	a.holdInventory(in.Merged)
	a.holdSweep(in.Sweep)
	a.holdPackages(in.Per, in.Root)
	a.unlinked = unlinkedPackages(in.Per, in.Root, a)
	a.unanalysed = unanalysedUnits(in.Per, in.Root)
	a.exposed = exposedTypes(a)
	return a
}

// newPass computes every fact of one pass, each from the ones before it.
func newPass(in *Input) *pass {
	a := classesOf(in)
	a.deprecated = deprecations(a)
	a.enumerated = enumGroupMembers(a)
	a.literals, a.constTypes = nameLiterals(a)
	a.interfaces = interfacesOf(a)
	a.fixed = fixedByOutsideInterface(a)
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
	if in.Config != nil && in.Config.Reporters.CascadeFull {
		a.listed = make(map[int][]positioned, len(a.swept))
		for at := range a.swept {
			a.listed[at] = a.listing(a.swept[at].Members)
		}
	}
	return a
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

// holdPackages records the directories of each import path, and which packages
// are main packages, which nothing outside the module can import.
func (in *pass) holdPackages(per []Configured, root string) {
	eachPackage(per, func(p *packages.Package) {
		if dir := packageDir(p, root); dir != "" && !slices.Contains(in.dirs[p.PkgPath], dir) {
			in.dirs[p.PkgPath] = append(in.dirs[p.PkgPath], dir)
		}
	})
	eachPackage(per, func(p *packages.Package) {
		if p.Name == mainPackageName {
			in.mains[in.packageKey(p.PkgPath, packageDir(p, root))] = true
		}
	})
}

// eachPackage calls visit for every target package of every configuration.
func eachPackage(per []Configured, visit func(*packages.Package)) {
	for _, one := range per {
		if one.Result != nil {
			for _, p := range one.Result.Packages {
				visit(p)
			}
		}
	}
}

// packageDir is the directory of p's first file relative to root, with forward
// slashes, as a symbol's File names its file.
func packageDir(p *packages.Package, root string) string {
	if len(p.GoFiles) == 0 {
		return ""
	}
	rel, err := filepath.Rel(root, filepath.Dir(p.GoFiles[0]))
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
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

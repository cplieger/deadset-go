package exempt

import (
	"cmp"
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/tools/go/packages"
)

// EnumGroupDetector retains every constant of the target whose deletion, alone or
// with other constants the class does not retain, the analysis does not prove
// leaves a program that builds and does the same (the Contract's enum-group
// class). It decides over the production files and again over every file.
func EnumGroupDetector(in *Program) ([]Exemption, error) {
	in.locals, in.arriving = nil, nil
	s := newEnumGroupScan(in)
	if len(s.consts) == 0 {
		return nil, nil
	}
	s.walkUses()
	production := s.view(false)
	full := s.view(true)
	in.arriving = s.arrivingMembers()
	return s.exemptions(production, full)
}

// EnumeratedMembers is every member of an enumerated type one configuration
// declares, mapped to the name of its type: the class's recognition, which the
// kinds read rather than recompute.
func EnumeratedMembers(rs *resolve.Resolver, x *facts.Index) map[inventory.SymbolID]string {
	found := make(map[inventory.SymbolID]string)
	if x == nil {
		return found // a configuration with no index declares no type
	}
	for _, enum := range recognizeEnums(rs, x).byKey {
		for _, id := range enum.members {
			found[id] = enum.name
		}
	}
	return found
}

// enumRecognition is the enumerated types of one configuration: each defined
// package-level type the target declares with constants in an iota group, and
// every non-blank constant of it any top-level block of the target declares.
type enumRecognition struct {
	byKey map[string]*egEnum
	keys  []string // in the order the walk registered them
}

// recognizeEnums reads the enumerated types of one configuration: the iota groups
// first, which register the types, then every other block, which only adds
// members to them.
func recognizeEnums(rs *resolve.Resolver, x *facts.Index) *enumRecognition {
	r := &enumRecognition{byKey: make(map[string]*egEnum)}
	for _, iotaGroups := range []bool{true, false} {
		for _, p := range x.Packages() {
			if p.TypesInfo == nil {
				continue
			}
			for _, f := range x.Files(p) {
				r.file(rs, p.TypesInfo, f, iotaGroups)
			}
		}
	}
	return r
}

// file reads every constant block of one file that is an iota group exactly
// where iotaGroups is set.
func (r *enumRecognition) file(rs *resolve.Resolver, info *types.Info, f *ast.File, iotaGroups bool) {
	for _, decl := range f.Decls {
		block, ok := decl.(*ast.GenDecl)
		if !ok || block.Tok != token.CONST || namesIota(info, block) != iotaGroups {
			continue
		}
		r.block(rs, info, block, iotaGroups)
	}
}

// block keeps every constant of one block whose type is an enumerated type, and
// registers the types an iota group declares constants of.
func (r *enumRecognition) block(rs *resolve.Resolver, info *types.Info, block *ast.GenDecl, register bool) {
	for _, spec := range block.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range value.Names {
			if c, isConst := info.Defs[name].(*types.Const); isConst && name.Name != "_" {
				r.member(rs, c, register)
			}
		}
	}
}

// member keeps one constant as a member of its type where that is an enumerated
// type.
func (r *enumRecognition) member(rs *resolve.Resolver, c *types.Const, register bool) {
	enum := r.enumOf(rs, c, register)
	if enum == nil {
		return
	}
	if id, held := rs.Object(c); held && !slices.Contains(enum.members, id) {
		enum.members = append(enum.members, id)
	}
}

// enumOf is the enumerated type a constant's type is, registering it where
// register is set: nil for a type that is no package-level defined type of the
// target, or one no iota group registered.
func (r *enumRecognition) enumOf(rs *resolve.Resolver, c *types.Const, register bool) *egEnum {
	named, isNamed := types.Unalias(c.Type()).(*types.Named)
	if !isNamed || !packageLevel(named.Obj()) || !rs.Inside(named.Obj().Pos()) {
		return nil
	}
	key := rs.TypeKey(named)
	if enum := r.byKey[key]; enum != nil || !register {
		return enum
	}
	basic, _ := named.Underlying().(*types.Basic)
	enum := &egEnum{
		name: named.Obj().Name(), index: len(r.keys),
		integer: basic != nil && basic.Info()&types.IsInteger != 0,
	}
	r.byKey[key] = enum
	r.keys = append(r.keys, key)
	return enum
}

// packageLevel reports whether a type is declared at the level of its package.
func packageLevel(obj types.Object) bool {
	return obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope()
}

// namesIota reports whether one constant block is an iota group: one of its value
// expressions names the predeclared iota.
func namesIota(info *types.Info, block *ast.GenDecl) bool {
	for _, spec := range block.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, expr := range value.Values {
			if exprNamesIota(info, expr) {
				return true
			}
		}
	}
	return false
}

// exprNamesIota reports whether an expression names the predeclared iota.
func exprNamesIota(info *types.Info, expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && isIota(info, id) {
			found = true
		}
		return !found
	})
	return found
}

// isIota reports whether an identifier denotes the predeclared iota.
func isIota(info *types.Info, id *ast.Ident) bool {
	c, ok := info.Uses[id].(*types.Const)
	return ok && c.Pkg() == nil && c.Name() == "iota"
}

// egEnum is one enumerated type and what the class learns about its values.
type egEnum struct {
	// The first use the list of uses does not name, in a production file and in
	// a test file, and the first exported declaration of an importable package
	// whose type holds it in a library.
	useProduction, useTest, exported *egSite

	name    string
	members []inventory.SymbolID
	items   []egItem // the constant expressions of the type the walk met
	index   int
	integer bool
}

// arrives reports whether, in one view, a value of the type may arise other than
// from a member's name, a constant or the zero value, or be held by code outside
// the program.
func (e *egEnum) arrives(full bool) bool {
	return e.exported != nil || e.useProduction != nil || full && e.useTest != nil
}

// egSite is one piece of evidence: where it is written, whether a test file writes
// it, and the clause it records. A site the walk found is rendered when an
// exemption records it; a reference's is already rendered.
type egSite struct {
	rendered *Exemption
	detail   string
	at       inventory.Offset
	pos      token.Pos
	test     bool
}

// before orders two sites by the file and byte they are written at.
func (a *egSite) before(b *egSite) bool {
	return cmp.Or(scope.Order(a.at.File, b.at.File), cmp.Compare(a.at.At, b.at.At)) < 0
}

// earliest is the earliest of the sites given, preferring a test file's where
// test is set and a production file's otherwise, and any where none of that kind
// is given.
func earliest(sites []*egSite, test bool) *egSite {
	var best, first *egSite
	for _, s := range sites {
		if s == nil {
			continue
		}
		if first == nil || s.before(first) {
			first = s
		}
		if s.test == test && (best == nil || s.before(best)) {
			best = s
		}
	}
	if best != nil {
		return best
	}
	return first
}

// egRetained is why one view retains one constant.
type egRetained struct {
	detail string
	sites  []*egSite
}

// enumGroupScan is the class over one configuration.
type enumGroupScan struct {
	in         *Program
	enums      *enumRecognition
	byID       map[inventory.SymbolID]*egConst
	locals     map[inventory.Offset]*egConst // the constants function bodies declare
	holds      *enumHolds
	named      map[inventory.SymbolID]*egNaming
	localNamed map[*egConst]*egNaming

	constrained map[string]bool // a file to whether a build constraint selects it
	fixed       map[*egConst]int8

	consts []*egConst // in position order
	specs  []*egSpec
}

// egNaming is the references that name one constant, by view.
type egNaming struct {
	production, test []*egSite
}

// newEnumGroupScan reads the constants of the target and the references that name
// them.
func newEnumGroupScan(in *Program) *enumGroupScan {
	s := &enumGroupScan{
		in:          in,
		enums:       recognizeEnums(in.Resolve, in.Facts),
		byID:        make(map[inventory.SymbolID]*egConst),
		locals:      make(map[inventory.Offset]*egConst),
		named:       make(map[inventory.SymbolID]*egNaming),
		localNamed:  make(map[*egConst]*egNaming),
		constrained: make(map[string]bool),
		fixed:       make(map[*egConst]int8),
	}
	s.holds = newEnumHolds(in.Resolve, s.enums)
	s.readBlocks()
	s.readReferences()
	s.readLocalUses()
	return s
}

// readLocalUses reads which identifiers name each constant a function body
// declares: such a constant is no symbol of the inventory, so no reference of the
// matrix names it, and every use is in the file declaring it.
func (s *enumGroupScan) readLocalUses() {
	if len(s.locals) == 0 {
		return
	}
	seen := make(map[inventory.Offset]bool)
	for _, p := range s.in.Facts.Packages() {
		if p.TypesInfo == nil {
			continue
		}
		for id, obj := range p.TypesInfo.Uses {
			c := s.locals[s.in.offsetOf(obj.Pos())]
			at := s.in.offsetOf(id.Pos())
			if c == nil || seen[at] {
				continue
			}
			seen[at] = true
			naming(s.localNamed, c).add(s.siteAt(id.Pos(), ""))
		}
	}
}

// readReferences reads which references of the matrix name each constant, and
// where, under the run's mode.
func (s *enumGroupScan) readReferences() {
	for i := range s.in.References {
		r := &s.in.References[i]
		if _, held := s.byID[r.To]; !held {
			continue
		}
		naming(s.named, r.To).add(s.referenceSite(r))
	}
}

// naming is the naming of one key of a table, added on first ask.
func naming[K comparable](table map[K]*egNaming, key K) *egNaming {
	n := table[key]
	if n == nil {
		n = &egNaming{}
		table[key] = n
	}
	return n
}

// add records one site in the view its file belongs to.
func (n *egNaming) add(site *egSite) {
	if site.test {
		n.test = append(n.test, site)
	} else {
		n.production = append(n.production, site)
	}
}

// referenceSite is the evidence one reference is.
func (s *enumGroupScan) referenceSite(r *facts.Reference) *egSite {
	file := s.in.Resolve.Positions().Relative(r.Site.File)
	if file == "" {
		file = r.Pos.Filename
	}
	return &egSite{
		at:     r.Site,
		test:   s.in.Mode.TestReference(r),
		detail: "",
		rendered: &Exemption{
			Consumer: r.Consumer,
			Site:     r.Pos,
			File:     file,
			Build:    r.Build,
		},
	}
}

// isNamed reports whether a reference one view counts names a constant, or the
// constant is an exported constant of an importable package of a library, and
// returns the sites that name it in that view.
func (s *enumGroupScan) isNamed(c *egConst, full bool) (bool, []*egSite) {
	if c.exported {
		return true, []*egSite{s.declSite(c, "exported from an importable package")}
	}
	n := s.named[c.id]
	if c.local {
		n = s.localNamed[c]
	}
	if n == nil {
		return false, nil
	}
	if full {
		return len(n.production)+len(n.test) > 0, append(slices.Clone(n.production), n.test...)
	}
	return len(n.production) > 0, n.production
}

// namedByReference reports whether a reference one view counts names a constant.
func (s *enumGroupScan) namedByReference(c *egConst, full bool) bool {
	n := s.named[c.id]
	if c.local {
		n = s.localNamed[c]
	}
	return n != nil && (len(n.production) > 0 || full && len(n.test) > 0)
}

// declSite is the site of one constant's declaration.
func (s *enumGroupScan) declSite(c *egConst, detail string) *egSite {
	return s.siteAt(c.pos(), detail)
}

// siteAt is the site of one position of this configuration's syntax.
func (s *enumGroupScan) siteAt(pos token.Pos, detail string) *egSite {
	return &egSite{
		at:     s.in.offsetOf(pos),
		pos:    pos,
		test:   s.testCode(pos),
		detail: detail,
	}
}

// testCode reports whether the run's mode counts the file writing pos as test code.
func (s *enumGroupScan) testCode(pos token.Pos) bool {
	build := s.in.Resolve.Build(pos)
	return s.in.Mode.TestCode(build, build.TestCode(s.in.Resolve.Positions().InTestFile(pos)))
}

// view runs the fixed point over one view: the production files, or every file.
func (s *enumGroupScan) view(full bool) map[*egConst]*egRetained {
	retained, candidate := s.candidates(full)
	for {
		round := s.round(candidate, full)
		if len(round) == 0 {
			return retained
		}
		for _, c := range s.consts {
			if r, held := round[c]; held {
				retained[c] = r
				delete(candidate, c)
			}
		}
	}
}

// candidates is the start of one view's fixed point: the constants rule D
// retains, and the constants left to judge, which are every other one of the
// program that no reference of the view names, is not blank and is not declared
// beside a non-Go source.
func (s *enumGroupScan) candidates(full bool) (retained map[*egConst]*egRetained, candidate map[*egConst]bool) {
	retained = make(map[*egConst]*egRetained)
	candidate = make(map[*egConst]bool)
	for _, c := range s.consts {
		if c.blank || c.id == "" && !c.local || c.nonGo {
			continue
		}
		// An exported constant of a library counts as named only as a constant
		// another deletion moves: its own deletion is judged, since the values it
		// renumbers may be held by an importer.
		if s.namedByReference(c, full) {
			continue
		}
		if c.spec.unsplittable {
			retained[c] = &egRetained{
				detail: "deleting it alone leaves its block unable to compile",
				sites:  []*egSite{s.declSite(c, "")},
			}
			continue
		}
		candidate[c] = true
	}
	return retained, candidate
}

// egRound is one round of the fixed point: the candidates left, rule Moved over
// them, what the round retains, and the enumerated types left to rule M2.
type egRound struct {
	candidate map[*egConst]bool
	m         *egMovers
	found     map[*egConst]*egRetained
	compared  []*egEnum
	full      bool
}

// retain retains every candidate of every specification of movers not yet
// retained this round, for one reason.
func (r *egRound) retain(movers egBits, detail string, sites ...*egSite) {
	for _, spec := range r.m.removable.members(movers) {
		for _, c := range spec.consts {
			if r.candidate[c] && r.found[c] == nil {
				r.found[c] = &egRetained{detail: detail, sites: sites}
			}
		}
	}
}

// round applies rules W, M0 and M2 once over the candidates left, and returns the
// candidates they retain.
func (s *enumGroupScan) round(candidate map[*egConst]bool, full bool) map[*egConst]*egRetained {
	r := &egRound{candidate: candidate, m: s.movers(candidate), found: make(map[*egConst]*egRetained), full: full}
	for _, c := range s.consts {
		s.judgeMoved(r, c)
	}
	for _, enum := range r.compared {
		s.compare(enum, r)
	}
	return r.found
}

// judgeMoved applies rules W and M0 to one constant the round's removals may
// move, and leaves its type to rule M2 where neither decides.
func (s *enumGroupScan) judgeMoved(r *egRound, c *egConst) {
	movers := r.m.of(c)
	if movers.empty() {
		return
	}
	if !s.stillBuilds(c, r.m) {
		r.retain(movers, "deleting it moves "+c.name+", which may then not compile", s.declSite(c, ""))
		return
	}
	named, sites := s.isNamed(c, r.full)
	switch {
	case !named:
	case c.enum == nil || !c.enum.integer:
		r.retain(movers, "deleting it renumbers "+c.name+", which code names", sites...)
	case c.enum.arrives(r.full):
		use := []*egSite{c.enum.exported, c.enum.useProduction}
		if r.full {
			use = append(use, c.enum.useTest)
		}
		detail := "deleting it renumbers " + c.name + ", and a value of " + c.enum.name + " " + earliest(use, false).detail
		r.retain(movers, detail, append(use, sites...)...)
	case !slices.Contains(r.compared, c.enum):
		r.compared = append(r.compared, c.enum)
	}
}

// arrivingMembers is every member of a type whose values may arise other than
// by a member's name in the run's mode, which a finding about a member says.
func (s *enumGroupScan) arrivingMembers() []inventory.SymbolID {
	var found []inventory.SymbolID
	for _, key := range s.enums.keys {
		enum := s.enums.byKey[key]
		if enum.arrives(!s.in.Mode.Production) {
			found = append(found, enum.members...)
		}
	}
	return found
}

// exemptions renders what the two views retain: a constant the production view
// retains at the earliest production site of its evidence, and one only the full
// view retains at the earliest test file's.
func (s *enumGroupScan) exemptions(production, full map[*egConst]*egRetained) ([]Exemption, error) {
	var found []Exemption
	for _, c := range s.consts {
		r, test := production[c], false
		if r == nil {
			r, test = full[c], true
		}
		if r == nil {
			continue
		}
		site := earliest(r.sites, test)
		e, err := s.render(site)
		if err != nil {
			return nil, err
		}
		e.Class, e.Detail = string(vocab.EnumGroup), r.detail
		if c.local {
			if err := s.local(c, &e); err != nil {
				return nil, err
			}
			continue
		}
		e.ID = c.id
		found = append(found, e)
	}
	return found, nil
}

// local keeps the retention of one constant a function body declares, where the
// run's mode holds its evidence.
func (s *enumGroupScan) local(c *egConst, e *Exemption) error {
	if !holdsInMode(e, s.in.Mode) {
		return nil
	}
	holder, held := s.in.Resolve.Holder(c.at)
	if !held {
		return nil
	}
	at, err := s.in.Resolve.Render(c.at)
	if err != nil {
		return err
	}
	e.ID = holder
	s.in.locals = append(s.in.locals, RetainedLocal{Exemption: *e, Declared: s.in.offsetOf(c.at), At: at})
	return nil
}

// render is the exemption one site records, without its symbol.
func (s *enumGroupScan) render(site *egSite) (Exemption, error) {
	if site.rendered != nil {
		return *site.rendered, nil
	}
	consumer, at, err := s.in.Resolve.Site(site.pos)
	if err != nil {
		return Exemption{}, err
	}
	return Exemption{Consumer: consumer, Site: at, File: s.in.Resolve.File(site.pos), Build: s.in.Resolve.Build(site.pos)}, nil
}

// published reports whether one package variant of the target is an importable
// package of a library, whose exported declarations code outside the program can use.
func (s *enumGroupScan) published(p *packages.Package) bool {
	return s.in.Options.PublishedAPI && load.Published(p.PkgPath, p.Name == mainPackage)
}

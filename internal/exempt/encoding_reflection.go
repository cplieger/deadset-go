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
// keeps the members no reference points at. Sorted, and searched as a set.
var destinationPackages = []string{
	"encoding/gob",
	"encoding/json",
	"encoding/xml",
	"html/template",
	"reflect",
	"text/template",
}

// destinationInterfaces are the interfaces a conversion into which reaches the
// class, each named by its package path and its name.
var destinationInterfaces = [...]struct{ pkg, name string }{
	{"log/slog", "LogValuer"},
	{"sort", "Interface"},
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
// target, a sort interface or a log-value interface keeps its exported methods
// and every field of it that carries a struct tag, because that consumer names
// them by string and the reference graph holds no edge to them.
//
// Three rules the class applies where the case is not spelled out. Every argument
// of a function or method of a destination package flows, the writer of a
// template execution included, because what is named is the argument position and
// not the parameter's meaning. A value reaches through a pointer, a slice, an
// array, a map, a channel and a type argument, and the reach stops at each
// defined type it finds: the members of that type are retained, and the members
// of the types those members are built from are not. A method is retained when
// the defined type declares it, so a method promoted from an embedded type is
// retained only where the embedded type flows in as well.
func EncodingReflectionDetector(in *Input) ([]graph.Exemption, error) {
	f := &encodingFlow{
		kept:    newRetention(in, EncodingReflection),
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
	targets []interfaceTarget
}

// interfaceTarget is one interface the class treats as a destination, resolved
// from the loaded program so a conversion is matched on the declared type rather
// than on a method-set shape a local interface could imitate.
type interfaceTarget struct {
	iface *types.Interface
	name  string
}

// destination is what one call is to the class: the clause an exemption records,
// and whether only a pointer argument flows into it.
type destination struct {
	detail   string
	pointers bool
}

// walkCalls records every value a call hands to a destination package.
func (f *encodingFlow) walkCalls() error {
	for _, p := range targetPackages(f.kept.in.Result.Packages) {
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
		}
		return failed == nil
	})
	return failed
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
		if err := f.retain(at, arg.Pos(), d.detail); err != nil {
			return err
		}
	}
	return nil
}

// walkConversions records every value converted to a destination interface. The
// conversion set is read rather than walked a second time, so a value reaching
// sort.Interface as an argument, as an assignment or as a satisfaction assertion
// is recorded by one rule.
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
		if err := f.retain(c.From, c.Site, "converted to "+name); err != nil {
			return err
		}
	}
	return nil
}

// retain records what every defined type a value of t carries keeps: the exported
// methods the type declares, and every field of it that carries a struct tag,
// whether that field is exported or not, because what is named is the tag and not
// the visibility.
func (f *encodingFlow) retain(t types.Type, at token.Pos, detail string) error {
	site, err := f.kept.site(at)
	if err != nil {
		return err
	}
	for _, named := range namedTypesReached(t) {
		origin := named.Origin()
		for m := range origin.Methods() {
			if m.Exported() {
				f.kept.record(m, site, detail)
			}
		}
		st, isStruct := origin.Underlying().(*types.Struct)
		if !isStruct {
			continue
		}
		for i := range st.NumFields() {
			if st.Tag(i) != "" {
				f.kept.record(st.Field(i), site, detail)
			}
		}
	}
	return nil
}

// encodingDestination reports whether a call to fn reaches the class, and how.
func encodingDestination(fn *types.Func) (destination, bool) {
	pkg := fn.Pkg()
	if pkg == nil {
		return destination{}, false
	}
	if _, found := slices.BinarySearch(destinationPackages, pkg.Path()); found {
		return destination{detail: "passed to " + fn.FullName()}, true
	}
	if pkg.Path() == sqlPackage && fn.Name() == sqlScanMethod && scansARow(fn) {
		return destination{detail: "scanned by " + fn.FullName(), pointers: true}, true
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
// reaching through a pointer, a slice, an array, a map, a channel and a type
// argument, and stopping at each defined type it finds.
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
			for arg := range u.TypeArgs().Types() {
				stack = append(stack, arg)
			}
		case *types.Pointer:
			stack = append(stack, u.Elem())
		case *types.Slice:
			stack = append(stack, u.Elem())
		case *types.Array:
			stack = append(stack, u.Elem())
		case *types.Map:
			stack = append(stack, u.Key(), u.Elem())
		case *types.Chan:
			stack = append(stack, u.Elem())
		}
	}
	return found
}

// targetPackages returns the loaded packages that carry both type information and
// syntax, in one order, so two runs over one load walk the same files in the same
// sequence.
func targetPackages(pkgs []*packages.Package) []*packages.Package {
	ordered := make([]*packages.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if p.TypesInfo != nil && len(p.Syntax) > 0 {
			ordered = append(ordered, p)
		}
	}
	slices.SortFunc(ordered, func(a, b *packages.Package) int { return strings.Compare(a.ID, b.ID) })
	return ordered
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

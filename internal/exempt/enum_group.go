package exempt

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/cplieger/deadset-go/internal/graph"
	"golang.org/x/tools/go/packages"
)

// The conversion methods whose presence on an enumerated type means a value of it
// can arrive spelled as text or as a number rather than as a member's name, in the
// Contract's order. The first one a type declares is the evidence recorded.
var enumGroupConversionMethods = [...]string{
	"String", "MarshalText", "UnmarshalText", "MarshalJSON", "UnmarshalJSON",
}

// The decoders whose target is a decoded wire value. A value reaching one of these
// arrives from outside the program, so every member of an enumerated type the
// target's type carries is reachable without being named.
var enumGroupDecoders = [...]string{
	"encoding/json.Unmarshal",
	"encoding/json.Decoder.Decode",
	"encoding/xml.Unmarshal",
	"encoding/xml.Decoder.Decode",
	"database/sql.Row.Scan",
	"database/sql.Rows.Scan",
}

// The three rules of the class, ranked: the evidence of the lowest rank wins, and
// the earliest rendered position wins inside one rank, so one type records one
// reason whatever order the load parsed its files in.
const (
	enumGroupByMethod = iota
	enumGroupByInteger
	enumGroupByDecode
)

// EnumGroupDetector retains every member of an enumerated type whose values can
// arrive by conversion rather than by name, so that a member reached only by value
// is never reported.
//
// The enumerated type is a defined type whose constants are declared in an iota
// group: a const block in which at least one specification's value expression
// mentions iota, the specifications that repeat the previous expression included,
// grouped by the defined type the constants carry. The class fires when the type
// declares a String, MarshalText, UnmarshalText, MarshalJSON or UnmarshalJSON
// method, when a value of it is produced by a conversion whose operand is an
// integer that is not a constant of the type, or when a value of it is the target
// of a decoder. Every constant of a type that fires is retained, including one
// declared outside the iota group, because a value that arrives by conversion can
// equal any of them.
//
// Three cases the mechanism leaves open are decided here, each as the rule it is.
// A conversion method is matched by name alone, because the mechanism names the five
// methods and a type whose String method carries another signature still spells a
// value as text somewhere. A decoder's target carries an enumerated type when one
// is reachable from the target's type through pointers, elements, map keys and
// values and struct fields, because decoding a container writes every field it
// holds. And a constant declared with the blank identifier names no member and is
// retained by nothing.
func EnumGroupDetector(in *Input) ([]graph.Exemption, error) {
	scan := &enumGroupScan{in: in, byKey: make(map[string]*enumGroupType)}
	if err := scan.groups(); err != nil {
		return nil, err
	}
	if err := scan.evidence(); err != nil {
		return nil, err
	}
	return scan.exemptions(), nil
}

// enumGroupScan accumulates the enumerated types of one configuration and the
// evidence that a value of each can arrive by conversion.
type enumGroupScan struct {
	in    *Input
	byKey map[string]*enumGroupType
	keys  []string
}

// enumGroupType is one defined type whose constants an iota group declares.
type enumGroupType struct {
	best    *enumGroupEvidence
	held    map[graph.SymbolID]struct{}
	members []enumGroupMember
	named   []*types.Named // the type as each package variant type-checked it
}

// enumGroupMember is one constant of an enumerated type, at the rendered position
// of its own declaration.
type enumGroupMember struct {
	id graph.SymbolID
	at token.Position
}

// enumGroupEvidence is why one enumerated type fires: the rule, the rendered
// position a maintainer reads, and the clause an exemption records.
type enumGroupEvidence struct {
	detail string
	at     token.Position
	rank   int
}

// groups collects every defined type whose constants an iota group declares, and
// then every constant of each such type whichever block declares it.
func (s *enumGroupScan) groups() error {
	for _, iotaGroups := range []bool{true, false} {
		if err := s.each(func(p *packages.Package, f *ast.File) error {
			for _, decl := range f.Decls {
				group, ok := decl.(*ast.GenDecl)
				if !ok || group.Tok != token.CONST || s.mentionsIota(group) != iotaGroups {
					continue
				}
				if err := s.constants(p, group); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// mentionsIota reports whether one const block is an iota group, which is one
// specification naming iota anywhere in a value expression. A specification that
// carries no expression repeats the previous one, so a block with one such
// specification is a group throughout.
func (s *enumGroupScan) mentionsIota(group *ast.GenDecl) bool {
	found := false
	for _, spec := range group.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, expr := range value.Values {
			ast.Inspect(expr, func(n ast.Node) bool {
				if id, isIdent := n.(*ast.Ident); isIdent && id.Name == "iota" {
					found = true
				}
				return !found
			})
		}
	}
	return found
}

// constants keeps every constant of one block whose type is a defined type the
// scan already holds, and registers a type the block declares under an iota group.
func (s *enumGroupScan) constants(p *packages.Package, group *ast.GenDecl) error {
	register := s.mentionsIota(group)
	for _, spec := range group.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range value.Names {
			if err := s.constant(p, name, register); err != nil {
				return err
			}
		}
	}
	return nil
}

// constant keeps one declared constant under the defined type it carries, and
// registers that type when the block declaring it is an iota group. A constant
// declared with the blank identifier names no member.
func (s *enumGroupScan) constant(p *packages.Package, name *ast.Ident, register bool) error {
	constant, ok := p.TypesInfo.Defs[name].(*types.Const)
	if !ok || name.Name == "_" {
		return nil
	}
	named, ok := constant.Type().(*types.Named)
	if !ok {
		return nil
	}
	key := types.TypeString(named, nil)
	enum := s.byKey[key]
	if enum == nil {
		if !register {
			return nil
		}
		enum = &enumGroupType{held: make(map[graph.SymbolID]struct{})}
		s.byKey[key] = enum
		s.keys = append(s.keys, key)
	}
	if !slices.Contains(enum.named, named) {
		enum.named = append(enum.named, named)
	}
	return s.member(enum, constant)
}

// member keeps one constant of an enumerated type, once per declaration site.
func (s *enumGroupScan) member(enum *enumGroupType, constant *types.Const) error {
	id, held := s.in.Resolve.Object(constant)
	if !held {
		return nil
	}
	if _, already := enum.held[id]; already {
		return nil
	}
	at, err := s.site(constant.Pos())
	if err != nil {
		return err
	}
	enum.held[id] = struct{}{}
	enum.members = append(enum.members, enumGroupMember{id: id, at: at})
	return nil
}

// evidence records, for every enumerated type, why a value of it can arrive by
// conversion: a conversion method it declares, a conversion from an integer, or a
// decoder that writes one.
func (s *enumGroupScan) evidence() error {
	for _, key := range s.keys {
		if err := s.method(s.byKey[key]); err != nil {
			return err
		}
	}
	return s.each(func(p *packages.Package, f *ast.File) error {
		var failed error
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || failed != nil {
				return failed == nil
			}
			if err := s.integerConversion(p, call); err != nil {
				failed = err
				return false
			}
			if err := s.decoded(p, call); err != nil {
				failed = err
				return false
			}
			return true
		})
		return failed
	})
}

// method records the first conversion method one enumerated type declares, in the
// Contract's order of the five names and then at the earliest position, whichever
// package variant declares it.
func (s *enumGroupScan) method(enum *enumGroupType) error {
	for _, name := range enumGroupConversionMethods {
		for _, named := range enum.named {
			for declared := range named.Methods() {
				if declared.Name() != name {
					continue
				}
				if err := s.record(enum, enumGroupByMethod, declared.Pos(),
					"declares a "+name+" method"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// integerConversion records a conversion of an integer to an enumerated type. The
// operand must not be a constant of the type itself: T(a member of T) and T(3)
// name a value in the source, while T(n) produces one nothing named.
func (s *enumGroupScan) integerConversion(p *packages.Package, call *ast.CallExpr) error {
	if len(call.Args) != 1 {
		return nil
	}
	target, held := p.TypesInfo.Types[call.Fun]
	if !held || !target.IsType() {
		return nil
	}
	named, ok := target.Type.(*types.Named)
	if !ok {
		return nil
	}
	enum := s.byKey[types.TypeString(named, nil)]
	if enum == nil {
		return nil
	}
	operand := p.TypesInfo.Types[call.Args[0]]
	if operand.Type == nil {
		return nil
	}
	basic, ok := operand.Type.Underlying().(*types.Basic)
	if !ok || basic.Info()&types.IsInteger == 0 {
		return nil
	}
	if operand.Value != nil && types.Identical(operand.Type, named) {
		return nil
	}
	return s.record(enum, enumGroupByInteger, call.Pos(),
		"converted from "+types.TypeString(operand.Type, nil))
}

// decoded records a decoder whose target carries an enumerated type, which is a
// value of that type arriving from outside the program.
func (s *enumGroupScan) decoded(p *packages.Package, call *ast.CallExpr) error {
	decoder := s.decoder(p, call)
	if decoder == "" {
		return nil
	}
	for _, arg := range call.Args {
		argument := p.TypesInfo.Types[arg]
		if argument.Type == nil {
			continue
		}
		for _, key := range s.reach(argument.Type) {
			enum := s.byKey[key]
			if enum == nil {
				continue
			}
			if err := s.record(enum, enumGroupByDecode, call.Pos(), "decoded by "+decoder); err != nil {
				return err
			}
		}
	}
	return nil
}

// decoder names the decoder one call calls, and an empty string for every other
// call. A method is named by its receiver's defined type and its own name.
func (s *enumGroupScan) decoder(p *packages.Package, call *ast.CallExpr) string {
	called, ok := resolveObject(p.TypesInfo, call.Fun).(*types.Func)
	if !ok || called.Pkg() == nil {
		return ""
	}
	name := called.Pkg().Path() + "."
	if recv := called.Signature().Recv(); recv != nil {
		t := recv.Type()
		if pointer, isPointer := t.(*types.Pointer); isPointer {
			t = pointer.Elem()
		}
		named, isNamed := t.(*types.Named)
		if !isNamed {
			return ""
		}
		name += named.Obj().Name() + "."
	}
	name += called.Name()
	if !slices.Contains(enumGroupDecoders[:], name) {
		return ""
	}
	return name
}

// reach names every defined type a value of t carries: t itself when it is
// defined, and every type reachable through a pointer, an element, a map key or
// value, or a struct field.
func (s *enumGroupScan) reach(t types.Type) []string {
	var keys []string
	seen := make(map[types.Type]struct{})
	var walk func(types.Type)
	walk = func(t types.Type) {
		if t == nil {
			return
		}
		if _, already := seen[t]; already {
			return
		}
		seen[t] = struct{}{}
		if named, ok := t.(*types.Named); ok {
			keys = append(keys, types.TypeString(named, nil))
			walk(named.Underlying())
			return
		}
		switch shape := t.(type) {
		case *types.Pointer:
			walk(shape.Elem())
		case *types.Slice:
			walk(shape.Elem())
		case *types.Array:
			walk(shape.Elem())
		case *types.Chan:
			walk(shape.Elem())
		case *types.Map:
			walk(shape.Key())
			walk(shape.Elem())
		case *types.Struct:
			for field := range shape.Fields() {
				walk(field.Type())
			}
		}
	}
	walk(t)
	return keys
}

// record keeps one piece of evidence when it outranks what the type already holds,
// and at the same rank when it is written earlier.
func (s *enumGroupScan) record(enum *enumGroupType, rank int, pos token.Pos, detail string) error {
	at, err := s.site(pos)
	if err != nil {
		return err
	}
	if enum.best == nil || rank < enum.best.rank ||
		(rank == enum.best.rank && bySite(at, enum.best.at) < 0) {
		enum.best = &enumGroupEvidence{rank: rank, at: at, detail: detail}
	}
	return nil
}

// exemptions renders one exemption per member of every enumerated type that
// fired, ordered by the defined type and then by the member's own declaration.
func (s *enumGroupScan) exemptions() []graph.Exemption {
	var found []graph.Exemption
	for _, key := range slices.Sorted(slices.Values(s.keys)) {
		enum := s.byKey[key]
		if enum.best == nil {
			continue
		}
		members := slices.Clone(enum.members)
		slices.SortFunc(members, func(a, b enumGroupMember) int { return bySite(a.at, b.at) })
		for _, m := range members {
			found = append(found, graph.Exemption{
				ID:     m.id,
				Class:  string(EnumGroup),
				Site:   enum.best.at,
				Detail: enum.best.detail,
			})
		}
	}
	return found
}

// each calls visit for every file of every package variant the load holds.
func (s *enumGroupScan) each(visit func(p *packages.Package, f *ast.File) error) error {
	for _, p := range s.in.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			if err := visit(p, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// site renders one position. Every position the load compiled is a position of
// the target, so one the resolver cannot render is a failure of the run rather
// than evidence to pass over.
func (s *enumGroupScan) site(pos token.Pos) (token.Position, error) {
	return s.in.Resolve.Render(pos)
}

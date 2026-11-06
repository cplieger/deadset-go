package facts

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"iter"
	"reflect"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"golang.org/x/tools/go/packages"
)

// SchemaRead is one use of a generated declaration that hand-written code makes
// without naming it, by a call of the schema runtime (grammar/analysis/
// references-go.md references-go.16) or a read of a oneof telling no member
// apart (references-go.15): From, the declaration holding the use, reads To.
// From is empty for a use in a loaded consumer's production code, an actual
// caller.
type SchemaRead struct {
	From, To inventory.SymbolID
}

// serializerEffect is what one serializer does to the message it is handed.
type serializerEffect uint8

const (
	serializerReads  serializerEffect = iota // encodes or compares: reads every field
	serializerWrites                         // decodes: writes
	serializerPasses                         // clones: passes its argument through
)

// serializers is the closed list of references-go.16, per package, by function
// or by option type and method; vtSerializers is protoc-gen-go-vtproto's methods
// of a message, by name (references-go.13).
var (
	textSerializers = map[string]serializerEffect{
		"Marshal": serializerReads, "Format": serializerReads, "Unmarshal": serializerWrites,
		"MarshalOptions.Marshal": serializerReads, "MarshalOptions.Format": serializerReads,
		"UnmarshalOptions.Unmarshal": serializerWrites,
	}
	serializers = map[string]map[string]serializerEffect{
		"google.golang.org/protobuf/proto": {
			"Marshal": serializerReads, "Size": serializerReads, "Equal": serializerReads,
			"Unmarshal": serializerWrites, "Clone": serializerPasses,
			"MarshalOptions.Marshal": serializerReads, "MarshalOptions.Size": serializerReads,
			"UnmarshalOptions.Unmarshal": serializerWrites,
		},
		protojsonPath: textSerializers,
		"google.golang.org/protobuf/encoding/prototext": textSerializers,
	}
	vtSerializers = map[string]serializerEffect{
		"MarshalVT": serializerReads, "SizeVT": serializerReads, "EqualVT": serializerReads,
		"UnmarshalVT": serializerWrites, "CloneVT": serializerPasses,
	}
)

// schemaScan is one scan of hand-written files for the reads of schemaReadsOf.
type schemaScan struct {
	info           *types.Info
	pkg            *packages.Package
	rs             *resolve.Resolver
	fset           *token.FileSet
	generatedFiles map[string]generatedFile // by the name a file is written at, which every reading of it shares
	oneofs         map[*types.TypeName][]oneof
	told           map[ast.Expr]bool // the oneof reads that tell a member apart, or are written
	evidence       capEvidence
	held           []SchemaRead
	consumer       bool // the files visited are a loaded consumer's
	test           bool // the file visited is a test file
}

// schemaReadsOf is every read a hand-written call of a serializer, an enum name
// conversion or a read of a oneof makes in the target (references-go.15,
// references-go.16, bindings.14, bindings.15): an encoding or comparing
// serializer reads every field reachable from the static message type it is
// handed, a name conversion reads every value of the enum, and a oneof read
// telling no member apart reads every member. It is also the cap of every
// consumer binding that hand-written code outside tests gives one ([schemaScan.capsOf]).
func schemaReadsOf(x *Index, rs *resolve.Resolver, generated map[string]generatedFile) (reads []SchemaRead, caps map[inventory.SymbolID]Cap) {
	s := &schemaScan{
		rs: rs, fset: x.fset, generatedFiles: generated,
		oneofs: make(map[*types.TypeName][]oneof), told: make(map[ast.Expr]bool),
	}
	s.scan(x.Sources(), load.TargetBuild)
	s.consumer = true
	for _, c := range x.order {
		s.scan(inventory.Sources(x.consumers[c], x.fset), c.Build)
	}
	return s.held, s.capsOf(x.bindings, x.Program())
}

// scan visits every hand-written file of sources, of one build. A loaded
// consumer's test code is skipped: no declaration holds a read it makes, and an
// unheld read seeds the carrier sweep, which counts production code alone.
func (s *schemaScan) scan(sources iter.Seq2[*packages.Package, *ast.File], build load.Build) {
	for p, f := range sources {
		_, test := inventory.IsTestFile(s.fset.File(f.Package).Name())
		if ast.IsGenerated(f) || p.TypesInfo == nil || (s.consumer && build.TestCode(test)) {
			continue
		}
		s.info, s.pkg, s.test = p.TypesInfo, p, test
		clear(s.told)
		ast.Inspect(f, s.visit)
	}
}

// visit records the reads one node makes, held by the declaration holding it.
// A node marks the oneof reads it tells members apart by before they are visited.
func (s *schemaScan) visit(n ast.Node) bool {
	var read []types.Object
	switch n := n.(type) {
	case *ast.CallExpr:
		read = append(s.serialized(n), s.oneofRead(n)...)
		s.told[ast.Unparen(n.Fun)] = true
		if !s.test {
			s.capEvidence(n)
		}
	case *ast.SelectorExpr:
		read = append(s.enumValues(n), s.oneofRead(n)...)
	case *ast.AssignStmt:
		for _, lhs := range n.Lhs {
			s.told[ast.Unparen(lhs)] = true
		}
	case *ast.TypeAssertExpr:
		s.told[ast.Unparen(n.X)] = true
	case *ast.BinaryExpr:
		s.compared(n)
	case *ast.SwitchStmt:
		s.switched(n)
	}
	if len(read) == 0 {
		return true
	}
	var from inventory.SymbolID
	if !s.consumer {
		holder, inside := s.rs.Holder(n.Pos())
		if !inside {
			return true
		}
		from = holder
	}
	for _, obj := range read {
		if to, ok := s.rs.Object(obj); ok {
			s.held = append(s.held, SchemaRead{From: from, To: to})
		}
	}
	return true
}

// called is the function or method of a package a call names through a
// selector, nil for any other call, and its name as the serializer tables key
// it: Type.Method for a method, empty for a method of an unnamed type.
func (s *schemaScan) called(call *ast.CallExpr) (sel *ast.SelectorExpr, fn *types.Func, name string) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, nil, ""
	}
	fn, ok = s.info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil, nil, ""
	}
	name = fn.Name()
	recv := fn.Signature().Recv()
	if recv == nil {
		return sel, fn, name
	}
	if named, isNamed := types.Unalias(derefed(recv.Type())).(*types.Named); isNamed {
		return sel, fn, named.Obj().Name() + "." + name
	}
	return sel, fn, ""
}

// serialized is every field an encoding or comparing serializer call reads.
func (s *schemaScan) serialized(call *ast.CallExpr) []types.Object {
	_, fn, name := s.called(call)
	if fn == nil {
		return nil
	}
	if effect, vt := vtSerializers[fn.Name()]; vt && fn.Signature().Recv() != nil && s.generated(fn) {
		if effect != serializerReads {
			return nil
		}
		return messageClosure(fn.Signature().Recv().Type())
	}
	if effect, listed := serializers[fn.Pkg().Path()][name]; !listed || effect != serializerReads {
		return nil
	}
	var read []types.Object
	for _, arg := range call.Args {
		read = append(read, messageClosure(s.info.TypeOf(arg))...)
	}
	return read
}

// enumValues is every value of the enum a name conversion reads: a call of the
// generated String method on a value of the enum type, a read of <Enum>_name or
// <Enum>_value, and a Values call on the enum's generated Descriptor.
func (s *schemaScan) enumValues(sel *ast.SelectorExpr) []types.Object {
	switch obj := s.info.Uses[sel.Sel].(type) {
	case *types.Func:
		if recv := obj.Signature().Recv(); recv != nil && obj.Name() == "String" && s.generated(obj) {
			return enumConstants(recv.Type())
		}
		if obj.Name() == "Values" {
			return s.descriptorValues(sel.X)
		}
	case *types.Var:
		return s.tableValues(obj)
	}
	return nil
}

// tableValues is every value of the enum whose generated <Enum>_name or
// <Enum>_value table v is.
func (s *schemaScan) tableValues(v *types.Var) []types.Object {
	if v.Pkg() == nil || !s.generated(v) {
		return nil
	}
	for _, suffix := range []string{"_name", "_value"} {
		if base, found := strings.CutSuffix(v.Name(), suffix); found {
			if enum, isType := v.Pkg().Scope().Lookup(base).(*types.TypeName); isType {
				return enumConstants(enum.Type())
			}
		}
	}
	return nil
}

// descriptorValues is every value of the enum whose generated Descriptor method
// the receiver of a Values call calls.
func (s *schemaScan) descriptorValues(receiver ast.Expr) []types.Object {
	inner, isCall := ast.Unparen(receiver).(*ast.CallExpr)
	if !isCall {
		return nil
	}
	descriptor, isSel := ast.Unparen(inner.Fun).(*ast.SelectorExpr)
	if !isSel {
		return nil
	}
	method, isFunc := s.info.Uses[descriptor.Sel].(*types.Func)
	if !isFunc || method.Name() != "Descriptor" || method.Signature().Recv() == nil || !s.generated(method) {
		return nil
	}
	return enumConstants(method.Signature().Recv().Type())
}

// compared marks an operand of == or != the other operand of which names a
// member's case constant: a constant of the operand's type other than the zero,
// not-set, one.
func (s *schemaScan) compared(n *ast.BinaryExpr) {
	if n.Op != token.EQL && n.Op != token.NEQ {
		return
	}
	for _, pair := range [2][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
		if value, named := s.caseConstant(pair[1], pair[0]); named && constant.Sign(value) != 0 {
			s.told[ast.Unparen(pair[0])] = true
		}
	}
}

// switched marks a switch's tag where every case names a case constant of the
// tag's type, the not-set one, which reads nothing further, included.
func (s *schemaScan) switched(n *ast.SwitchStmt) {
	if n.Tag == nil {
		return
	}
	for _, stmt := range n.Body.List {
		clause, isClause := stmt.(*ast.CaseClause)
		if !isClause {
			return
		}
		for _, e := range clause.List {
			if _, named := s.caseConstant(e, n.Tag); !named {
				return
			}
		}
	}
	s.told[ast.Unparen(n.Tag)] = true
}

// caseConstant is the value of the generated integer constant e names when its
// type is the type of against.
func (s *schemaScan) caseConstant(e, against ast.Expr) (constant.Value, bool) {
	var id *ast.Ident
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		id = e
	case *ast.SelectorExpr:
		id = e.Sel
	default:
		return nil, false
	}
	c, isConst := s.info.Uses[id].(*types.Const)
	if !isConst || c.Pkg() == nil || !s.generated(c) || c.Val().Kind() != constant.Int || !types.Identical(c.Type(), s.info.TypeOf(against)) {
		return nil, false
	}
	return c.Val(), true
}

// oneofRead is every member declaration a read of a oneof reads when it tells no
// member apart: a selection of the oneof's field, or a call or method value of
// its Get, Which or Has method, that is no type-switch or type-assertion
// operand, no operand compared with a member's case constant and no left side of
// an assignment.
func (s *schemaScan) oneofRead(e ast.Expr) []types.Object {
	if s.told[e] {
		return nil
	}
	o, found := s.oneofOf(e)
	if !found {
		return nil
	}
	var read []types.Object
	for _, m := range o.members {
		read = append(read, m.declarations()...)
	}
	return read
}

// oneofOf is the oneof of a generated message e selects the field of, or calls
// or selects the Get, Which or Has method of.
func (s *schemaScan) oneofOf(e ast.Expr) (oneof, bool) {
	selection, method := s.selected(e)
	if selection == nil {
		return oneof{}, false
	}
	owner := ownerOf(selection)
	if owner == nil {
		return oneof{}, false
	}
	if _, memo := s.oneofs[owner]; !memo {
		s.oneofs[owner] = oneofsOf(owner)
	}
	name := selection.Obj().Name()
	for _, prefix := range []string{"Get", "Which", "Has"} {
		if rest, cut := strings.CutPrefix(name, prefix); cut && method {
			name = rest
			break
		}
	}
	for _, o := range s.oneofs[owner] {
		if (method && o.name == name) || (!method && o.field == selection.Obj()) {
			return o, true
		}
	}
	return oneof{}, false
}

// selected is the selection of a generated field or method e makes, or of the
// method e calls, and whether it selects a method.
func (s *schemaScan) selected(e ast.Expr) (*types.Selection, bool) {
	call, isCall := e.(*ast.CallExpr)
	if isCall {
		e = ast.Unparen(call.Fun)
	}
	sel, isSel := e.(*ast.SelectorExpr)
	if !isSel {
		return nil, false
	}
	selection := s.info.Selections[sel]
	if selection == nil || !s.generated(selection.Obj()) {
		return nil, false
	}
	method := selection.Kind() == types.MethodVal
	if isCall && !method {
		return nil, false
	}
	return selection, method
}

// ownerOf is the named type declaring the field or method a selection selects,
// through the embedded fields its path promotes it by.
func ownerOf(selection *types.Selection) *types.TypeName {
	if fn, isFunc := selection.Obj().(*types.Func); isFunc {
		named, isNamed := types.Unalias(derefed(fn.Signature().Recv().Type())).(*types.Named)
		if !isNamed {
			return nil
		}
		return named.Origin().Obj()
	}
	t := selection.Recv()
	path := selection.Index()
	for _, ix := range path[:len(path)-1] {
		structure, isStruct := derefed(t).Underlying().(*types.Struct)
		if !isStruct {
			return nil
		}
		t = structure.Field(ix).Type()
	}
	named, isNamed := types.Unalias(derefed(t)).(*types.Named)
	if !isNamed {
		return nil
	}
	return named.Origin().Obj()
}

// generated reports whether obj is declared in a generated file of the target.
func (s *schemaScan) generated(obj types.Object) bool {
	_, generated := s.generatedFiles[s.rs.Positions().Offset(obj.Pos()).File]
	return generated
}

// enumConstants is every constant of the named integer type t its package declares.
func enumConstants(t types.Type) []types.Object {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return nil
	}
	if basic, isBasic := named.Underlying().(*types.Basic); !isBasic || basic.Info()&types.IsInteger == 0 {
		return nil
	}
	var held []types.Object
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		if c, isConst := scope.Lookup(name).(*types.Const); isConst && types.Identical(c.Type(), named) {
			held = append(held, c)
		}
	}
	return held
}

// messageClosure is every field reachable from the message type t through field
// types: each protobuf field of the struct, each oneof member's declarations, and
// so on through the message types a field's type holds as pointer,
// slice, array or map key or value.
func messageClosure(t types.Type) []types.Object {
	return closureOf(t).held
}

// closureOf is the walk of messageClosure from t.
func closureOf(t types.Type) *closure {
	c := &closure{seen: make(map[*types.TypeName]bool)}
	c.walk(t)
	return c
}

type closure struct {
	seen     map[*types.TypeName]bool
	held     []types.Object
	messages []types.Object // every message type the walk entered
}

func (c *closure) walk(t types.Type) {
	switch u := types.Unalias(t).(type) {
	case *types.Pointer:
		c.walk(u.Elem())
	case *types.Slice:
		c.walk(u.Elem())
	case *types.Array:
		c.walk(u.Elem())
	case *types.Map:
		c.walk(u.Key())
		c.walk(u.Elem())
	case *types.Named:
		c.message(u)
	}
}

// message holds every protobuf field and oneof member of one message type.
func (c *closure) message(named *types.Named) {
	structure, isStruct := named.Underlying().(*types.Struct)
	if !isStruct || c.seen[named.Obj()] || named.Obj().Pkg() == nil {
		return
	}
	c.seen[named.Obj()] = true
	c.messages = append(c.messages, named.Obj())
	for i := range structure.NumFields() {
		if reflect.StructTag(structure.Tag(i)).Get("protobuf") != "" {
			c.held = append(c.held, structure.Field(i))
			c.walk(structure.Field(i).Type())
		}
	}
	for _, o := range oneofsOf(named.Obj()) {
		for _, m := range o.members {
			c.held = append(c.held, m.declarations()...)
			c.walk(m.field.Type())
		}
	}
}

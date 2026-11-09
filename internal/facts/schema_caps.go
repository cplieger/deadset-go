package facts

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"maps"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"golang.org/x/tools/go/packages"
)

// Cap is hand-code evidence that lowers the schema finding of a consumer binding
// to possible (grammar/analysis/bindings.md bindings.16).
type Cap uint8

// The caps, the zero Cap none, ordered so that the greater is the one a binding
// with both carries: both lower a finding alike, and the facts document holds one.
const (
	DescriptorExposure Cap = iota + 1
	Persistence
)

// generatedFile is one generated file of the target as the schema scan reads it:
// the path the report spells it with and its package.
type generatedFile struct {
	path, pkg string
}

// capEvidence is the hand-written evidence of grammar/analysis/references-go.md
// references-go.17 one schema scan gathers outside test files.
type capEvidence struct {
	converted map[string]bool     // the written generated files whose descriptor is converted
	persisted []types.Object      // what a ProtoJSON decode keeping unknown fields fills
	serving   []*packages.Package // the packages serving server reflection
}

// The packages of references-go.17's ProtoJSON decoding and descriptor conversion.
const (
	protojsonPath = "google.golang.org/protobuf/encoding/protojson"
	protodescPath = "google.golang.org/protobuf/reflect/protodesc"
)

// reflectionServers is references-go.17's functions serving server reflection,
// per package.
var reflectionServers = map[string]map[string]bool{
	"google.golang.org/grpc/reflection": {"Register": true},
	"connectrpc.com/grpcreflect":        {"NewHandlerV1": true, "NewHandlerV1Alpha": true},
}

// capEvidence records what one call shows for references-go.17: a ProtoJSON
// decode keeping unknown fields persists every message and field reachable from
// its arguments' static types, a server reflection call serves its package's
// program, and a descriptor conversion names the generated file declaring the
// File_ variable it is handed.
func (s *schemaScan) capEvidence(call *ast.CallExpr) {
	sel, fn, name := s.called(call)
	if fn == nil {
		return
	}
	switch path := fn.Pkg().Path(); {
	case path == protojsonPath && textSerializers[name] == serializerWrites &&
		(fn.Signature().Recv() == nil || !s.discardsUnknown(sel.X)):
		for _, arg := range call.Args {
			c := closureOf(s.info.TypeOf(arg))
			s.evidence.persisted = append(append(s.evidence.persisted, c.held...), c.messages...)
		}
	case reflectionServers[path][name]:
		s.evidence.serving = append(s.evidence.serving, s.pkg)
	case path == protodescPath && name == "ToFileDescriptorProto":
		s.converts(call.Args)
	}
}

// converts records the generated file declaring each File_ variable args name.
func (s *schemaScan) converts(args []ast.Expr) {
	for _, arg := range args {
		if v := s.generatedVar(arg); v != nil {
			if s.evidence.converted == nil {
				s.evidence.converted = make(map[string]bool)
			}
			s.evidence.converted[s.rs.Positions().Offset(v.Pos()).File] = true
		}
	}
}

// discardsUnknown reports whether the receiver of an UnmarshalOptions.Unmarshal
// call is a literal, or the address of one, setting DiscardUnknown to a constant
// true, by its key or by its position. Any other receiver may keep unknown fields.
func (s *schemaScan) discardsUnknown(receiver ast.Expr) bool {
	e := ast.Unparen(receiver)
	if addr, isAddr := e.(*ast.UnaryExpr); isAddr && addr.Op == token.AND {
		e = ast.Unparen(addr.X)
	}
	literal, isLiteral := e.(*ast.CompositeLit)
	if !isLiteral {
		return false
	}
	var fields *types.Struct
	if t := s.info.TypeOf(literal); t != nil {
		fields, _ = t.Underlying().(*types.Struct)
	}
	for i, elt := range literal.Elts {
		name, value := "", elt
		if kv, isKV := elt.(*ast.KeyValueExpr); isKV {
			if key, isIdent := kv.Key.(*ast.Ident); isIdent {
				name = key.Name
			}
			value = kv.Value
		} else if fields != nil && i < fields.NumFields() {
			name = fields.Field(i).Name()
		}
		if name == "DiscardUnknown" {
			set := s.info.Types[value].Value
			return set != nil && constant.BoolVal(set)
		}
	}
	return false
}

// generatedVar is the variable a generated file declares that e names, or nil.
func (s *schemaScan) generatedVar(e ast.Expr) *types.Var {
	var id *ast.Ident
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		id = e
	case *ast.SelectorExpr:
		id = e.Sel
	default:
		return nil
	}
	if v, isVar := s.info.Uses[id].(*types.Var); isVar && !v.IsField() && s.generated(v) {
		return v
	}
	return nil
}

// capsOf is the cap of every consumer binding the scan's evidence gives one
// (references-go.17, bindings.16): persistence on every binding of a schema
// declaration a ProtoJSON decode keeping unknown fields fills, and
// descriptor-exposure on every binding of a generated file a program serving
// reflection links and of the schema file whose descriptor hand code converts.
func (s *schemaScan) capsOf(bindings []Binding, program []*packages.Package) map[inventory.SymbolID]Cap {
	persisted := make(map[inventory.SymbolID]bool, len(s.evidence.persisted))
	for _, obj := range s.evidence.persisted {
		if id, ok := s.rs.Object(obj); ok {
			persisted[id] = true
		}
	}
	linked := linkedBy(s.evidence.serving, program)
	pkgOf := make(map[string]string, len(s.generatedFiles))
	converted := make(map[string]bool)
	for name, g := range s.generatedFiles {
		pkgOf[g.path] = g.pkg
		converted[g.path] = s.evidence.converted[name]
	}
	persistedSchemas := make(map[[2]string]bool)
	exposedSchemaFiles := make(map[string]bool)
	for _, b := range bindings {
		if b.Role != consumerRole {
			continue
		}
		if persisted[b.Symbol] {
			persistedSchemas[[2]string{b.SchemaFile, b.Schema}] = true
		}
		if converted[b.GeneratedFile] {
			exposedSchemaFiles[b.SchemaFile] = true
		}
	}
	caps := make(map[inventory.SymbolID]Cap)
	for _, b := range bindings {
		switch {
		case b.Role != consumerRole:
		case persistedSchemas[[2]string{b.SchemaFile, b.Schema}]:
			caps[b.Symbol] = Persistence
		case linked[pkgOf[b.GeneratedFile]] || exposedSchemaFiles[b.SchemaFile]:
			caps[b.Symbol] = DescriptorExposure
		}
	}
	return caps
}

// linkedBy is the path of every package a program holding one of serving links:
// each main package of program that links a serving package, or the serving
// package's production variant itself where no main package does, since a test
// variant links its test imports too.
func linkedBy(serving, program []*packages.Package) map[string]bool {
	linked := make(map[string]bool)
	if len(serving) == 0 {
		return linked
	}
	var mains []map[string]bool
	production := make(map[string]*packages.Package, len(program))
	for _, p := range program {
		if p.ForTest != "" {
			continue
		}
		production[p.PkgPath] = p
		if p.Name == mainPackage {
			mains = append(mains, importClosure(p))
		}
	}
	for _, p := range serving {
		hosted := false
		for _, m := range mains {
			if m[p.PkgPath] {
				hosted = true
				maps.Copy(linked, m)
			}
		}
		if own := production[p.PkgPath]; !hosted && own != nil {
			maps.Copy(linked, importClosure(own))
		}
	}
	return linked
}

// importClosure is the path of p and of every package it imports, directly or not.
func importClosure(p *packages.Package) map[string]bool {
	seen := make(map[string]bool)
	pending := []*packages.Package{p}
	for len(pending) > 0 {
		next := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[next.PkgPath] {
			continue
		}
		seen[next.PkgPath] = true
		for _, imported := range next.Imports {
			pending = append(pending, imported)
		}
	}
	return seen
}

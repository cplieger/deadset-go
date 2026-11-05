package facts

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"iter"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/mod/semver"
	"golang.org/x/tools/go/packages"
)

// errUndecodable is a generated file a row of contract/generators.json covers
// whose identities the analysis cannot read by that row.
var errUndecodable = errors.New("a generated file its generator row covers cannot be decoded by the row")

// Binding is one binding identity: a generated declaration of the target, or a
// generated file, and the schema declaration it stands for.
type Binding struct {
	Symbol        inventory.SymbolID // the declaration; empty for a fallback
	SchemaFile    string
	Schema        string // the proto:// reference; empty for a fallback
	GeneratedFile string
	Role          bindingRole
	Oneof         OneofPart
}

// OneofPart is the part of a oneof member a consumer's declaration is, which the
// carrier sweep reads by grammar/analysis/references-go.md references-go.15's
// forms alone.
type OneofPart uint8

// The parts of a oneof member.
const (
	NotOneof     OneofPart = iota // no part of a oneof member
	OneofWrapper                  // the member's wrapper type
	oneofField                    // the wrapper's one field
	oneofCase                     // the member's case constant
)

// bindingRole is what a binding identity's declaration is to its schema declaration.
type bindingRole uint8

const (
	// consumerRole is a generated declaration the schema declaration became.
	consumerRole bindingRole = iota

	// Fallback is a generated file standing for its whole schema file, which no
	// declaration-level identity could be read for.
	Fallback

	// Served is a method of a generated server or handler interface, which
	// publishes no record: the hand-written methods answering it are the
	// implementers of its RPC (grammar/analysis/bindings.md bindings.8).
	Served

	// Registration is a generated registration function of a service, which
	// publishes no record: the carrier sweep follows none of its references
	// (bindings.13).
	Registration
)

// The cells of contract/generators.json that publish records.
const (
	cellExact    = "exact"
	cellRPCOnly  = "rpc-only"
	cellFallback = "fallback"
)

// cellExposureOnly is the cell of a row that publishes no record but registers
// services, whose registration functions the carrier sweep cuts.
const cellExposureOnly = "exposure-only"

// goRows is every generator row of Go, in document order, with its compiled header.
var goRows = sync.OnceValue(func() []generatorRow {
	var rows []generatorRow
	for _, row := range vocab.GeneratorRows() {
		if row.Language == vocab.Go {
			rows = append(rows, generatorRow{
				GeneratorRow: row,
				header:       regexp.MustCompile(row.Header),
				version:      regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(row.Plugin) + ` (v\d+\.\d+\.\d+\S*)`),
			})
		}
	}
	return rows
})

type generatorRow struct {
	header, version *regexp.Regexp
	vocab.GeneratorRow
}

// sourceLine is the line a protobuf generator names its schema file on.
var sourceLine = regexp.MustCompile(`^//\s*[Ss]ource: (\S+\.proto)$`)

// leadingLines is every comment line before f's package clause.
func leadingLines(f *ast.File) []string {
	var lines []string
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			lines = append(lines, strings.Split(c.Text, "\n")...)
		}
	}
	return lines
}

// recognise names the row g's file matches: the first Go row whose header
// matches a leading comment line, whose runtime import the file imports, and
// whose version interval holds the version the output states.
func recognise(g *Generated, f *ast.File) *generatorRow {
	lines := leadingLines(f)
	rows := goRows()
	for i := range rows {
		row := &rows[i]
		if !slices.ContainsFunc(lines, row.header.MatchString) || !imports(f, row.RuntimeImport) {
			continue
		}
		version := firstMatch(lines, row.version)
		if (row.VersionMin != "" || row.VersionMax != "") && !within(version, row.VersionMin, row.VersionMax) {
			continue
		}
		g.Generator, g.Version, g.SchemaFiles = row.ID, version, schemaFiles(lines)
		return row
	}
	return nil
}

// imports reports whether f imports path; an empty path asks nothing.
func imports(f *ast.File, path string) bool {
	return path == "" || slices.ContainsFunc(f.Imports, func(spec *ast.ImportSpec) bool {
		return spec.Path.Value == strconv.Quote(path)
	})
}

// firstMatch is re's first submatch on the first of lines it matches, or "".
func firstMatch(lines []string, re *regexp.Regexp) string {
	for _, line := range lines {
		if m := re.FindStringSubmatch(line); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// schemaFiles is every schema file a source line of lines names, once each.
func schemaFiles(lines []string) []string {
	held := []string{}
	for _, line := range lines {
		if m := sourceLine.FindStringSubmatch(line); len(m) > 1 && !slices.Contains(held, m[1]) {
			held = append(held, m[1])
		}
	}
	return held
}

// within reports whether version lies in the interval [low, high], either end
// open where empty.
func within(version, low, high string) bool {
	return semver.IsValid(version) && (low == "" || semver.Compare(version, low) >= 0) &&
		(high == "" || semver.Compare(version, high) <= 0)
}

// binder publishes the records of one generated file.
type binder struct {
	rs   *resolve.Resolver
	p    *packages.Package
	f    *ast.File
	g    *Generated
	held []Binding
}

// bind is the records of one generated file its row matched.
func bind(rs *resolve.Resolver, p *packages.Package, f *ast.File, g *Generated, row *generatorRow) ([]Binding, error) {
	b := &binder{rs: rs, p: p, f: f, g: g}
	var err error
	switch row.Cell {
	case cellExact:
		err = b.descriptor()
	case cellRPCOnly:
		err = b.services(row.Registration)
	case cellExposureOnly:
		b.registrations(row.Registration)
	case cellFallback:
		for _, file := range g.SchemaFiles {
			b.held = append(b.held, Binding{Role: Fallback, SchemaFile: file, GeneratedFile: g.Path})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s, row %s: %w", errUndecodable, g.Path, row.ID, err)
	}
	return b.held, nil
}

// consumer publishes obj as standing for the schema declaration fragment of pkg.
func (b *binder) consumer(obj types.Object, schemaFile, pkg, fragment string) {
	b.identity(consumerRole, NotOneof, obj, schemaFile, pkg, fragment)
}

// identity publishes obj in role, as part of a oneof member or not, as standing
// for the schema declaration fragment of pkg.
func (b *binder) identity(role bindingRole, part OneofPart, obj types.Object, schemaFile, pkg, fragment string) {
	if obj == nil {
		return
	}
	if id, ok := b.rs.Object(obj); ok {
		b.held = append(b.held, Binding{
			Symbol: id, SchemaFile: schemaFile, Role: role, Oneof: part,
			Schema: protoRef(pkg, fragment), GeneratedFile: b.g.Path,
		})
	}
}

// protoRef is the proto:// reference of a declaration of pkg, a single dot
// standing for no package.
func protoRef(pkg, fragment string) string {
	if pkg == "" {
		pkg = "."
	}
	return "proto://" + pkg + "#" + fragment
}

// declared is every value spec of b's file, by the name it declares.
func (b *binder) declared() map[string]ast.Expr {
	values := make(map[string]ast.Expr)
	for v := range valueSpecs(b.f) {
		for i, name := range v.Names {
			values[name.Name] = name
			if i < len(v.Values) {
				values[name.Name] = v.Values[i]
			}
		}
	}
	return values
}

// valueSpecs is every value specification of f's declarations.
func valueSpecs(f *ast.File) iter.Seq[*ast.ValueSpec] {
	return func(yield func(*ast.ValueSpec) bool) {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				if v, isValue := spec.(*ast.ValueSpec); isValue && !yield(v) {
					return
				}
			}
		}
	}
}

// fileTable is the value values holds for a file_<f><suffix> declaration, or nil.
func fileTable(values map[string]ast.Expr, suffix string) ast.Expr {
	for name, value := range values {
		if strings.HasPrefix(name, "file_") && strings.HasSuffix(name, suffix) {
			return value
		}
	}
	return nil
}

// descriptor publishes the records of a protoc-gen-go file from the serialized
// file descriptor it embeds, three spellings of it read, and its goTypes table.
func (b *binder) descriptor() error {
	values := b.declared()
	body, ok := b.bytesOf(fileTable(values, "_rawDesc"))
	if !ok {
		return errors.New("no file_<f>_rawDesc of a form the row decodes")
	}
	d, err := decodeFile(body)
	if err != nil {
		return err
	}
	names := descriptorNames(&d)
	table, ok := ast.Unparen(fileTable(values, "_goTypes")).(*ast.CompositeLit)
	if !ok {
		return errors.New("no file_<f>_goTypes table")
	}
	declared := len(names)
	for _, element := range table.Elts {
		if declared == 0 {
			break
		}
		declared--
		full, ok := b.trailing(element)
		if !ok || names[full] == nil {
			return fmt.Errorf("the goTypes entry %q names no declaration of the descriptor", full)
		}
		if obj := typeNameOf(b.p.TypesInfo, element); obj != nil {
			b.declaration(&d, obj, full, names[full])
		}
	}
	b.extensions(&d, values)
	return nil
}

// descriptorNames is every enum and message of d by its full name, a message
// with its descriptor and an enum with nil.
func descriptorNames(d *fileDescriptor) map[string]any {
	names := make(map[string]any)
	prefix := ""
	if d.pkg != "" {
		prefix = d.pkg + "."
	}
	var walk func(scope string, messages []messageDescriptor, enums []enumDescriptor)
	walk = func(scope string, messages []messageDescriptor, enums []enumDescriptor) {
		for i := range enums {
			names[scope+enums[i].name] = &enums[i]
		}
		for i := range messages {
			names[scope+messages[i].name] = &messages[i]
			walk(scope+messages[i].name+".", messages[i].nested, messages[i].enums)
		}
	}
	walk(prefix, d.messages, d.enums)
	return names
}

// bytesOf is the bytes a rawDesc declaration holds: a []byte literal, a
// conversion of one to string, or a string constant.
func (b *binder) bytesOf(expr ast.Expr) ([]byte, bool) {
	info := b.p.TypesInfo
	if id, ok := expr.(*ast.Ident); ok {
		if c, ok := info.Defs[id].(*types.Const); ok && c.Val().Kind() == constant.String {
			return []byte(constant.StringVal(c.Val())), true
		}
	}
	if tv, ok := info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return []byte(constant.StringVal(tv.Value)), true
	}
	if call, ok := ast.Unparen(expr).(*ast.CallExpr); ok && len(call.Args) == 1 {
		expr = call.Args[0]
	}
	lit, ok := ast.Unparen(expr).(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	held := make([]byte, 0, len(lit.Elts))
	for _, element := range lit.Elts {
		v, exact := constant.Uint64Val(info.Types[element].Value)
		if !exact || v > 0xff {
			return nil, false
		}
		held = append(held, byte(v))
	}
	return held, true
}

// trailing is the full name the comment ending element's line writes, as
// protoc-gen-go writes `// 0: pkg.Msg` after each goTypes entry.
func (b *binder) trailing(element ast.Expr) (string, bool) {
	fset := b.rs.Positions().Fset
	line := fset.PositionFor(element.End(), false).Line
	for _, group := range b.f.Comments {
		for _, c := range group.List {
			if fset.PositionFor(c.Pos(), false).Line != line || c.Pos() < element.End() {
				continue
			}
			_, full, ok := strings.Cut(strings.TrimPrefix(c.Text, "//"), ": ")
			return strings.TrimSpace(full), ok
		}
	}
	return "", false
}

// typeNameOf is the named type a goTypes entry, (*Msg)(nil) or (Enum)(0), converts to.
func typeNameOf(info *types.Info, element ast.Expr) *types.TypeName {
	call, ok := ast.Unparen(element).(*ast.CallExpr)
	if !ok {
		return nil
	}
	fun := ast.Unparen(call.Fun)
	if star, isStar := fun.(*ast.StarExpr); isStar {
		fun = ast.Unparen(star.X)
	}
	id, ok := fun.(*ast.Ident)
	if !ok {
		return nil
	}
	name, _ := info.Uses[id].(*types.TypeName)
	return name
}

// declaration publishes one message's or enum's records.
func (b *binder) declaration(d *fileDescriptor, obj *types.TypeName, full string, desc any) {
	fragment := strings.TrimPrefix(full, d.pkg+".")
	if d.pkg == "" {
		fragment = full
	}
	b.consumer(obj, d.name, d.pkg, fragment)
	switch desc := desc.(type) {
	case *messageDescriptor:
		b.message(d, obj, fragment, desc)
	case *enumDescriptor:
		b.enum(d, obj, fragment, desc)
	}
}

// message publishes a message's fields, through their tag numbers, with their
// getters, Has and Clear methods, oneof wrappers and case constants.
func (b *binder) message(d *fileDescriptor, obj *types.TypeName, fragment string, m *messageDescriptor) {
	structure, isStruct := obj.Type().Underlying().(*types.Struct)
	if !isStruct {
		return
	}
	byNumber := make(map[int]string, len(m.fields))
	for _, f := range m.fields {
		byNumber[f.number] = f.name
	}
	member := func(tag string) (string, bool) {
		number, found := tagNumber(tag)
		if !found || byNumber[number] == "" {
			return "", false
		}
		return fragment + "." + byNumber[number], true
	}
	methods := types.NewMethodSet(types.NewPointer(obj.Type()))
	for i := range structure.NumFields() {
		if at, ok := member(structure.Tag(i)); ok {
			b.field(d, obj, methods, structure.Field(i), at, NotOneof)
		}
	}
	for _, o := range oneofsOf(obj) {
		for _, m := range o.members {
			if at, ok := member(m.tag); ok {
				b.oneofMember(d, obj, methods, m, at)
			}
		}
	}
}

// oneofMember publishes one oneof member of message obj as the schema member at:
// its wrapper type, the wrapper's field with its accessors, and its case constant.
func (b *binder) oneofMember(d *fileDescriptor, obj *types.TypeName, methods *types.MethodSet, m oneofMember, at string) {
	b.identity(consumerRole, OneofWrapper, m.wrapper, d.name, d.pkg, at)
	b.field(d, obj, methods, m.field, at, oneofField)
	if m.caseConst != nil {
		b.identity(consumerRole, oneofCase, m.caseConst, d.name, d.pkg, at)
	}
}

// field publishes one field of message obj, the part of a oneof member it is, as
// the schema member at, with its getter, Has and Clear methods.
func (b *binder) field(d *fileDescriptor, obj *types.TypeName, methods *types.MethodSet, field *types.Var, at string, part OneofPart) {
	b.identity(consumerRole, part, field, d.name, d.pkg, at)
	name := strings.TrimPrefix(field.Name(), "xxx_hidden_")
	for _, prefix := range []string{"Get", "Has", "Clear"} {
		if sel := methods.Lookup(obj.Pkg(), prefix+name); sel != nil {
			b.consumer(sel.Obj(), d.name, d.pkg, at)
		}
	}
}

// oneof is one oneof of a generated message: the message's field of it and its
// members.
type oneof struct {
	field   *types.Var
	name    string // the oneof's Go name: Pick for the field Pick or, opaque, xxx_hidden_Pick
	members []oneofMember
}

// oneofMember is one member of a oneof: its wrapper type, the wrapper's one field
// with its struct tag, and the member's case constant, nil in the open API.
type oneofMember struct {
	wrapper   *types.TypeName
	field     *types.Var
	caseConst *types.Const
	tag       string
}

// declarations is every declaration of the member a read of it reads.
func (m oneofMember) declarations() []types.Object {
	if m.caseConst == nil {
		return []types.Object{m.wrapper, m.field}
	}
	return []types.Object{m.wrapper, m.field, m.caseConst}
}

// oneofsOf is every oneof of message obj: a field tagged protobuf_oneof, whose
// members are the one-field structs of the package a pointer to which implements
// the field's interface type, each with its <Msg>_<Field>_case constant.
func oneofsOf(obj *types.TypeName) []oneof {
	structure, isStruct := obj.Type().Underlying().(*types.Struct)
	if !isStruct {
		return nil
	}
	var held []oneof
	for i := range structure.NumFields() {
		iface, isInterface := structure.Field(i).Type().Underlying().(*types.Interface)
		if !isInterface || iface.NumMethods() == 0 || reflect.StructTag(structure.Tag(i)).Get("protobuf_oneof") == "" {
			continue
		}
		held = append(held, oneof{
			field:   structure.Field(i),
			name:    strings.TrimPrefix(structure.Field(i).Name(), "xxx_hidden_"),
			members: oneofMembers(obj, iface),
		})
	}
	return held
}

// oneofMembers is every member of message obj's oneof whose interface type iface is.
func oneofMembers(obj *types.TypeName, iface *types.Interface) []oneofMember {
	var held []oneofMember
	scope := obj.Pkg().Scope()
	for _, name := range scope.Names() {
		wrapper, isType := scope.Lookup(name).(*types.TypeName)
		if !isType || wrapper.IsAlias() {
			continue
		}
		ws, isStruct := wrapper.Type().Underlying().(*types.Struct)
		if !isStruct || ws.NumFields() != 1 || !types.Implements(types.NewPointer(wrapper.Type()), iface) {
			continue
		}
		m := oneofMember{wrapper: wrapper, field: ws.Field(0), tag: ws.Tag(0)}
		m.caseConst, _ = scope.Lookup(obj.Name() + "_" + ws.Field(0).Name() + "_case").(*types.Const)
		held = append(held, m)
	}
	return held
}

// tagNumber is the field number a protobuf struct tag writes second:
// `protobuf:"varint,3,opt,name=x"`, or `group,3,...` for a group.
func tagNumber(tag string) (int, bool) {
	parts := strings.Split(reflect.StructTag(tag).Get("protobuf"), ",")
	if len(parts) < 2 {
		return 0, false
	}
	number, err := strconv.Atoi(parts[1])
	return number, err == nil
}

// enum publishes each constant of the enum's type by the value it names: the
// value its name ends with, else the first value of its number.
func (b *binder) enum(d *fileDescriptor, obj *types.TypeName, fragment string, e *enumDescriptor) {
	scope := obj.Pkg().Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !types.Identical(c.Type(), obj.Type()) {
			continue
		}
		number, exact := constant.Int64Val(c.Val())
		value := ""
		for _, v := range e.values {
			if strings.HasSuffix(name, "_"+v.name) {
				value = v.name
				break
			}
			if exact && v.number == int(number) && value == "" {
				value = v.name
			}
		}
		if value != "" {
			b.consumer(c, d.name, d.pkg, fragment+"."+value)
		}
	}
}

// extensions publishes each E_ variable that points into the file's extTypes
// table at the extension that entry's Name names.
func (b *binder) extensions(d *fileDescriptor, values map[string]ast.Expr) {
	table, _ := ast.Unparen(fileTable(values, "_extTypes")).(*ast.CompositeLit)
	if table == nil {
		return
	}
	for name, value := range values {
		unary, ok := ast.Unparen(value).(*ast.UnaryExpr)
		if !strings.HasPrefix(name, "E_") || !ok || unary.Op != token.AND {
			continue
		}
		index, ok := unary.X.(*ast.IndexExpr)
		if !ok {
			continue
		}
		at, exact := constant.Int64Val(b.p.TypesInfo.Types[index.Index].Value)
		if !exact || at < 0 || int(at) >= len(table.Elts) {
			continue
		}
		full := extensionName(b.p.TypesInfo, table.Elts[at])
		if full == "" {
			continue
		}
		fragment := strings.TrimPrefix(full, d.pkg+".")
		b.consumer(b.p.TypesInfo.Defs[b.ident(name)], d.name, d.pkg, fragment)
	}
}

// extensionName is the Name an ExtensionInfo literal sets.
func extensionName(info *types.Info, element ast.Expr) string {
	lit, ok := ast.Unparen(element).(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, kv := range lit.Elts {
		pair, ok := kv.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, isIdent := pair.Key.(*ast.Ident); isIdent && key.Name == "Name" {
			if tv := info.Types[pair.Value]; tv.Value != nil && tv.Value.Kind() == constant.String {
				return constant.StringVal(tv.Value)
			}
		}
	}
	return ""
}

// ident is the identifier b's file declares name with.
func (b *binder) ident(name string) *ast.Ident {
	for v := range valueSpecs(b.f) {
		for _, id := range v.Names {
			if id.Name == name {
				return id
			}
		}
	}
	return nil
}

// fullMethod is a gRPC or Connect full method name, /pkg.Service/Method.
var fullMethod = regexp.MustCompile(`^/((?:[A-Za-z_]\w*\.)*)([A-Za-z_]\w*)/([A-Za-z_]\w*)$`)

// services publishes the records of a gRPC or Connect file of the row id: each
// RPC its full method names name, by the client interface's method, each service
// by its client interface, its client constructor and its server or handler
// interface, each RPC's method of that interface as served, and each service's
// registration functions.
func (b *binder) services(registration []string) error {
	if len(b.g.SchemaFiles) == 0 {
		return errors.New("the output names no schema file")
	}
	schemaFile := b.g.SchemaFiles[0]
	b.registrations(registration)
	bound := make(map[string]bool)
	for _, r := range fullMethods(b.f) {
		client := b.inFile(r.service + "Client")
		if client == nil {
			continue
		}
		b.rpcMethod(consumerRole, client, schemaFile, r)
		for _, name := range []string{r.service + "Server", r.service + "Handler"} {
			if server := b.inFile(name); server != nil {
				b.rpcMethod(Served, server, schemaFile, r)
			}
		}
		if service := r.pkg + "." + r.service; !bound[service] {
			bound[service] = true
			for _, name := range []string{r.service + "Client", "New" + r.service + "Client", r.service + "Server", r.service + "Handler"} {
				b.consumer(b.inFile(name), schemaFile, r.pkg, r.service)
			}
		}
	}
	return nil
}

// registrations publishes, for each service a full method name of b's file names,
// the registration functions of a row's registration names that the file declares.
func (b *binder) registrations(names []string) {
	schemaFile := ""
	if len(b.g.SchemaFiles) > 0 {
		schemaFile = b.g.SchemaFiles[0]
	}
	bound := make(map[string]bool)
	for _, r := range fullMethods(b.f) {
		if service := r.pkg + "." + r.service; !bound[service] {
			bound[service] = true
			for _, name := range names {
				b.identity(Registration, NotOneof, b.inFile(strings.ReplaceAll(name, "<Svc>", r.service)), schemaFile, r.pkg, r.service)
			}
		}
	}
}

// rpc is one method of a service, as a full method name spells it.
type rpc struct{ pkg, service, method string }

// fullMethods is every RPC a full method name literal of f names, once each.
func fullMethods(f *ast.File) []rpc {
	var rpcs []rpc
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if m := fullMethod.FindStringSubmatch(value); err == nil && m != nil {
			if one := (rpc{pkg: strings.TrimSuffix(m[1], "."), service: m[2], method: m[3]}); !slices.Contains(rpcs, one) {
				rpcs = append(rpcs, one)
			}
		}
		return true
	})
	return rpcs
}

// inFile is the object b's package declares by name in b's file, or nil.
func (b *binder) inFile(name string) types.Object {
	obj := b.p.Types.Scope().Lookup(name)
	if obj == nil || obj.Pos() < b.f.FileStart || obj.Pos() >= b.f.FileEnd {
		return nil
	}
	return obj
}

// rpcMethod publishes, in role, the method of the interface named by obj that r names.
func (b *binder) rpcMethod(role bindingRole, obj types.Object, schemaFile string, r rpc) {
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return
	}
	for method := range iface.ExplicitMethods() {
		if method.Name() == r.method {
			b.identity(role, NotOneof, method, schemaFile, r.pkg, r.service+"."+r.method)
		}
	}
}

package graph

import (
	"go/ast"
	"go/build/constraint"
	"go/types"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"golang.org/x/tools/go/packages"
)

// The spellings the generator, go-fuzz and go-swagger roots read.
const (
	generateDirective = "//go:generate "
	goFuzzTag         = "gofuzz"
	swaggerDirective  = "swagger:"
	currentPackage    = "."
	goCommand         = "go"
	sourceFlag        = "source"
	typeFlag          = "type"
)

// The generators whose inputs a go:generate directive names, each by the base name
// of the program it runs. A type generator reads the types its -type flag lists from
// the directive's own package; mockgen reads the interfaces of the file its -source
// flag names, or the names its last argument lists from the package before it.
const (
	mockgenGenerator = "mockgen"
	stringer         = "stringer"
	enumer           = "enumer"
)

// goValueFlags are the build flags of go run that take a value, which may be the next
// word: https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies
var goValueFlags = map[string]bool{
	"C": true, "asmflags": true, "buildmode": true, "compiler": true, "covermode": true,
	"coverpkg": true, "exec": true, "gccgoflags": true, "gcflags": true, "installsuffix": true,
	"ldflags": true, "mod": true, "modfile": true, "overlay": true, "p": true, "pgo": true,
	"pkgdir": true, "tags": true, "toolexec": true,
}

// mockgenBooleanFlags are the mockgen flags that take no separate value, so the word
// after one is an argument.
var mockgenBooleanFlags = map[string]bool{
	"typed": true, "version": true, "debug_parser": true,
	"write_package_comment": true, "write_source_comment": true, "write_generate_directive": true,
}

// generatorInputs roots every declaration a go:generate directive of f names as the
// input of a generator: deleting one breaks go generate, whatever reads its output.
func (d *rootDetection) generatorInputs(p *packages.Package, f *ast.File) error {
	if p.Types == nil {
		return nil
	}
	file := d.pos.fset.Position(f.FileStart).Filename
	for _, group := range f.Comments {
		for _, c := range group.List {
			line, ok := strings.CutPrefix(c.Text, generateDirective)
			if !ok {
				continue
			}
			for _, obj := range d.generatorNames(p, file, generateWords(line, filepath.Base(file), p.Name)) {
				if err := d.rootInput(obj); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// rootInput roots one generator input, and every method an input interface declares,
// which the generator reads as well.
func (d *rootDetection) rootInput(obj types.Object) error {
	if err := d.addAt(obj.Pos(), RootGenerate); err != nil {
		return err
	}
	if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
		for method := range iface.ExplicitMethods() {
			if err := d.addAt(method.Pos(), RootGenerate); err != nil {
				return err
			}
		}
	}
	return nil
}

// generatorNames is every declaration one directive's words name as a generator's
// input.
func (d *rootDetection) generatorNames(p *packages.Package, file string, words []string) []types.Object {
	name, args := generatorOf(words)
	switch name {
	case stringer, enumer:
		return lookup([]*types.Package{p.Types}, flagValue(args, typeFlag, nil))
	case mockgenGenerator:
		if source := flagValue(args, sourceFlag, mockgenBooleanFlags); source != "" {
			return d.interfacesOf(p, filepath.Join(filepath.Dir(file), source))
		}
		positional := positionalArgs(args, mockgenBooleanFlags)
		if len(positional) < 2 {
			return nil
		}
		from, names := positional[len(positional)-2], positional[len(positional)-1]
		scopes := []*types.Package{p.Types}
		if from != currentPackage {
			scopes = nil
			for _, variant := range d.byPath[from] {
				scopes = append(scopes, variant.Types)
			}
		}
		return lookup(scopes, names)
	default:
		return nil
	}
}

// generateWords splits one directive the way go generate does, at spaces and tabs with a
// double-quoted Go string one word, then expands the file and package variables in
// each word: https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source
func generateWords(line, file, pkg string) []string {
	expand := strings.NewReplacer("$GOFILE", file, "${GOFILE}", file, "$GOPACKAGE", pkg, "${GOPACKAGE}", pkg)
	var words []string
	for line = strings.TrimLeft(line, " \t"); line != ""; line = strings.TrimLeft(line, " \t") {
		word, rest := line, ""
		if line[0] == '"' {
			end := closingQuote(line)
			unquoted, err := strconv.Unquote(line[:end])
			if err != nil {
				return nil
			}
			word, rest = unquoted, line[end:]
		} else if at := strings.IndexAny(line, " \t"); at >= 0 {
			word, rest = line[:at], line[at:]
		}
		words = append(words, expand.Replace(word))
		line = rest
	}
	return words
}

// closingQuote is the length of the double-quoted Go string line starts with, through its
// closing quote, or the whole line where none closes it.
func closingQuote(line string) int {
	for at := 1; at < len(line); at++ {
		switch line[at] {
		case '\\':
			at++
		case '"':
			return at + 1
		}
	}
	return len(line)
}

// generatorOf is the base name of the program one directive runs, without a module
// version, and the words it passes it: `go run <package>` and `go tool <name>` run
// the program they name.
func generatorOf(words []string) (name string, args []string) {
	if len(words) == 0 {
		return "", nil
	}
	program, args := words[0], words[1:]
	if program == goCommand && len(args) > 1 && (args[0] == "run" || args[0] == "tool") {
		at := 1
		for at < len(args) && strings.HasPrefix(args[at], "-") {
			flag, _, joined := strings.Cut(strings.TrimLeft(args[at], "-"), "=")
			if !joined && goValueFlags[flag] {
				at++
			}
			at++
		}
		if at >= len(args) {
			return "", nil
		}
		program, args = args[at], args[at+1:]
	}
	program, _, _ = strings.Cut(program, "@")
	return path.Base(filepath.ToSlash(program)), args
}

// flagValue is the value one flag carries among args, in its -name=value or
// -name value form, and the empty string where args do not set it.
func flagValue(args []string, name string, boolean map[string]bool) string {
	for i := 0; i < len(args); i++ {
		flag, value, joined := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || boolean[flag] {
			continue
		}
		if !joined {
			value = ""
			// A boolean flag written before another flag takes no value.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				value = args[i+1]
				i++
			}
		}
		if flag == name {
			return value
		}
	}
	return ""
}

// positionalArgs is every word of args that is neither a flag nor a flag's value.
func positionalArgs(args []string, boolean map[string]bool) []string {
	var held []string
	for i := 0; i < len(args); i++ {
		word := args[i]
		if !strings.HasPrefix(word, "-") {
			held = append(held, word)
			continue
		}
		flag, _, joined := strings.Cut(strings.TrimLeft(word, "-"), "=")
		if !joined && !boolean[flag] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return held
}

// lookup is each comma-separated name in the first scope that declares it.
func lookup(scopes []*types.Package, names string) []types.Object {
	var held []types.Object
	for name := range strings.SplitSeq(names, ",") {
		for _, scope := range scopes {
			if obj := scope.Scope().Lookup(strings.TrimSpace(name)); obj != nil {
				held = append(held, obj)
				break
			}
		}
	}
	return held
}

// interfacesOf is every interface type the file of p at that path declares.
func (d *rootDetection) interfacesOf(p *packages.Package, file string) []types.Object {
	var held []types.Object
	for _, f := range p.Syntax {
		if filepath.Clean(d.pos.fset.Position(f.FileStart).Filename) != filepath.Clean(file) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if _, isInterface := spec.Type.(*ast.InterfaceType); isInterface && p.TypesInfo.Defs[spec.Name] != nil {
				held = append(held, p.TypesInfo.Defs[spec.Name])
			}
			return true
		})
	}
	return held
}

// goFuzzFile reports whether a go-fuzz build may compile f: its build constraint
// holds with the gofuzz tag set, for some setting of its other tags. A fuzz build sets
// the tag, and a file whose constraint names no gofuzz tag compiles either way.
func goFuzzFile(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			if expr, err := constraint.Parse(c.Text); err == nil && !buildconstraint.HoldsWith(expr, goFuzzTag, true) {
				return false
			}
		}
	}
	return true
}

// goFuzzEntry reports whether fn is a go-fuzz entry point: Fuzz or a name it opens
// as a test family's prefix does, of type func([]byte) int.
func goFuzzEntry(p *packages.Package, fn *ast.FuncDecl) bool {
	obj, _ := p.TypesInfo.Defs[fn.Name].(*types.Func)
	if obj == nil || !hasTestPrefix(fn.Name.Name, "Fuzz") || fn.Type.TypeParams.NumFields() > 0 {
		return false
	}
	sig, _ := obj.Type().(*types.Signature)
	if sig == nil || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	slice, _ := types.Unalias(sig.Params().At(0).Type()).(*types.Slice)
	return slice != nil && types.Identical(slice.Elem(), types.Typ[types.Byte]) &&
		types.Identical(sig.Results().At(0).Type(), types.Typ[types.Int])
}

// swaggerDeclarations roots every declaration of f whose doc comment holds a
// go-swagger directive, and every field of a struct type it declares, an anonymous
// struct's included: the generator reads each one.
func (d *rootDetection) swaggerDeclarations(f *ast.File) error {
	for _, decl := range f.Decls {
		if err := d.swaggerDeclaration(decl); err != nil {
			return err
		}
	}
	return nil
}

// swaggerDeclaration roots one top-level declaration a swagger directive names.
func (d *rootDetection) swaggerDeclaration(decl ast.Decl) error {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		if swaggerDoc(decl.Doc) {
			return d.addAt(decl.Name.Pos(), RootSwagger)
		}
	case *ast.GenDecl:
		for _, spec := range decl.Specs {
			if err := d.swaggerSpec(spec, swaggerDoc(decl.Doc)); err != nil {
				return err
			}
		}
	}
	return nil
}

// swaggerSpec roots one specification a swagger directive names, through its own doc
// comment or through its declaration's.
func (d *rootDetection) swaggerSpec(spec ast.Spec, declared bool) error {
	switch spec := spec.(type) {
	case *ast.TypeSpec:
		if !declared && !swaggerDoc(spec.Doc) {
			return nil
		}
		if err := d.addAt(spec.Name.Pos(), RootSwagger); err != nil {
			return err
		}
		return d.swaggerFields(spec.Type)
	case *ast.ValueSpec:
		if !declared && !swaggerDoc(spec.Doc) {
			return nil
		}
		for _, name := range spec.Names {
			if err := d.addAt(name.Pos(), RootSwagger); err != nil {
				return err
			}
		}
	}
	return nil
}

// swaggerFields roots every field of a struct type expression, through anonymous
// struct types.
func (d *rootDetection) swaggerFields(expr ast.Expr) error {
	structure, ok := expr.(*ast.StructType)
	if !ok {
		return nil
	}
	for _, field := range structure.Fields.List {
		for _, name := range field.Names {
			if err := d.addAt(name.Pos(), RootSwagger); err != nil {
				return err
			}
		}
		if err := d.swaggerFields(field.Type); err != nil {
			return err
		}
	}
	return nil
}

// swaggerDoc reports whether a doc comment holds a line opening with a swagger
// directive. The raw comments are read because [ast.CommentGroup.Text] drops a
// line spelled as a directive, as one written with no space after the slashes is.
func swaggerDoc(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		text := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"), "*/")
		for line := range strings.SplitSeq(text, "\n") {
			if strings.HasPrefix(strings.TrimLeft(line, " \t*"), swaggerDirective) {
				return true
			}
		}
	}
	return false
}

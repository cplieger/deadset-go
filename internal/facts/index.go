package facts

import (
	"go/ast"
	"go/token"
	"iter"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"golang.org/x/tools/go/packages"
)

// Index is one loaded configuration's syntax in the one walk order, and the
// generated files it holds, built once per configuration. Every pass after the
// load reads the target's packages and files through it.
type Index struct {
	outside    outside
	fset       *token.FileSet
	published  *Published
	instances  *Instantiations
	callees    *Callees
	readings   *Readings
	consumers  map[*load.Consumer][]*packages.Package
	caps       map[inventory.SymbolID]Cap
	packages   []*packages.Package
	order      []*load.Consumer
	generated  []Generated
	bindings   []Binding
	interfaces []Interface
	reads      []SchemaRead
}

// Generated is one generated file of the target: its target-relative path, which a
// line directive does not move, the 1-based line of the header comment that marks
// it and that comment's text, the site of its package clause, and, where a row of
// contract/generators.json matches it, the row, the version the output states and
// the schema files it names.
type Generated struct {
	Path        string
	Detail      string
	Generator   string
	Version     string
	SchemaFiles []string
	Site        token.Position
	Line        int
}

// The two ends of the header comment [ast.IsGenerated] matches.
const (
	generatedOpen  = "// Code generated "
	generatedClose = " DO NOT EDIT."
)

// NewIndex indexes r. A generated file's package clause that does not render is
// [inventory.ErrSource], and one its generator row cannot decode an error naming the row.
func NewIndex(r *load.Result, rs *resolve.Resolver) (*Index, error) {
	referencing := r.Referencing()
	x := &Index{
		fset:      r.Fset,
		readings:  readingsOver(r.Fset),
		packages:  inventory.Packages(r.Packages, r.Fset),
		consumers: make(map[*load.Consumer][]*packages.Package, len(referencing)),
	}
	for i := range referencing {
		x.consumers[&referencing[i]] = inventory.Packages(referencing[i].Packages, r.Fset)
		x.order = append(x.order, &referencing[i])
	}
	generatedFiles := make(map[string]generatedFile)
	for p, f := range x.Sources() {
		if !ast.IsGenerated(f) {
			continue
		}
		site, err := rs.Render(f.Package)
		if err != nil {
			return nil, err
		}
		g := Generated{Site: site, Path: rs.Positions().File(f.Package)}
		generatedFiles[rs.Positions().Offset(f.Package).File] = generatedFile{path: g.Path, pkg: p.PkgPath}
		g.Line, g.Detail = header(r.Fset, f)
		if row := recognise(&g, f); row != nil {
			held, err := bind(rs, p, f, &g, row)
			if err != nil {
				return nil, err
			}
			x.bindings = append(x.bindings, held...)
		}
		x.generated = append(x.generated, g)
	}
	slices.SortFunc(x.generated, func(a, b Generated) int { return scope.Order(a.Path, b.Path) })
	x.generated = slices.CompactFunc(x.generated, func(a, b Generated) bool { return scope.SameName(a.Path, b.Path) })
	x.interfaces = interfacesOf(x, rs)
	x.outside = outsideOf(x)
	x.instances = instantiationsOf(x.Program(), rs.Positions())
	x.callees = calleesOf(x.Program(), rs.Positions())
	x.published = publishedOf(x, rs)
	x.reads, x.caps = schemaReadsOf(x, rs, generatedFiles)
	return x, nil
}

// header is the line and text of the header comment that marks f as generated.
// [ast.IsGenerated] decided that f has one and exports neither, so this reads the
// line it matched and decides nothing.
func header(fset *token.FileSet, f *ast.File) (line int, text string) {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, generatedOpen) && strings.HasSuffix(c.Text, generatedClose) {
				return fset.PositionFor(c.Pos(), false).Line, c.Text
			}
		}
	}
	return fset.PositionFor(f.Package, false).Line, ""
}

// Packages is every package variant of the target, in the walk order.
func (x *Index) Packages() []*packages.Package { return x.packages }

// Typed is every package variant of the target the type checker read, in the
// walk order.
func (x *Index) Typed() iter.Seq[*packages.Package] {
	return func(yield func(*packages.Package) bool) {
		for _, p := range x.packages {
			if p.Types != nil && p.TypesInfo != nil && !yield(p) {
				return
			}
		}
	}
}

// Files is one package variant's syntax trees in the walk order.
func (x *Index) Files(p *packages.Package) []*ast.File { return inventory.Files(p, x.fset) }

// Sources yields every source file of the target once, with the first variant
// that compiles it.
func (x *Index) Sources() iter.Seq2[*packages.Package, *ast.File] {
	return inventory.Sources(x.packages, x.fset)
}

// Program is every package variant of the analysed program: the target's, then
// every loaded consumer's in [load.Result.Referencing] order.
func (x *Index) Program() []*packages.Package {
	found := slices.Clone(x.packages)
	for i := range x.order {
		found = append(found, x.consumers[x.order[i]]...)
	}
	return found
}

// Instantiations is every type argument each type parameter takes in an
// instantiation the program holds.
func (x *Index) Instantiations() *Instantiations { return x.instances }

// Callees is which functions each call of the program reaches.
func (x *Index) Callees() *Callees { return x.callees }

// Readings compares the types of the configuration's readings of one package.
func (x *Index) Readings() *Readings { return x.readings }

// Bindings is every binding identity the target's generated files publish.
func (x *Index) Bindings() []Binding { return x.bindings }

// SchemaReads is every read the target's hand-written serializer calls and enum
// name conversions make of generated declarations.
func (x *Index) SchemaReads() []SchemaRead { return x.reads }

// Caps is the cap of every consumer binding hand-written code gives one
// (grammar/analysis/references-go.md references-go.17), by its declaration.
func (x *Index) Caps() map[inventory.SymbolID]Cap { return x.caps }

// Generated is every generated file of the target, by path.
func (x *Index) Generated() []Generated { return x.generated }

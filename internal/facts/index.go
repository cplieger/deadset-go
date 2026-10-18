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
	"golang.org/x/tools/go/packages"
)

// Index is one loaded configuration's syntax in the one walk order, and the
// generated files it holds, built once per configuration. Every pass after the
// load reads the target's packages and files through it.
type Index struct {
	fset       *token.FileSet
	packages   []*packages.Package
	consumers  map[*load.Consumer][]*packages.Package
	order      []*load.Consumer
	generated  []Generated
	bindings   []Binding
	interfaces []Interface
}

// Generated is one generated file of the target: the path the report spells it
// with, the 1-based line of the header comment that marks it and that comment's
// text, the site of its package clause, and, where a row of
// contract/generators.json matches it, the row, the version the output states
// and the schema files it names.
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
	x := &Index{
		fset:      r.Fset,
		packages:  inventory.Packages(r.Packages, r.Fset),
		consumers: make(map[*load.Consumer][]*packages.Package, len(r.Consumers)),
	}
	for i := range r.Consumers {
		x.consumers[&r.Consumers[i]] = inventory.Packages(r.Consumers[i].Packages, r.Fset)
		x.order = append(x.order, &r.Consumers[i])
	}
	for p, f := range x.Sources() {
		if !ast.IsGenerated(f) {
			continue
		}
		site, err := rs.Render(f.Package)
		if err != nil {
			return nil, err
		}
		g := Generated{Site: site, Path: site.Filename}
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
	slices.SortFunc(x.generated, func(a, b Generated) int { return strings.Compare(a.Path, b.Path) })
	x.generated = slices.CompactFunc(x.generated, func(a, b Generated) bool { return a.Path == b.Path })
	x.interfaces = interfacesOf(x, rs)
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
// every loaded consumer's in the order the scope declares them.
func (x *Index) Program() []*packages.Package {
	found := slices.Clone(x.packages)
	for i := range x.order {
		found = append(found, x.consumers[x.order[i]]...)
	}
	return found
}

// Bindings is every binding identity the target's generated files publish.
func (x *Index) Bindings() []Binding { return x.bindings }

// Generated is every generated file of the target, by path.
func (x *Index) Generated() []Generated { return x.generated }

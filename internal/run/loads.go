package run

import (
	"context"
	"fmt"
	"go/token"
	"hash/fnv"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/findings"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"golang.org/x/tools/go/packages"
)

// loads is every configuration's load with the declarations enumerated from it, the
// resolver and the facts index over them, which every stage after the load reads one
// configuration at a time ([findings.Loads]). Outside memory pressure it keeps each
// configuration it loaded; under pressure it drops them ([memory.OnPressureOf]) and
// keeps only the one it last handed out, loading any other again over the matrix's
// own trees and dependency checks ([load.Matrix]), so a run that does not fit with
// every configuration held runs with one at a time.
type loads struct {
	// ctx is the run's: a configuration loaded again is part of the load stage,
	// which the cancellation of the run ends wherever a later stage asks for it.
	ctx     context.Context
	matrix  *load.Matrix
	pressed func() bool
	root    string

	configurations []load.Configuration
	outlines       []outline
	digests        []loadDigest // each configuration's, [digestOf]

	held []*findings.Configured
	mu   sync.Mutex
}

// outline is what a report reads of one configuration's load beyond its
// declarations, kept for the whole run because it holds no syntax or types.
type outline struct {
	units         []load.Unit // without their packages
	consumers     []string    // the declared consumers' identifiers, in scope order
	dropped       []load.Dropped
	excludedByCgo []string
	hidden        []string // [load.Result.Hidden]
}

func newLoads(ctx context.Context, matrix *load.Matrix, root string) *loads {
	l := &loads{ctx: ctx, matrix: matrix, root: root, pressed: memory.Pressed}
	memory.OnPressureOf(l, (*loads).drop)
	return l
}

// Len is the number of configurations the run analyzes.
func (l *loads) Len() int { return len(l.configurations) }

// At is the configuration at config, loaded again where pressure dropped it.
func (l *loads) At(config int) (*findings.Configured, error) {
	l.mu.Lock()
	one := l.held[config]
	l.mu.Unlock()
	if one != nil {
		return one, nil
	}
	// The load runs outside the lock, so a drop under pressure never waits for it.
	result, err := l.matrix.Load(l.ctx, &l.configurations[config])
	if err != nil {
		return nil, fmt.Errorf("load %s again: %w", l.configurations[config].ID, err)
	}
	one, err = configuredOf(&result, l.root)
	if err != nil {
		return nil, err
	}
	if changed, differs := l.digests[config].differs(digestOf(one)); differs {
		return nil, fmt.Errorf("load %s again: %s", l.configurations[config].ID, changed)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keep(config, one)
	return one, nil
}

// add records the next configuration's first load.
func (l *loads) add(one *findings.Configured) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := one.Result
	units := make([]load.Unit, len(r.Units))
	for i, unit := range r.Units {
		units[i] = unit
		units[i].Packages = nil
	}
	consumers := make([]string, len(r.Consumers))
	for i := range r.Consumers {
		consumers[i] = r.Consumers[i].ID
	}
	l.configurations = append(l.configurations, r.Configuration)
	l.outlines = append(l.outlines, outline{units: units, consumers: consumers, dropped: r.Dropped, excludedByCgo: r.ExcludedByCgo, hidden: r.Hidden})
	l.digests = append(l.digests, digestOf(one))
	l.held = append(l.held, nil)
	l.keep(len(l.held)-1, one)
}

// keep holds one configuration, and under pressure it alone.
func (l *loads) keep(config int, one *findings.Configured) {
	if l.pressed() {
		clear(l.held)
	}
	l.held[config] = one
}

// drop forgets every configuration it holds; a caller still reading one keeps it
// until it is done.
func (l *loads) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.held)
}

// configuredOf enumerates one configuration's declarations, with the resolver and
// the facts index over them.
func configuredOf(result *load.Result, root string) (*findings.Configured, error) {
	inv, err := inventory.Of(result, root, os.ReadFile)
	if err != nil {
		return nil, err
	}
	resolver, err := resolve.New(result, inv)
	if err != nil {
		return nil, err
	}
	index, err := facts.NewIndex(result, resolver)
	if err != nil {
		return nil, err
	}
	return &findings.Configured{Result: result, Resolve: resolver, Facts: index, Symbols: inv.Symbols}, nil
}

// loadDigest is what a second load of a configuration must reproduce, since every
// answer the run kept from the first load names its declarations and was read
// from its packages: the declarations' identifiers in order, and per package
// read, its files, its module and its imports.
type loadDigest struct {
	packages     []packageDigest // ordered by id
	declarations uint64
}

// packageDigest is one package's share of a [loadDigest], one digest per part
// ([packageParts]), so a reload that differs names the part.
type packageDigest struct {
	id    string
	parts [len(packageParts)]uint64
}

// packageParts names each part of a [packageDigest], in [packageDigestOf]'s order.
var packageParts = [...]string{"name", "module", "compiled files", "parsed files", "imports"}

// digestOf is one configuration's [loadDigest].
func digestOf(one *findings.Configured) loadDigest {
	h := fnv.New64a()
	for i := range one.Symbols {
		h.Write([]byte(one.Symbols[i].ID))
		h.Write([]byte{0})
	}
	d := loadDigest{declarations: h.Sum64()}
	r := one.Result
	roots := slices.Clone(r.Packages)
	for _, consumer := range r.Referencing() {
		roots = append(roots, consumer.Packages...)
	}
	seen := make(map[string]bool)
	packages.Visit(roots, nil, func(p *packages.Package) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		d.packages = append(d.packages, packageDigest{id: p.ID, parts: packageDigestOf(r.Fset, p)})
	})
	slices.SortFunc(d.packages, func(a, b packageDigest) int { return strings.Compare(a.id, b.id) })
	return d
}

// packageDigestOf digests each of one package's [packageParts]: its path and name,
// its module, its compiled files, the name and size of each parsed one, and the ids
// of its imports.
func packageDigestOf(fset *token.FileSet, p *packages.Package) [len(packageParts)]uint64 {
	var module, parsed []string
	if m := p.Module; m != nil {
		module = append(module, m.Path, m.Version, m.Dir)
		if m.Replace != nil {
			module = append(module, m.Replace.Path, m.Replace.Version, m.Replace.Dir)
		}
	}
	for _, f := range p.Syntax {
		if file := fset.File(f.FileStart); file != nil {
			parsed = append(parsed, file.Name(), strconv.Itoa(file.Size()))
		}
	}
	imports := make([]string, 0, len(p.Imports))
	for _, imported := range p.Imports {
		imports = append(imports, imported.ID)
	}
	slices.Sort(imports)
	return [...]uint64{
		digest(p.PkgPath, p.Name),
		digest(module...),
		digest(p.CompiledGoFiles...),
		digest(parsed...),
		digest(imports...),
	}
}

// digest is the hash of parts, each ended by a zero byte.
func digest(parts ...string) uint64 {
	h := fnv.New64a()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

// differs says how got first differs from the first load d: the package, and the
// part of it, or else the declarations; false where nothing does.
func (d loadDigest) differs(got loadDigest) (string, bool) {
	for i, j := 0, 0; i < len(d.packages) || j < len(got.packages); {
		switch {
		case j == len(got.packages) || i < len(d.packages) && d.packages[i].id < got.packages[j].id:
			return "package " + d.packages[i].id + " of its first load is not read", true
		case i == len(d.packages) || got.packages[j].id < d.packages[i].id:
			return "package " + got.packages[j].id + " is read, which its first load did not", true
		}
		for part := range packageParts {
			if d.packages[i].parts[part] != got.packages[j].parts[part] {
				return "package " + d.packages[i].id + " differs from its first load in its " + packageParts[part], true
			}
		}
		i, j = i+1, j+1
	}
	if d.declarations != got.declarations {
		return "the declarations differ from its first load", true
	}
	return "", false
}

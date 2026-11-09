package findings

import (
	"fmt"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/deps"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The words the Contract's subject vocabulary spells these subjects with. A file is
// also the word the framework derives from the kind of a declaration of the
// inventory, because the inventory holds one symbol per source file.
const (
	dependencySubject = "dependency"
	directiveSubject  = "module-directive"
	fileSubject       = "file"
)

// requireSection is the section of the module file that declares a dependency,
// which is both the selector its reference carries and the class a finding names.
const requireSection = "require"

// replaceSelector ends the reference of a replace directive.
const replaceSelector = ":replace"

// goSuffix is the extension of the files a build configuration decides.
const goSuffix = ".go"

// versionSeparator joins a module path to its version in a symbol reference, which
// is the one spelling that writes the two as one word.
const versionSeparator = "@"

// versionGap joins a module path to its version the way a module file writes the
// two, which is as two words of one directive.
const versionGap = " "

// fileNeverBuilt reports a source file of the target that no configuration of the
// build matrix compiles, naming the build constraint that excluded it. It reports
// only under a listed matrix declared complete: a derived matrix holds only the
// configurations the tree's own build atoms imply, so a file it misses is no
// evidence. A file ignored only because it imports "C" is excluded by cgo instead,
// which no configuration decides; one the cgo build tag excluded stays.
func fileNeverBuilt(in *pass) ([]draft, error) {
	if !completeMatrix(in) {
		return nil, nil
	}
	ignored, err := neverBuilt(in)
	if err != nil {
		return nil, err
	}
	found := make([]draft, 0, len(ignored))
	for _, file := range ignored {
		one := findingAt(vocab.FileNeverBuilt, Subject{
			Ref:       inventory.Ref(inventory.KindFile, file.pkgPath, []string{file.name}),
			Kind:      fileSubject,
			Name:      file.name,
			SizeLines: file.lines,
		}, Position{Path: file.path, Line: 1, Column: 1, EndLine: file.lines},
			"no configuration of the build matrix compiles file "+file.name)
		one.Details.ExcludedBy = file.constraint
		found = append(found, one)
	}
	return found, nil
}

// completeMatrix reports whether the run analyzes a matrix the configuration both
// listed and declared complete, the one declaration under which a file no
// configuration builds is a finding: completeness declared over no listed
// configuration is of a derived matrix, which is incomplete.
func completeMatrix(in *pass) bool {
	return in != nil && in.Config != nil &&
		in.Config.Analysis.Matrix.Complete && len(in.Config.Analysis.Platforms) > 0
}

// ignoredFile is one file of the target no configuration of the matrix compiled.
type ignoredFile struct {
	path       string // target-relative, forward slashes
	name       string // the base name, which is what the file's reference spells
	pkgPath    string // the import path of the directory that holds it
	constraint string // the constraint that excluded it, as the file spells it
	lines      int    // the number of source lines the file spans
}

// neverBuilt is every Go file of the target that every configuration of the run
// ignored, in path order, those excluded by cgo dropped, each with the constraint
// read from the file. A directory whose every file a constraint excludes holds no
// package, so the load, and this population, never reach it; the report records
// that limit.
func neverBuilt(in *pass) ([]ignoredFile, error) {
	ignored := in.artifacts.ignored
	if len(ignored) == 0 {
		return nil, nil
	}
	cgo := in.artifacts.cgo
	files := make([]ignoredFile, 0, len(ignored))
	for _, path := range slices.Sorted(maps.Keys(ignored)) {
		relative := load.RelativeSlash(in.Root, path)
		if cgo[relative] {
			continue
		}
		file, reported, err := readIgnored(path, relative, ignored[path])
		if err != nil {
			return nil, err
		}
		if !reported {
			continue
		}
		files = append(files, file)
	}
	return files, nil
}

// artifactFacts is what the module-artifact kinds read of the configurations'
// loads, gathered in one visit per configuration ([artifactFacts.read]).
type artifactFacts struct {
	in *pass

	// ignored is every Go file each configuration read ignored, by absolute path,
	// with the import path of the tested package: a test variant lists its
	// package's ignored files and names that package in ForTest.
	ignored map[string]string
	// cgo is every target-relative path one configuration excluded by cgo alone,
	// since a file importing "C" imports it under every configuration.
	cgo map[string]bool

	// last is the union over the configurations of the modules each deletion
	// candidate holds the last use of ([joinLastUses]).
	last map[inventory.SymbolID][]string

	// requires and replaces are what every configuration read so far answers about
	// the unit of each module file of [Input.Modules], by its index ([agreement]).
	requires []agreement[deps.Requirement]
	replaces []agreement[deps.Replacement]

	configurations int // the configurations read
}

func newArtifactFacts(in *pass) *artifactFacts {
	return &artifactFacts{
		in:       in,
		cgo:      make(map[string]bool),
		requires: make([]agreement[deps.Requirement], len(in.Modules)),
		replaces: make([]agreement[deps.Replacement], len(in.Modules)),
		last:     make(map[inventory.SymbolID][]string),
	}
}

// read adds one configuration's answers.
func (f *artifactFacts) read(one *Configured) {
	under := ignoredUnder(one.Result)
	if f.configurations == 0 {
		f.ignored = under
	}
	for path := range f.ignored {
		if _, also := under[path]; !also {
			delete(f.ignored, path)
		}
	}
	if one.Result != nil {
		for _, path := range one.Result.ExcludedByCgo {
			f.cgo[path] = true
		}
	}
	for i := range f.in.Modules {
		m := &f.in.Modules[i]
		unit := unitOf(one.Result, m.Dir)
		f.requires[i].add(f.configurations == 0, unit, func() []deps.Requirement { return deps.UnusedRequirements(m.File, unit) })
		f.replaces[i].add(f.configurations == 0, unit, func() []deps.Replacement { return deps.NoopReplacements(m.File, unit) })
	}
	if f.in.Sweep != nil && one.Result != nil {
		// A last use is a module's last use inside one unit, whose own module
		// file is what the deletion leaves requiring it.
		for i := range one.Result.Units {
			unit := one.Result.Unit(one.Result.Units[i].Dir)
			joinLastUses(f.last, deps.LastUses(unit, one.Resolve, f.in.Sweep.Candidates))
		}
	}
	f.configurations++
}

// agreement is what every configuration read so far answers about the unit of one
// module file, in the order the first answers it, and nothing once a configuration
// loaded no such unit.
type agreement[T comparable] struct {
	kept   []T
	absent bool
}

// add folds one configuration's answer in, answer reading the unit, nil where the
// configuration loaded none.
func (a *agreement[T]) add(first bool, unit *load.Result, answer func() []T) {
	if a.absent {
		return
	}
	if unit == nil {
		a.absent, a.kept = true, nil
		return
	}
	also := answer()
	if first {
		a.kept = also
		return
	}
	a.kept = slices.DeleteFunc(a.kept, func(v T) bool { return !slices.Contains(also, v) })
}

// ignoredUnder is every Go file one configuration ignored, keyed by absolute path,
// with the import path of the directory that holds it.
func ignoredUnder(r *load.Result) map[string]string {
	ignored := make(map[string]string)
	if r == nil {
		return ignored
	}
	for _, p := range r.Packages {
		pkgPath := p.PkgPath
		if p.ForTest != "" {
			pkgPath = p.ForTest
		}
		for _, path := range p.IgnoredFiles {
			// IgnoredFiles carries every ignored file, assembly and C sources
			// included, and only a Go file is a subject of this kind.
			if filepath.Ext(path) != goSuffix {
				continue
			}
			if held, seen := ignored[path]; !seen || len(pkgPath) < len(held) {
				ignored[path] = pkgPath
			}
		}
	}
	return ignored
}

// readIgnored reads one ignored file, the constraint that excluded it and the
// number of lines it spans, and reports whether the file is a subject of the kind.
// A file whose constraint holds only under the ignore tag is not one: the toolchain
// keeps it out of ordinary builds, and the analysis leaves it outside. A file another
// custom tag excluded is one, because a complete matrix lists every configuration. A
// file no constraint decides is an error, because the toolchain ignored every file
// of this population, so a constraint this rule cannot read is one it misreads.
func readIgnored(path, relative, pkgPath string) (ignoredFile, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return ignoredFile{}, false, fmt.Errorf("read the ignored file %s: %w", relative, err)
	}
	base := filepath.Base(path)
	expression, err := buildconstraint.Of(base, content)
	if err != nil {
		return ignoredFile{}, false, fmt.Errorf("read the build constraint of %s: %w", relative, err)
	}
	if expression == nil {
		return ignoredFile{}, false, fmt.Errorf("%w: %s declares no build constraint and no configuration built it",
			errEmitter, relative)
	}
	if buildconstraint.OnlyUnderIgnore(expression) {
		return ignoredFile{}, false, nil
	}
	return ignoredFile{
		path:       relative,
		name:       base,
		pkgPath:    pkgPath,
		constraint: expression.String(),
		lines:      max(1, strings.Count(string(content), "\n")+1),
	}, true, nil
}

// fileNeverImported reports a source file of a package that no import reaches,
// that no root names, and whose every declaration the sweep found dead: the file
// falls with its package. A package main is built whatever imports it, and a test
// package reaches its declarations through its test roots, so neither is a subject.
func fileNeverImported(in *pass) ([]draft, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	unreached := unreachedPackages(in)
	var found []draft
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind != inventory.KindFile || !unreached[in.packageOf(symbol)] {
			continue
		}
		one, held := in.finding(symbol.ID, vocab.FileNeverImported,
			"no import reaches the package file "+symbol.Name+" declares and no root names a declaration in it")
		if !held {
			continue
		}
		found = append(found, one)
	}
	return found, nil
}

// unreachedPackages is every package of the target, by [pass.packageKey], that no
// import reaches, no root names, and whose every declaration is a candidate of the
// sweep, a package whose files declare nothing among them. A declaration the sweep
// did not judge dead keeps the package's files out of the set whatever held it
// live, which for a package nothing imports is an exemption: a use no reference
// names reaches that declaration, so the file it is written in does not fall.
func unreachedPackages(in *pass) map[string]bool {
	imported := in.importedPackages()
	rooted := in.rootedPackages()

	files := make(map[string]bool)
	live := make(map[string]int)
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		switch {
		case symbol.Kind == inventory.KindFile:
			files[in.packageOf(symbol)] = true
		case symbol.Kind == inventory.KindPackage:
		case in.candidateOf(symbol.ID) == nil:
			live[in.packageOf(symbol)]++
		}
	}

	unreached := make(map[string]bool, len(files))
	for pkg := range files {
		if imported[pkg] || rooted[pkg] || in.mains[pkg] {
			continue
		}
		unreached[pkg] = live[pkg] == 0
	}
	return unreached
}

// importedPackages is every package of the target, by [pass.packageKey], an
// import from outside it reaches.
//
// An import is a reference to the package symbol the inventory holds for the
// imported package, which is the edge the reference pass records at an import
// spec. A reference a declaration of the package itself made is not an import of
// it, whatever names it, so only an edge from outside counts.
func (in *pass) importedPackages() map[string]bool {
	imported := make(map[string]bool)
	for i := range in.Merged.References {
		ref := &in.Merged.References[i]
		to := in.symbol(ref.To)
		if to == nil || to.Kind != inventory.KindPackage {
			continue
		}
		if from := in.symbol(ref.From); from != nil && in.packageOf(from) == in.packageOf(to) {
			continue
		}
		imported[in.packageOf(to)] = true
	}
	return imported
}

// rootedPackages is every package of the target, by [pass.packageKey], holding a
// declaration a root names.
func (in *pass) rootedPackages() map[string]bool {
	rooted := make(map[string]bool)
	for _, root := range in.Merged.Roots {
		if symbol := in.symbol(root.ID); symbol != nil {
			rooted[in.packageOf(symbol)] = true
		}
	}
	return rooted
}

// unusedDependency reports a direct requirement of a module file whose module
// provides no package any package of that file's module imports.
//
// A requirement marked indirect is never reported: it carries no import by
// construction and pins a transitive version under module graph pruning, so
// deleting it changes the build list. A requirement is reported only where every
// configuration of the matrix agrees, because a requirement one configuration
// imports is used.
func unusedDependency(in *pass) ([]draft, error) {
	var found []draft
	for i := range in.Modules {
		m := &in.Modules[i]
		for _, require := range in.artifacts.requires[i].kept {
			one := findingAt(vocab.UnusedDependency, Subject{
				Ref:       moduleFragment(m.Path, require.Path+":"+requireSection),
				Kind:      dependencySubject,
				Name:      require.Path,
				SizeLines: 1,
			}, in.directivePosition(m, require.Site),
				"no package of the target imports a package required module "+require.Path+" provides")
			one.Details.DependencyClass = requireSection
			found = append(found, one)
		}
	}
	return found, nil
}

// unusedReplace reports a replace directive whose replaced module is absent from
// its module's build list, which is the one case in which the directive redirects
// nothing.
//
// It is reported only where every configuration of the matrix agrees, because a
// module one configuration loads is in the build list.
func unusedReplace(in *pass) ([]draft, error) {
	var found []draft
	for i := range in.Modules {
		m := &in.Modules[i]
		for _, replace := range in.artifacts.replaces[i].kept {
			named := referenceSpelling(replace.Old)
			one := findingAt(vocab.UnusedModuleDirective, Subject{
				Ref:       moduleFragment(m.Path, named+replaceSelector),
				Kind:      directiveSubject,
				Name:      named,
				SizeLines: 1,
			}, in.directivePosition(m, replace.Site),
				"module "+named+", which this directive replaces, is absent from the build list")
			one.Details.Replacement = moduleFileSpelling(replace.New)
			found = append(found, one)
		}
	}
	return found, nil
}

// unitOf is one configuration's load narrowed to the unit rooted at dir, and nil
// where the configuration loaded no such unit.
func unitOf(r *load.Result, dir string) *load.Result {
	if r == nil {
		return nil
	}
	return r.Unit(dir)
}

// referenceSpelling renders one module the way a symbol reference names it: the
// path, and the version after an at sign where the directive gives one. It is the
// form the reference grammar fixes for a directive's subject, so two directives over
// one module at two versions are two references.
func referenceSpelling(m deps.Module) string {
	if m.Version == "" {
		return m.Path
	}
	return m.Path + versionSeparator + m.Version
}

// moduleFileSpelling renders one module the way the module file writes it: the path,
// and the version after a space where the directive gives one. A replacement that is
// a directory is that directory as the file spells it and carries no version.
func moduleFileSpelling(m deps.Module) string {
	if m.Version == "" {
		return m.Path
	}
	return m.Path + versionGap + m.Version
}

// moduleFragment is the reference of one subject a module file of the target
// declares: that file's module as the scope, and the fragment the subject's own
// grammar spells. The scope comes from the reference of a package so that the
// language prefix has one owner.
func moduleFragment(module, fragment string) string {
	return inventory.Ref(inventory.KindPackage, module, nil) + fragment
}

// directivePosition renders the position of a directive of one module file,
// relative to the target root, which spans the one line it is written on.
func (in *pass) directivePosition(m *ModuleFile, site token.Position) Position {
	return Position{
		Path:    load.RelativeSlash(in.Root, filepath.Join(m.Dir, site.Filename)),
		Line:    site.Line,
		Column:  site.Column,
		EndLine: site.Line,
	}
}

// annotate names, on every deletion finding whose subject holds the last use of a
// dependency, the modules that deletion removes the last use of.
//
// It is a completion step over the findings of every kind rather than a kind of
// its own: the join is the same whichever code reports the declaration, and only a
// deletable finding carries it, because narrowing a declaration or reporting a
// live one removes no use of anything. A run completes its findings once, so this
// runs after the pass that produced them.
func (in *pass) annotate(findings []Finding) {
	if len(findings) == 0 {
		return
	}
	last := in.artifacts.last
	if len(last) == 0 {
		return
	}
	for i := range findings {
		found := &findings[i]
		if found.Fixability != vocab.Deletable || found.id == "" {
			continue
		}
		if modules := last[found.id]; len(modules) > 0 {
			found.Details.RemovesLastUseOf = modules
		}
	}
}

// joinLastUses adds each module of found to its candidate's entry of union, once,
// in sorted order. The union is the right fold for this join and the intersection
// is not: a module whose last use one configuration writes inside the candidate
// loses that use when the candidate goes, whatever another configuration does, and
// the finding names what the deletion would remove.
func joinLastUses(union, found map[inventory.SymbolID][]string) {
	for id, modules := range found {
		for _, module := range modules {
			if at, held := slices.BinarySearch(union[id], module); !held {
				union[id] = slices.Insert(union[id], at, module)
			}
		}
	}
}

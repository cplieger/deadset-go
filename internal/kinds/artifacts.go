package kinds

import (
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/deps"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// The codes of the kinds whose subject is a non-code artifact: a source file, a
// module requirement and a module-file directive.
const (
	fileNeverBuiltCode    = "DS1501"
	fileNeverImportedCode = "DS1502"
	unusedDependencyCode  = "DS1601"
	unusedReplaceCode     = "DS1605"
)

// The words the Contract's subject vocabulary spells these subjects with. A file
// is also the word for a declaration of the inventory, so only the two module-file
// subjects are outside what the framework derives from a declaration's kind.
const (
	dependencySubject = "dependency"
	directiveSubject  = "module-directive"
)

// requireSection is the section of the module file that declares a dependency,
// which is both the selector its reference carries and the class a finding names.
const requireSection = "require"

// replaceSelector ends the reference of a replace directive.
const replaceSelector = ":replace"

// goSuffix is the extension of the files a build configuration decides.
const goSuffix = ".go"

// versionSeparator joins a module path to the version a directive gives it,
// wherever the two are written as one word.
const versionSeparator = "@"

// FileNeverBuilt reports a source file of the target that no configuration of the
// build matrix compiles, naming the build constraint that excluded it.
//
// The kind reports only where the configuration both lists the build
// configurations and declares the matrix complete, because that declaration says
// the listed configurations are every one the target builds. A matrix the run
// derived holds the configurations the tree's own build atoms imply and cannot
// claim to be every configuration: the derivation satisfies a Boolean constraint
// only where its atoms happen to, and a configuration no file in the tree names is
// never derived, so a file the derivation could not reach is no evidence that
// nothing builds it. Under a derived matrix the kind reports nothing and the report
// says so.
//
// A file the toolchain ignored solely because it imports "C" is never reported.
// Under the cgo-disabled load such a file is ignored for a reason no build
// configuration decides, and the run records it as excluded by cgo instead. A file
// the cgo build TAG excluded stays in the population, because that tag is an atom
// a configuration names.
func FileNeverBuilt(in *Input) ([]Finding, error) {
	if !completeMatrix(in) {
		return nil, nil
	}
	ignored, err := neverBuilt(in)
	if err != nil {
		return nil, err
	}
	found := make([]Finding, 0, len(ignored))
	for _, file := range ignored {
		found = append(found, Finding{
			Code:     fileNeverBuiltCode,
			Position: Position{Path: file.path, Line: 1, Column: 1, EndLine: file.lines},
			Symbol: Subject{
				Ref:       graph.Ref(graph.KindFile, file.pkgPath, []string{file.name}),
				Kind:      symbolKinds[graph.KindFile],
				Name:      file.name,
				SizeLines: file.lines,
			},
			Configurations: slices.Clone(in.Matrix),
			Message:        "no configuration of the build matrix compiles this file",
			Details:        Details{ExcludedBy: file.constraint},
		})
	}
	return found, nil
}

// completeMatrix reports whether the run analyzes a matrix the configuration both
// listed and declared complete, which is the one declaration under which a file no
// configuration builds is a finding.
//
// Both halves are the declaration: the assertion is about the configurations the
// document lists, so a configuration declaring completeness and listing none
// declares it of a matrix the run derived, and a derived matrix is incomplete.
func completeMatrix(in *Input) bool {
	return in != nil && in.Config != nil &&
		in.Config.Analysis.Matrix.Complete && len(in.Config.Analysis.Configurations) > 0
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
// ignored, in path order, each with the constraint that excluded it.
//
// The population is the intersection of what the configurations ignored, per the
// matrix rule that a subject is reported only where every configuration agrees: a
// file one configuration compiled is built, whatever the others did with it. The
// files excluded by cgo are then dropped, and the constraint of each survivor is
// read from the file itself.
//
// Two limits are the load's rather than this rule's, and both are recorded in the
// run's report. A directory whose every file a constraint excludes holds no package
// at all, so the load returns nothing for it and no file of it is in this
// population. A file the load never reached for any other reason is likewise
// invisible here.
func neverBuilt(in *Input) ([]ignoredFile, error) {
	ignored := ignoredEverywhere(in.Per)
	if len(ignored) == 0 {
		return nil, nil
	}
	cgo := excludedByCgo(in.Per)
	root, err := targetRoot(in.Per)
	if err != nil {
		return nil, err
	}

	files := make([]ignoredFile, 0, len(ignored))
	for _, path := range slices.Sorted(maps.Keys(ignored)) {
		relative := relativeToSlash(root, path)
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

// ignoredEverywhere is every Go file every configuration of the run ignored, keyed
// by its absolute path, with the import path of the package that listed it.
//
// A package and its test variants list the same ignored files, so the import path
// kept is the tested package's: a variant carries the path of the package it tests
// in ForTest, and a file belongs to the directory rather than to a variant of it.
func ignoredEverywhere(per []Configured) map[string]string {
	if len(per) == 0 {
		return nil
	}
	everywhere := ignoredUnder(per[0].Result)
	for _, one := range per[1:] {
		under := ignoredUnder(one.Result)
		for path := range everywhere {
			if _, also := under[path]; !also {
				delete(everywhere, path)
			}
		}
	}
	return everywhere
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

// excludedByCgo is every target-relative path any configuration recorded as
// excluded by cgo alone. One configuration is enough: a file importing "C" imports
// it whichever configuration reads it.
func excludedByCgo(per []Configured) map[string]bool {
	excluded := make(map[string]bool)
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		for _, path := range one.Result.ExcludedByCgo {
			excluded[path] = true
		}
	}
	return excluded
}

// targetRoot is the directory the target's module is rooted at, which is what the
// positions of a report are relative to.
func targetRoot(per []Configured) (string, error) {
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		for _, p := range one.Result.Packages {
			if p.Module != nil && p.Module.Main && p.Module.Dir != "" {
				return p.Module.Dir, nil
			}
		}
	}
	return "", fmt.Errorf("%w: the load names no directory for the target's own module",
		ErrInput)
}

// targetModule is the module path of the target, which scopes the reference of
// every subject the module file declares.
func targetModule(per []Configured) (string, error) {
	for _, one := range per {
		if one.Result == nil {
			continue
		}
		for _, p := range one.Result.Packages {
			if p.Module != nil && p.Module.Main && p.Module.Path != "" {
				return p.Module.Path, nil
			}
		}
	}
	return "", fmt.Errorf("%w: the load names no module path for the target", ErrInput)
}

// relativeToSlash renders path relative to base with forward slashes, falling back
// to path itself where no relative form exists.
func relativeToSlash(base, path string) string {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// readIgnored reads one ignored file, the constraint that excluded it and the
// number of lines it spans, and reports whether the file is a subject of the kind
// at all.
//
// A file the ignore tag constrains is not one. That tag is the toolchain's own
// convention for a file run by hand rather than built, so a file carrying it is
// built by hand by definition and no declaration about the matrix says otherwise. A
// file any other custom tag excluded is a subject, because a matrix the
// configuration declares complete asserts that the listed configurations are every
// one the target builds and a tag the project builds by hand belongs in the list.
//
// A file no constraint decides is an error rather than a finding with nothing to
// name: every file of this population was ignored by the toolchain, so one whose
// constraint cannot be read is a file this rule does not understand, and the run
// says so instead of reporting a subject with no reason.
func readIgnored(path, relative, pkgPath string) (ignoredFile, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return ignoredFile{}, false, fmt.Errorf("read the ignored file %s: %w", relative, err)
	}
	base := filepath.Base(path)
	expression, err := fileConstraint(path, base, content)
	if err != nil {
		return ignoredFile{}, false, fmt.Errorf("read the build constraint of %s: %w", relative, err)
	}
	if expression == nil {
		return ignoredFile{}, false, fmt.Errorf("%w: %s declares no build constraint and no configuration built it",
			ErrEmitter, relative)
	}
	if builtByHand(expression) {
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

// ignoreTag is the tag the toolchain's convention for a file run by hand
// constrains it on, which no build sets.
const ignoreTag = "ignore"

// builtByHand reports whether one constraint states the ignore tag among the
// conditions it puts side by side, which is a file the toolchain never builds under
// any configuration because it is meant to be run.
func builtByHand(expression constraint.Expr) bool {
	for _, condition := range conditions(expression) {
		if tag, named := condition.(*constraint.TagExpr); named && tag.Tag == ignoreTag {
			return true
		}
	}
	return false
}

// fileConstraint is the one build constraint a file carries: what its name implies
// and what its content declares, joined where it carries both.
//
// A condition both halves state appears once. A file named for a platform and
// declaring that platform states one condition twice, and the constraint the file
// spells is that condition, so the join drops the repetition rather than writing it
// out. Only the conditions the join itself puts side by side are compared: a
// condition nested inside one half's own Boolean expression is that expression's and
// is left as the file wrote it.
//
// internal/matrix owns the reading of a build constraint and does not export it,
// so these are its two rules over the same standard-library parser, and they go
// when that package exports one function for a caller outside it.
func fileConstraint(path, base string, content []byte) (constraint.Expr, error) {
	declared, err := declaredConstraint(path, content)
	if err != nil {
		return nil, err
	}
	implied := nameConstraint(base)
	switch {
	case implied == nil:
		return declared, nil
	case declared == nil:
		return implied, nil
	default:
		return conjunction(append(conditions(implied), conditions(declared)...)), nil
	}
}

// conditions is the conditions one expression states side by side, which is the
// expression itself where it states one.
func conditions(expression constraint.Expr) []constraint.Expr {
	and, joined := expression.(*constraint.AndExpr)
	if !joined {
		return []constraint.Expr{expression}
	}
	return append(conditions(and.X), conditions(and.Y)...)
}

// conjunction joins conditions into one expression, keeping the first of any that
// read the same and their order, and answers nil for none.
func conjunction(stated []constraint.Expr) constraint.Expr {
	var joined constraint.Expr
	held := make(map[string]bool, len(stated))
	for _, condition := range stated {
		if held[condition.String()] {
			continue
		}
		held[condition.String()] = true
		joined = and(joined, condition)
	}
	return joined
}

// declaredConstraint is the constraint one file's content declares, and nil for
// content that declares none.
//
// A //go:build expression controls where the file carries one; otherwise the
// legacy lines the header holds are combined, each a further condition the file
// must satisfy. Only the comments above the package clause count, which is where
// the language admits a build constraint.
func declaredConstraint(path string, content []byte) (constraint.Expr, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, content,
		parser.PackageClauseOnly|parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var legacy constraint.Expr
	for _, group := range parsed.Comments {
		if group.End() > parsed.Package {
			break
		}
		for _, line := range group.List {
			switch {
			case constraint.IsGoBuild(line.Text):
				return constraint.Parse(line.Text)
			case constraint.IsPlusBuild(line.Text):
				expression, err := constraint.Parse(line.Text)
				if err != nil {
					continue
				}
				legacy = and(legacy, expression)
			}
		}
	}
	return legacy, nil
}

// and joins two conditions, and answers the second alone where the first is
// absent.
func and(held, next constraint.Expr) constraint.Expr {
	if held == nil {
		return next
	}
	return &constraint.AndExpr{X: held, Y: next}
}

// nameConstraint is the constraint a file's name implies, and nil for a name that
// implies none.
//
// The rule is the toolchain's: the name is read up to its first full stop,
// everything before its first underscore is dropped, a trailing test field is
// dropped with it, and then the last two fields count where the first of them
// names an operating system and the second an architecture, or the last field
// alone counts where it names either.
func nameConstraint(base string) constraint.Expr {
	stem, _, _ := strings.Cut(base, ".")
	_, suffixed, underscored := strings.Cut(stem, "_")
	if !underscored {
		return nil
	}
	fields := strings.Split(suffixed, "_")
	if last := len(fields) - 1; fields[last] == testStem {
		fields = fields[:last]
	}

	last := len(fields) - 1
	switch {
	case last < 0:
		return nil
	case last >= 1 && isOperatingSystem(fields[last-1]) && isArchitecture(fields[last]):
		return &constraint.AndExpr{
			X: &constraint.TagExpr{Tag: fields[last-1]},
			Y: &constraint.TagExpr{Tag: fields[last]},
		}
	case isPlatformName(fields[last]):
		return &constraint.TagExpr{Tag: fields[last]}
	default:
		return nil
	}
}

// testStem is the file-name field that names a test file, which carries no
// platform constraint of its own.
const testStem = "test"

// The two names a platform probe pins so that the toolchain answers about the
// field beside them: an architecture every toolchain knows, and an operating
// system and architecture no toolchain does.
const (
	probeArch    = "amd64"
	unknownOS    = "notanoperatingsystem"
	unknownArch  = "notanarchitecture"
	probePattern = "probe_%s.go"
)

// isPlatformName reports whether one field of a file name is a platform name, so
// that a file whose name ends in it carries a constraint.
//
// The toolchain is asked rather than a second copy of its platform list carried
// here: a file whose name ends in a platform name is selected only where that name
// is in effect, so a synthetic name ending in the field is refused under a
// configuration naming neither an operating system nor an architecture exactly
// where the field is a platform name.
func isPlatformName(field string) bool {
	return !selectsName(fmt.Sprintf(probePattern, field), unknownOS, unknownArch)
}

// isOperatingSystem reports whether one field of a file name is an
// operating-system name.
//
// The toolchain reads the last two fields of a name together only where the first
// is an operating system and the second an architecture, so a synthetic name
// pairing the field with an architecture is refused under a configuration naming
// that architecture exactly where the field is an operating-system name.
func isOperatingSystem(field string) bool {
	return !selectsName(fmt.Sprintf(probePattern, field+"_"+probeArch), unknownOS, probeArch)
}

// isArchitecture reports whether one field of a file name is an architecture name,
// which is a platform name that is not an operating system: the toolchain's two
// lists are disjoint.
func isArchitecture(field string) bool {
	return isPlatformName(field) && !isOperatingSystem(field)
}

// selectsName reports whether the toolchain selects a file of that name under the
// named platform, deciding on the name alone: the content every probe reads holds
// a package clause and no constraint.
func selectsName(base, system, arch string) bool {
	ctxt := build.Default
	ctxt.GOOS = system
	ctxt.GOARCH = arch
	ctxt.BuildTags = nil
	ctxt.ToolTags = nil
	ctxt.CgoEnabled = false
	ctxt.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package p\n")), nil
	}
	selected, err := ctxt.MatchFile(".", base)
	return err == nil && selected
}

// FileNeverImported reports a source file of a package that no import reaches,
// that no root names, and whose every declaration the sweep found dead.
//
// The three conditions are one claim: nothing compiles the file into a package
// anything reaches, so the file falls with its package. A package main is never a
// subject, because the toolchain builds one whatever imports it, and neither is a
// package holding a root, which is what leaves a test package with a test function
// out: the test binary reaches its declarations through those roots rather than
// through an import.
func FileNeverImported(in *Input) ([]Finding, error) {
	if in == nil || in.Merged == nil || in.Sweep == nil {
		return nil, nil
	}
	unreached := unreachedPackages(in)
	var found []Finding
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind != graph.KindFile || !unreached[symbol.PkgPath] {
			continue
		}
		one, held := in.finding(symbol.ID, fileNeverImportedCode,
			"no import reaches the package this file declares and no root names a declaration in it")
		if !held {
			continue
		}
		found = append(found, one)
	}
	return found, nil
}

// unreachedPackages is every import path of the target that no import reaches, no
// root names, and whose every declaration is a candidate of the sweep.
//
// A package declaring nothing at all is absent from the set rather than in it:
// there is nothing dead in it, so its files fall with no declaration and the more
// specific answer about the directory is that it holds no code.
//
// A declaration the sweep did not judge dead keeps the package's files out of the
// set whatever held it live, which for a package nothing imports is an exemption: a
// use no reference names reaches that declaration, so the file it is written in
// does not fall.
func unreachedPackages(in *Input) map[string]bool {
	imported := in.importedPackages()
	rooted := in.rootedPackages()

	declared := make(map[string]int)
	live := make(map[string]int)
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind == graph.KindPackage || symbol.Kind == graph.KindFile {
			continue
		}
		declared[symbol.PkgPath]++
		if in.candidateOf(symbol.ID) == nil {
			live[symbol.PkgPath]++
		}
	}

	unreached := make(map[string]bool, len(declared))
	for pkgPath := range declared {
		if imported[pkgPath] || rooted[pkgPath] || in.index().mains[pkgPath] {
			continue
		}
		unreached[pkgPath] = live[pkgPath] == 0
	}
	return unreached
}

// importedPackages is every package of the target an import from outside it
// reaches.
//
// An import is a reference to the package symbol the inventory holds for the
// imported package, which is the edge the reference pass records at an import
// spec. A reference a declaration of the package itself made is not an import of
// it, whatever names it, so only an edge from outside counts.
func (in *Input) importedPackages() map[string]bool {
	imported := make(map[string]bool)
	for i := range in.Merged.References {
		ref := &in.Merged.References[i]
		to := in.symbol(ref.To)
		if to == nil || to.Kind != graph.KindPackage {
			continue
		}
		if from := in.symbol(ref.From); from != nil && from.PkgPath == to.PkgPath {
			continue
		}
		imported[to.PkgPath] = true
	}
	return imported
}

// rootedPackages is every package of the target holding a declaration a root
// names.
func (in *Input) rootedPackages() map[string]bool {
	rooted := make(map[string]bool)
	for _, root := range in.Merged.Roots {
		if symbol := in.symbol(root.ID); symbol != nil {
			rooted[symbol.PkgPath] = true
		}
	}
	return rooted
}

// UnusedDependency reports a direct requirement of the module file whose module
// provides no package any package of the target imports.
//
// A requirement marked indirect is never reported: it carries no import by
// construction and pins a transitive version under module graph pruning, so
// deleting it changes the build list. A requirement is reported only where every
// configuration of the matrix agrees, because a requirement one configuration
// imports is used.
func UnusedDependency(in *Input) ([]Finding, error) {
	unused := unusedRequirements(in)
	if len(unused) == 0 {
		return nil, nil
	}
	module, err := targetModule(in.Per)
	if err != nil {
		return nil, err
	}

	found := make([]Finding, 0, len(unused))
	for _, require := range unused {
		found = append(found, Finding{
			Code:     unusedDependencyCode,
			Position: directivePosition(require.Site),
			Symbol: Subject{
				Ref:       moduleFragment(module, require.Path+":"+requireSection),
				Kind:      dependencySubject,
				Name:      require.Path,
				SizeLines: 1,
			},
			Configurations: slices.Clone(in.Matrix),
			Message:        "no package of the target imports a package this required module provides",
			Details:        Details{DependencyClass: requireSection},
		})
	}
	return found, nil
}

// unusedRequirements is every direct requirement every configuration of the run
// found unused, in the order the module file declares them.
func unusedRequirements(in *Input) []deps.Requirement {
	if in == nil || in.Deps == nil || len(in.Per) == 0 {
		return nil
	}
	unused := deps.UnusedRequirements(*in.Deps, in.Per[0].Result)
	for _, one := range in.Per[1:] {
		also := deps.UnusedRequirements(*in.Deps, one.Result)
		unused = slices.DeleteFunc(unused, func(require deps.Requirement) bool {
			return !slices.ContainsFunc(also, func(other deps.Requirement) bool {
				return other.Path == require.Path
			})
		})
	}
	return unused
}

// UnusedReplace reports a replace directive whose replaced module is absent from
// the build list, which is the one case in which the directive redirects nothing.
//
// It is reported only where every configuration of the matrix agrees, because a
// module one configuration loads is in the build list.
func UnusedReplace(in *Input) ([]Finding, error) {
	noop := noopReplacements(in)
	if len(noop) == 0 {
		return nil, nil
	}
	module, err := targetModule(in.Per)
	if err != nil {
		return nil, err
	}

	found := make([]Finding, 0, len(noop))
	for _, replace := range noop {
		named := spelled(replace.Old)
		found = append(found, Finding{
			Code:     unusedReplaceCode,
			Position: directivePosition(replace.Site),
			Symbol: Subject{
				Ref:       moduleFragment(module, named+replaceSelector),
				Kind:      directiveSubject,
				Name:      named,
				SizeLines: 1,
			},
			Configurations: slices.Clone(in.Matrix),
			Message:        "the module this directive replaces is absent from the build list",
			Details:        Details{Replacement: spelled(replace.New)},
		})
	}
	return found, nil
}

// noopReplacements is every replace directive every configuration of the run found
// to redirect nothing, in the order the module file declares them.
func noopReplacements(in *Input) []deps.Replacement {
	if in == nil || in.Deps == nil || len(in.Per) == 0 {
		return nil
	}
	noop := deps.NoopReplacements(*in.Deps, in.Per[0].Result)
	for _, one := range in.Per[1:] {
		also := deps.NoopReplacements(*in.Deps, one.Result)
		noop = slices.DeleteFunc(noop, func(replace deps.Replacement) bool {
			return !slices.ContainsFunc(also, func(other deps.Replacement) bool {
				return other.Old == replace.Old && other.New == replace.New
			})
		})
	}
	return noop
}

// spelled renders one module the way the module file writes it: the path, and the
// version after an at sign where the directive names one.
func spelled(m deps.Module) string {
	if m.Version == "" {
		return m.Path
	}
	return m.Path + versionSeparator + m.Version
}

// moduleFragment is the reference of one subject the target's module file
// declares: the target's module as the scope, and the fragment the subject's own
// grammar spells. The scope comes from the reference of a package so that the
// language prefix has one owner.
func moduleFragment(module, fragment string) string {
	return graph.Ref(graph.KindPackage, module, nil) + fragment
}

// positionOf renders the position of a directive of the module file, which spans
// the one line it is written on.
func directivePosition(site token.Position) Position {
	return Position{
		Path:    site.Filename,
		Line:    site.Line,
		Column:  site.Column,
		EndLine: site.Line,
	}
}

// Annotate names, on every deletion finding whose subject holds the last use of a
// dependency, the modules that deletion removes the last use of.
//
// It is a completion step over the findings of every kind rather than a kind of
// its own: the join is the same whichever code reports the declaration, and only a
// deletable finding carries it, because narrowing a declaration or reporting a
// live one removes no use of anything. A run completes its findings once, so this
// runs after the pass that produced them.
func Annotate(in *Input, findings []Finding) {
	if in == nil || len(findings) == 0 {
		return
	}
	last := lastUses(in)
	if len(last) == 0 {
		return
	}
	for i := range findings {
		found := &findings[i]
		if found.Fixability != deletableFixability || found.id == "" {
			continue
		}
		if modules := last[found.id]; len(modules) > 0 {
			found.Details.RemovesLastUseOf = modules
		}
	}
}

// deletableFixability is what a mechanical edit may do with a finding whose
// subject can be deleted, and the one fixability a dependency's last use is named
// on.
const deletableFixability = "deletable"

// lastUses is the union over the configurations of the modules each deletion
// candidate holds the last use of, each list sorted.
//
// The union is the right fold for this join and the intersection is not: a module
// whose last use one configuration writes inside the candidate loses that use when
// the candidate goes, whatever another configuration does, and the finding names
// what the deletion would remove.
func lastUses(in *Input) map[graph.SymbolID][]string {
	if in.Sweep == nil {
		return nil
	}
	union := make(map[graph.SymbolID][]string)
	for _, one := range in.Per {
		for id, modules := range deps.LastUses(one.Result, one.Resolve, in.Sweep.Candidates) {
			for _, module := range modules {
				if !slices.Contains(union[id], module) {
					union[id] = append(union[id], module)
				}
			}
		}
	}
	for id := range union {
		slices.Sort(union[id])
	}
	return union
}

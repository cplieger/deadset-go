package graph

import (
	"go/token"
	"go/types"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"golang.org/x/tools/go/packages"
)

// testSupportRule names the classification of a file as test-support code, which
// no file-name pattern makes.
const testSupportRule = "test-support"

// testPackageSuffix ends the import path of an external test package, which
// nothing imports.
const testPackageSuffix = "_test"

// The users a package's import or reference is written in, beside the package
// paths of the target: a test file, and a consumer's file that is not a test
// file. Neither spelling is an import path.
const (
	testUser    = ""
	outsideUser = " consumer"
)

// ClassifyTestSupport fills r.TestSupport, by [load.Result.PackageKey], and marks those
// declarations in symbols (positions relative to targetRoot): the largest set of
// non-main, non-test, unrooted packages carrying test evidence that only test files and
// members import or reference by resolved name, so an interface call references no
// implementation. Init functions, blank declarations and tests are no root; a library's
// package qualifies only under an internal tree. r.TestReached is the same largest set
// with no evidence needed, less r.TestSupport: packages only tests reach that are not
// test support, which stay production code.
func ClassifyTestSupport(r *load.Result, targetRoot string, symbols []Symbol, roots []Root, library bool) {
	reached := make(map[string]bool)
	evidenced := make(map[string]bool)
	for _, p := range r.Packages {
		if p.Module != nil && p.Module.Main && supportCandidate(p, library) {
			reached[r.KeyOf(p)] = true
			evidenced[r.KeyOf(p)] = carriesTestEvidence(p)
		}
	}
	keyOf := func(s *Symbol) string {
		return r.PackageKey(s.PkgPath, filepath.Join(targetRoot, filepath.FromSlash(path.Dir(s.Pos.Filename))))
	}
	for key := range rootedPackages(symbols, roots, keyOf) {
		delete(reached, key)
	}
	candidates := maps.Clone(reached)
	maps.DeleteFunc(candidates, func(key string, _ bool) bool { return !evidenced[key] })
	users := usersOf(r, reached)
	r.TestSupport = largestSupport(candidates, users)
	r.TestReached = largestSupport(reached, users)
	maps.DeleteFunc(r.TestReached, func(key string, _ bool) bool { return r.TestSupport[key] })
	for i := range symbols {
		symbols[i].TestSupport = r.TestSupport[keyOf(&symbols[i])]
	}
}

// supportCandidate reports whether one package of the main module could be test
// support: a non-main, non-test package, under an internal tree for a library.
func supportCandidate(p *packages.Package, library bool) bool {
	return p.ForTest == "" && p.Name != mainPackage && !strings.HasSuffix(p.PkgPath, testPackageSuffix) &&
		(!library || slices.Contains(strings.Split(p.PkgPath, "/"), internalElement))
}

// carriesTestEvidence reports whether one package's files other than its test files
// import the testing package. A package only tests reach that carries no such
// evidence is production code a test happens to use.
func carriesTestEvidence(p *packages.Package) bool {
	return p.Imports[testingPath] != nil
}

// rootedPackages is the key of every package that declares a root of a kind
// that keeps a package out of test-support code.
func rootedPackages(symbols []Symbol, roots []Root, keyOf func(*Symbol) string) map[string]bool {
	pkgOf := make(map[SymbolID]string, len(symbols))
	for i := range symbols {
		pkgOf[symbols[i].ID] = keyOf(&symbols[i])
	}
	rooted := make(map[string]bool)
	for _, root := range roots {
		switch root.Kind {
		case RootInit, RootBlank, RootTest:
			continue
		}
		if key, held := pkgOf[root.ID]; held {
			rooted[key] = true
		}
	}
	return rooted
}

// usersOf is, per candidate, every user that imports it or references one of its
// declarations from outside it: the package key of a target file that is not a
// test file, testUser for a test file of the target or of a consumer, and
// outsideUser for any other file of a consumer.
func usersOf(r *load.Result, candidates map[string]bool) map[string]map[string]bool {
	keys := packageKeys(r)
	users := make(map[string]map[string]bool, len(candidates))
	add := func(to, from string) {
		if !candidates[to] || to == from {
			return
		}
		if users[to] == nil {
			users[to] = make(map[string]bool)
		}
		users[to][from] = true
	}
	for _, p := range r.Packages {
		if p.Module != nil && p.Module.Main {
			addUses(r.Fset, p, keys, r.KeyOf(p), add)
		}
	}
	for _, consumer := range r.Referencing() {
		for _, p := range consumer.Packages {
			addUses(r.Fset, p, keys, outsideUser, add)
		}
	}
	return users
}

// packageKeys is the [load.Result.PackageKey] of every package the load reads,
// the target's dependencies and its consumers' included, by its types.
func packageKeys(r *load.Result) map[*types.Package]string {
	keys := make(map[*types.Package]string)
	record := func(p *packages.Package) {
		if p.Types != nil {
			keys[p.Types] = r.KeyOf(p)
		}
	}
	packages.Visit(r.Packages, nil, record)
	for _, consumer := range r.Referencing() {
		packages.Visit(consumer.Packages, nil, record)
	}
	return keys
}

// addUses records every import and every resolved name of one package against the
// user its file is: testUser for a test file and production for any other.
func addUses(fset *token.FileSet, p *packages.Package, keys map[*types.Package]string, production string, add func(to, from string)) {
	files := fileUsers{fset: fset, production: production, of: make(map[*token.File]string)}
	for _, f := range p.Syntax {
		user := files.at(f.FileStart)
		for _, spec := range f.Imports {
			if imported, err := strconv.Unquote(spec.Path.Value); err == nil {
				add(importKey(p, keys, imported), user)
			}
		}
	}
	if p.TypesInfo == nil {
		return
	}
	for id, object := range p.TypesInfo.Uses {
		if object.Pkg() != nil {
			add(typesKey(keys, object.Pkg()), files.at(id.Pos()))
		}
	}
}

// importKey is the key of the package p imports by that path.
func importKey(p *packages.Package, keys map[*types.Package]string, imported string) string {
	if dep := p.Imports[imported]; dep != nil && dep.Types != nil {
		return typesKey(keys, dep.Types)
	}
	return imported
}

// typesKey is the key of pkg, its import path where the load read no such package.
func typesKey(keys map[*types.Package]string, pkg *types.Package) string {
	if key, held := keys[pkg]; held {
		return key
	}
	return pkg.Path()
}

// fileUsers is the user each file of one package is, computed once per file.
type fileUsers struct {
	fset       *token.FileSet
	of         map[*token.File]string
	production string
}

// at is the user of the file holding one position.
func (u *fileUsers) at(pos token.Pos) string {
	file := u.fset.File(pos)
	if user, held := u.of[file]; held {
		return user
	}
	user := u.production
	if file != nil {
		if _, test := IsTestFile(file.Name()); test {
			user = testUser
		}
	}
	u.of[file] = user
	return user
}

// largestSupport is the largest subset of the candidates in which every user of a
// member is a test file or another member and a test reaches every member. Each
// pass drops what breaks either condition, and a set either condition holds of
// survives every pass, so the fixpoint is the largest such set.
func largestSupport(candidates map[string]bool, users map[string]map[string]bool) map[string]bool {
	support := maps.Clone(candidates)
	for changed := true; changed; {
		changed = false
		for member := range support {
			if hasOutsideUser(users[member], support) {
				delete(support, member)
				changed = true
			}
		}
		reached := reachedByTests(support, users)
		for member := range support {
			if !reached[member] {
				delete(support, member)
				changed = true
			}
		}
	}
	return support
}

// hasOutsideUser reports whether a user of one member is neither a test file nor
// another member.
func hasOutsideUser(users, support map[string]bool) bool {
	for user := range users {
		if user != testUser && !support[user] {
			return true
		}
	}
	return false
}

// reachedByTests is every member of support that a chain of users starting at a
// test file reaches through other members.
func reachedByTests(support map[string]bool, users map[string]map[string]bool) map[string]bool {
	reached := make(map[string]bool, len(support))
	for growing := true; growing; {
		growing = false
		for member := range support {
			if reached[member] {
				continue
			}
			for user := range users[member] {
				if user == testUser || reached[user] {
					reached[member] = true
					growing = true
					break
				}
			}
		}
	}
	return reached
}

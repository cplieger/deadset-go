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

// The users a package's import or reference is written in, beside the packages of
// the target: a test file, and a consumer's file that is not a test file. Neither
// names a package.
var (
	testUser    = load.PackageID{}
	outsideUser = load.PackageID{Path: " consumer"}
)

// TestClasses is one configuration's classification of the target's packages
// only tests reach: Support carries test evidence and is test code, Reached does
// not and stays production code.
type TestClasses struct {
	Support map[load.PackageID]bool
	Reached map[load.PackageID]bool
}

// ClassifyTestSupport classifies the target's packages and marks the test-support
// declarations in symbols (positions relative to targetRoot). Support is the largest
// set of non-main, non-test, unrooted packages carrying test evidence that only test
// files and members import or reference by resolved name, so an interface call
// references no implementation. Init functions, blank declarations and tests are no
// root; a library's package qualifies only under an internal tree. Reached is the
// same largest set with no evidence needed, less Support.
func ClassifyTestSupport(r *load.Result, targetRoot string, symbols []Symbol, roots []Root, library bool) TestClasses {
	reached := make(map[load.PackageID]bool)
	evidenced := make(map[load.PackageID]bool)
	for _, p := range r.Packages {
		if p.Module != nil && p.Module.Main && supportCandidate(p, library) {
			reached[r.ID(p)] = true
			evidenced[r.ID(p)] = carriesTestEvidence(p)
		}
	}
	keyOf := func(s *Symbol) load.PackageID { return PackageOf(r, s, targetRoot) }
	for key := range rootedPackages(symbols, roots, keyOf) {
		delete(reached, key)
	}
	candidates := maps.Clone(reached)
	maps.DeleteFunc(candidates, func(key load.PackageID, _ bool) bool { return !evidenced[key] })
	users := usersOf(r, reached)
	classes := TestClasses{Support: largestSupport(candidates, users), Reached: largestSupport(reached, users)}
	maps.DeleteFunc(classes.Reached, func(key load.PackageID, _ bool) bool { return classes.Support[key] })
	for i := range symbols {
		symbols[i].TestSupport = classes.Support[keyOf(&symbols[i])]
	}
	return classes
}

// PackageOf is the identity in r of the package declaring s, positions relative to
// targetRoot.
func PackageOf(r *load.Result, s *Symbol, targetRoot string) load.PackageID {
	return r.IDAt(s.PkgPath, filepath.Join(targetRoot, filepath.FromSlash(path.Dir(s.Pos.Filename))))
}

// supportCandidate reports whether one package of the main module could be test
// support: a non-main, non-test package, under an internal tree for a library.
func supportCandidate(p *packages.Package, library bool) bool {
	return p.ForTest == "" && p.Name != mainPackage && !strings.HasSuffix(p.PkgPath, testPackageSuffix) &&
		(!library || slices.Contains(strings.Split(p.PkgPath, "/"), internalElement))
}

// testLibraries are the module paths of the assertion, runner and comparison
// libraries whose import is test evidence as the testing package's is; a package
// below one of them counts too.
var testLibraries = [...]string{
	"github.com/google/go-cmp",
	"github.com/onsi/ginkgo",
	"github.com/onsi/gomega",
	"github.com/stretchr/testify",
	"gotest.tools",
}

// carriesTestEvidence reports whether one package's files other than its test files
// import the testing package or a package of a test library. A package only tests
// reach that carries no such evidence is production code a test happens to use.
func carriesTestEvidence(p *packages.Package) bool {
	for imported := range p.Imports {
		if imported == testingPath || slices.ContainsFunc(testLibraries[:], func(library string) bool {
			return imported == library || strings.HasPrefix(imported, library+"/")
		}) {
			return true
		}
	}
	return false
}

// rootedPackages is the key of every package that declares a root of a kind
// that keeps a package out of test-support code.
func rootedPackages(symbols []Symbol, roots []Root, keyOf func(*Symbol) load.PackageID) map[load.PackageID]bool {
	pkgOf := make(map[SymbolID]load.PackageID, len(symbols))
	for i := range symbols {
		pkgOf[symbols[i].ID] = keyOf(&symbols[i])
	}
	rooted := make(map[load.PackageID]bool)
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
func usersOf(r *load.Result, candidates map[load.PackageID]bool) map[load.PackageID]map[load.PackageID]bool {
	keys := packageKeys(r)
	users := make(map[load.PackageID]map[load.PackageID]bool, len(candidates))
	add := func(to, from load.PackageID) {
		if !candidates[to] || to == from {
			return
		}
		if users[to] == nil {
			users[to] = make(map[load.PackageID]bool)
		}
		users[to][from] = true
	}
	for _, p := range r.Packages {
		if p.Module != nil && p.Module.Main {
			addUses(r.Fset, p, keys, r.ID(p), add)
		}
	}
	for _, consumer := range r.Referencing() {
		for _, p := range consumer.Packages {
			addUses(r.Fset, p, keys, outsideUser, add)
		}
	}
	return users
}

// packageKeys is the [load.PackageID] of every package the load reads, the
// target's dependencies and its consumers' included, by its types.
func packageKeys(r *load.Result) map[*types.Package]load.PackageID {
	keys := make(map[*types.Package]load.PackageID)
	record := func(p *packages.Package) {
		if p.Types != nil {
			keys[p.Types] = r.ID(p)
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
func addUses(fset *token.FileSet, p *packages.Package, keys map[*types.Package]load.PackageID, production load.PackageID, add func(to, from load.PackageID)) {
	files := fileUsers{fset: fset, production: production, of: make(map[*token.File]load.PackageID)}
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

// importKey is the identity of the package p imports by that path.
func importKey(p *packages.Package, keys map[*types.Package]load.PackageID, imported string) load.PackageID {
	if dep := p.Imports[imported]; dep != nil && dep.Types != nil {
		return typesKey(keys, dep.Types)
	}
	return load.PackageID{Path: imported}
}

// typesKey is the identity of pkg, its import path alone where the load read no
// such package.
func typesKey(keys map[*types.Package]load.PackageID, pkg *types.Package) load.PackageID {
	if key, held := keys[pkg]; held {
		return key
	}
	return load.PackageID{Path: pkg.Path()}
}

// fileUsers is the user each file of one package is, computed once per file.
type fileUsers struct {
	fset       *token.FileSet
	of         map[*token.File]load.PackageID
	production load.PackageID
}

// at is the user of the file holding one position.
func (u *fileUsers) at(pos token.Pos) load.PackageID {
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
func largestSupport(candidates map[load.PackageID]bool, users map[load.PackageID]map[load.PackageID]bool) map[load.PackageID]bool {
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
func hasOutsideUser(users, support map[load.PackageID]bool) bool {
	for user := range users {
		if user != testUser && !support[user] {
			return true
		}
	}
	return false
}

// reachedByTests is every member of support that a chain of users starting at a
// test file reaches through other members.
func reachedByTests(support map[load.PackageID]bool, users map[load.PackageID]map[load.PackageID]bool) map[load.PackageID]bool {
	reached := make(map[load.PackageID]bool, len(support))
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

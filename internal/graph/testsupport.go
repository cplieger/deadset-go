package graph

import (
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
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

// ClassifyTestSupport records the target's test-support packages in r.TestSupport and
// marks their declarations in symbols. That is the largest set of non-main packages of
// the target, none a test package by name and none holding a root, that a test reaches
// and that nothing outside the set imports or references except a test file. A
// package's own test files are outside it. A reference is a resolved name, so a call
// through an interface method references no implementation. An init function, a blank
// declaration and a test are no root here. A library's package is one only under an
// internal tree, because outside programs may import any other.
func ClassifyTestSupport(r *load.Result, symbols []Symbol, roots []Root, library bool) {
	candidates := make(map[string]bool)
	for _, p := range r.Packages {
		if p.Module != nil && p.Module.Main && supportCandidate(p, library) {
			candidates[p.PkgPath] = true
		}
	}
	for path := range rootedPackages(symbols, roots) {
		delete(candidates, path)
	}
	r.TestSupport = largestSupport(candidates, usersOf(r, candidates))
	for i := range symbols {
		symbols[i].TestSupport = r.TestSupport[symbols[i].PkgPath]
	}
}

// supportCandidate reports whether one package of the main module could be test
// support: a non-main, non-test package, under an internal tree for a library.
func supportCandidate(p *packages.Package, library bool) bool {
	return p.ForTest == "" && p.Name != mainPackage && !strings.HasSuffix(p.PkgPath, testPackageSuffix) &&
		(!library || slices.Contains(strings.Split(p.PkgPath, "/"), internalElement))
}

// rootedPackages is the import path of every package that declares a root of a
// kind that keeps a package out of test-support code.
func rootedPackages(symbols []Symbol, roots []Root) map[string]bool {
	pkgOf := make(map[SymbolID]string, len(symbols))
	for i := range symbols {
		pkgOf[symbols[i].ID] = symbols[i].PkgPath
	}
	rooted := make(map[string]bool)
	for _, root := range roots {
		switch root.Kind {
		case RootInit, RootBlank, RootTest:
			continue
		}
		if path, held := pkgOf[root.ID]; held {
			rooted[path] = true
		}
	}
	return rooted
}

// usersOf is, per candidate, every user that imports it or references one of its
// declarations from outside it: the package path of a target file that is not a
// test file, testUser for a test file of the target or of a consumer, and
// outsideUser for any other file of a consumer.
func usersOf(r *load.Result, candidates map[string]bool) map[string]map[string]bool {
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
			addUses(r.Fset, p, p.PkgPath, add)
		}
	}
	for _, consumer := range r.Consumers {
		for _, p := range consumer.Packages {
			addUses(r.Fset, p, outsideUser, add)
		}
	}
	return users
}

// addUses records every import and every resolved name of one package against the
// user its file is: testUser for a test file and production for any other.
func addUses(fset *token.FileSet, p *packages.Package, production string, add func(to, from string)) {
	files := fileUsers{fset: fset, production: production, of: make(map[*token.File]string)}
	for _, f := range p.Syntax {
		user := files.at(f.FileStart)
		for _, spec := range f.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil {
				add(path, user)
			}
		}
	}
	if p.TypesInfo == nil {
		return
	}
	for id, object := range p.TypesInfo.Uses {
		if object.Pkg() != nil {
			add(object.Pkg().Path(), files.at(id.Pos()))
		}
	}
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

// Package matrix derives a build matrix from a target tree: the platform and tag
// atoms its files name, plus the configuration of the host.
//
// Derivation never enumerates tag combinations. It emits one configuration per
// distinct operating-system atom, one per architecture atom, one per tag, and
// the host's own, so a tree naming three systems and two tags derives five
// configurations beside the host rather than the twelve their combinations would
// give.
//
// A derived matrix is incomplete by definition: the atoms satisfy a Boolean
// constraint only where they happen to, no configuration being invented to
// satisfy one, and a configuration no file in the tree names is never derived.
// The completeness of a matrix is the configuration document's assertion
// (analysis.matrix.complete), which derivation neither reads nor sets. The
// findings pass reads Unreachable for DS1501 and reports nothing from it unless
// the configuration declares the matrix complete, because a file derivation
// could not reach is no evidence that nothing builds it.
package matrix

import (
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/load"
)

// idSeparator joins the parts of a configuration identifier.
const idSeparator = "-"

// Atoms are the build atoms one target tree names, each list sorted and without
// repetition. A tag the toolchain answers from its own environment rather than
// from a configuration's tag list is no atom and appears in none of them.
type Atoms struct {
	OS   []string // operating-system names
	Arch []string // architecture names
	Tags []string // every other tag a build expression or a file name names
}

// File is one file of the target and the build constraint that decided it.
type File struct {
	Path       string // target-relative, forward slashes
	Constraint string // the constraint, as one //go:build expression
}

// Derived is one target tree's derivation.
type Derived struct {
	// Configurations is the derived matrix: the host first, then one
	// configuration per operating-system atom, one per architecture atom and one
	// per tag, deduplicated and in that order.
	Configurations []load.Configuration

	// Guessed names, by identifier, every configuration of Configurations that
	// derivation answered from an atom the tree names rather than reading from the
	// host. The pairing of an atom with an axis is derivation's own answer, so a
	// pair the TARGET does not build is that answer being wrong rather than the
	// target being broken, and a load may drop one. The host's own configuration is
	// never one: a target that does not build where the run is has nothing to
	// analyse.
	Guessed []string

	// Unreachable holds every file no configuration of the derived matrix
	// builds, each with the constraint that excluded it, in the order the tree
	// was read.
	Unreachable []File

	// Atoms is what the tree named, including the names the toolchain no longer
	// builds for, which derive no configuration of their own.
	Atoms Atoms
}

// Derive reads the module tree rooted at root and returns the matrix its build
// constraints and the host configuration imply.
//
// A file whose build constraint does not parse, or which declares more than one,
// ends derivation with that file named: the toolchain refuses such a file too, so
// deriving a matrix from the rest would describe a tree that does not build.
func Derive(root string) (Derived, error) {
	return derive(root, load.HostConfiguration())
}

// derive is Derive with the host configuration supplied, which is what lets a
// test state the identifiers it expects rather than compute them.
func derive(root string, host load.Configuration) (Derived, error) {
	files, err := collect(root)
	if err != nil {
		return Derived{}, err
	}
	atoms := atomsOf(files)
	configurations := configurationsOf(host, atoms)
	return Derived{
		Configurations: configurations,
		Guessed:        guessedIn(configurations, host),
		Unreachable:    unreachableUnder(files, configurations),
		Atoms:          atoms,
	}, nil
}

// atomsOf classifies every tag the files name onto the axis it belongs to.
func atomsOf(files []fileConstraint) Atoms {
	named := make(map[string]bool)
	for _, file := range files {
		collectTags(file.expr(), named)
	}

	var atoms Atoms
	for name := range named {
		switch {
		case isOS(name):
			atoms.OS = append(atoms.OS, name)
		case isArch(name):
			atoms.Arch = append(atoms.Arch, name)
		case isToolchainTag(name):
		default:
			atoms.Tags = append(atoms.Tags, name)
		}
	}
	slices.Sort(atoms.OS)
	slices.Sort(atoms.Arch)
	slices.Sort(atoms.Tags)
	return atoms
}

// configurationsOf returns the matrix one atom set and one host imply: the host,
// then one configuration per atom of each axis, deduplicated by identifier.
//
// A platform atom is paired with the host's other axis where the toolchain builds
// that pair and with a pair it does build otherwise, so every configuration the
// matrix names is one the toolchain has a target for. An atom the toolchain builds
// nothing for derives no configuration at all. Whether the TARGET builds for a pair
// is a further question only its load answers, which is why every configuration but
// the host's carries the derived mark: the pairing is this derivation's answer, and a
// pair the target does not build is that answer being wrong.
//
// An atom whose pairing names the host's own identifier keeps the host's entry, so a
// tree naming its own platform does not turn the host into a derived configuration.
func configurationsOf(host load.Configuration, atoms Atoms) []load.Configuration {
	configurations := []load.Configuration{host}
	named := map[string]bool{host.ID: true}
	add := func(c load.Configuration) {
		if !named[c.ID] {
			named[c.ID] = true
			configurations = append(configurations, c)
		}
	}

	for _, system := range atoms.OS {
		if arch, built := archFor(system, host.Arch); built {
			add(configurationFor(system, arch, nil))
		}
	}
	for _, arch := range atoms.Arch {
		if system, built := osFor(arch, host.OS); built {
			add(configurationFor(system, arch, nil))
		}
	}
	for _, tag := range atoms.Tags {
		add(configurationFor(host.OS, host.Arch, []string{tag}))
	}
	return configurations
}

// configurationFor names one configuration the way an entry of the configured
// matrix names itself: the platform, then every tag in effect.
func configurationFor(system, arch string, tags []string) load.Configuration {
	id := system + idSeparator + arch
	if len(tags) > 0 {
		id += idSeparator + strings.Join(tags, idSeparator)
	}
	return load.Configuration{ID: id, OS: system, Arch: arch, Tags: tags}
}

// guessedIn names every configuration of the matrix but the host's, in the order the
// matrix lists them.
func guessedIn(configurations []load.Configuration, host load.Configuration) []string {
	var guessed []string
	for _, c := range configurations {
		if c.ID != host.ID {
			guessed = append(guessed, c.ID)
		}
	}
	return guessed
}

// unreachableUnder returns every file no configuration of the matrix builds.
func unreachableUnder(files []fileConstraint, configurations []load.Configuration) []File {
	answers := make([]func(tag string) bool, len(configurations))
	for i, c := range configurations {
		answers[i] = satisfies(c)
	}

	var unreachable []File
	for _, file := range files {
		expression := file.expr()
		if expression == nil {
			continue
		}
		built := slices.ContainsFunc(answers, func(satisfied func(tag string) bool) bool {
			return expression.Eval(satisfied)
		})
		if !built {
			unreachable = append(unreachable, File{Path: file.path, Constraint: expression.String()})
		}
	}
	return unreachable
}

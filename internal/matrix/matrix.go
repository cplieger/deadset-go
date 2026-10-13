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
// (analysis.matrix.complete), which derivation neither reads nor sets.
package matrix

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/load"
)

// idSeparator joins the parts of a configuration identifier.
const idSeparator = "-"

// atomSet is the build atoms one target tree names, each list sorted and without
// repetition. A tag the toolchain answers from its own environment rather than
// from a configuration's tag list is no atom and appears in none of them.
type atomSet struct {
	OS   []string // operating-system names
	Arch []string // architecture names
	Tags []string // every other tag a build expression or a file name names
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
}

// Derive reads the tree of every module a load of root reads, [load.Units], and
// returns the matrix their build constraints and the host configuration imply.
//
// A file whose build constraint does not parse, or which declares more than one,
// ends derivation with that file named: the toolchain refuses such a file too, so
// deriving a matrix from the rest would describe a tree that does not build.
func Derive(ctx context.Context, root string) (Derived, error) {
	return derive(ctx, root, load.HostConfiguration())
}

// derive is Derive with the host configuration supplied, which is what lets a
// test state the identifiers it expects rather than compute them.
func derive(ctx context.Context, root string, host load.Configuration) (Derived, error) {
	units, err := load.Units(ctx, root)
	if err != nil {
		return Derived{}, fmt.Errorf("read the build constraints under %s: %w", root, err)
	}
	var files []fileConstraint
	for _, unit := range units {
		held, err := collect(root, unit)
		if err != nil {
			return Derived{}, err
		}
		files = append(files, held...)
	}
	configurations := configurationsOf(host, atomsOf(files))
	return Derived{
		Configurations: configurations,
		Guessed:        guessedIn(configurations, host),
	}, nil
}

// atomsOf classifies every tag the files name onto the axis it belongs to. A test
// file's tags count as any file's. A file built only under the ignore tag names no
// atom, and the ignore tag is never one, so no derived configuration sets it.
func atomsOf(files []fileConstraint) atomSet {
	named := make(map[string]bool)
	for _, file := range files {
		if OnlyUnderIgnore(file.expr()) {
			continue
		}
		collectTags(file.expr(), named)
	}
	delete(named, ignoreTag)

	var atoms atomSet
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
func configurationsOf(host load.Configuration, atoms atomSet) []load.Configuration {
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

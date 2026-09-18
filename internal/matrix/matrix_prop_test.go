package matrix

import (
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
	"pgregory.net/rapid"
)

// drawnAtoms draws one atom set: names of both platform axes taken from the ones
// the toolchain reserves, and tags from a small closed set, each axis distinct
// and sorted the way collection leaves it.
func drawnAtoms(t *rapid.T) Atoms {
	systems := append(slices.Sorted(maps.Keys(platforms())), retiredOS...)
	arches := append(slices.Sorted(maps.Keys(architectures())), retiredArch...)
	tags := []string{"integration", "cgo", "purego", "netgo", "osusergo"}

	atoms := Atoms{
		OS:   rapid.SliceOfNDistinct(rapid.SampledFrom(systems), 0, 4, rapid.ID).Draw(t, "the operating-system atoms"),
		Arch: rapid.SliceOfNDistinct(rapid.SampledFrom(arches), 0, 4, rapid.ID).Draw(t, "the architecture atoms"),
		Tags: rapid.SliceOfNDistinct(rapid.SampledFrom(tags), 0, 3, rapid.ID).Draw(t, "the tag atoms"),
	}
	slices.Sort(atoms.OS)
	slices.Sort(atoms.Arch)
	slices.Sort(atoms.Tags)
	return atoms
}

// TestDerivationNeverEnumeratesCombinations is property dead-code-suite/P11-derivation:
// the matrix one atom set derives holds at most the host plus one configuration
// per atom, never their product; every atom the toolchain still builds for is
// carried by a configuration of its own axis; every configuration names a
// platform the toolchain builds and an identifier no other configuration shares;
// and the host comes first.
func TestDerivationNeverEnumeratesCombinations(t *testing.T) {
	t.Parallel()

	host := fixtureHost()
	rapid.Check(t, func(t *rapid.T) {
		atoms := drawnAtoms(t)
		configurations := configurationsOf(host, atoms)

		if bound := 1 + len(atoms.OS) + len(atoms.Arch) + len(atoms.Tags); len(configurations) > bound {
			t.Fatalf("configurationsOf(%+v) derived %d configurations, want at most %d",
				atoms, len(configurations), bound)
		}
		if got := configurations[0]; got.ID != host.ID {
			t.Fatalf("configurationsOf(%+v) derived %q first, want the host %q", atoms, got.ID, host.ID)
		}

		ids := make(map[string]bool, len(configurations))
		for _, c := range configurations {
			if ids[c.ID] {
				t.Fatalf("configurationsOf(%+v) derived %q twice", atoms, c.ID)
			}
			ids[c.ID] = true
			if !slices.Contains(platforms()[c.OS], c.Arch) {
				t.Fatalf("configurationsOf(%+v) derived %q, which names the pair %s/%s the toolchain does not build",
					atoms, c.ID, c.OS, c.Arch)
			}
			if len(c.Tags) > 1 {
				t.Fatalf("configurationsOf(%+v) derived %q carrying %v, want at most one tag per configuration",
					atoms, c.ID, c.Tags)
			}
		}

		for _, system := range atoms.OS {
			if _, built := archFor(system, host.Arch); built && !carried(configurations, system, "") {
				t.Fatalf("configurationsOf(%+v) derived no configuration for the operating system %q", atoms, system)
			}
		}
		for _, arch := range atoms.Arch {
			if _, built := osFor(arch, host.OS); built && !carried(configurations, "", arch) {
				t.Fatalf("configurationsOf(%+v) derived no configuration for the architecture %q", atoms, arch)
			}
		}
		for _, tag := range atoms.Tags {
			if count := carrying(configurations, tag); count != 1 {
				t.Fatalf("configurationsOf(%+v) derived %d configurations carrying the tag %q, want exactly one",
					atoms, count, tag)
			}
		}
	})
}

// carried reports whether some configuration names the operating system or the
// architecture given, an empty one standing for either.
func carried(configurations []load.Configuration, system, arch string) bool {
	return slices.ContainsFunc(configurations, func(c load.Configuration) bool {
		return (system == "" || c.OS == system) && (arch == "" || c.Arch == arch)
	})
}

// carrying counts the configurations that carry one tag.
func carrying(configurations []load.Configuration, tag string) int {
	count := 0
	for _, c := range configurations {
		if slices.Contains(c.Tags, tag) {
			count++
		}
	}
	return count
}

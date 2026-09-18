package matrix

import (
	"go/build"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/deadset-go/internal/load"
)

// platformList is every platform the toolchain builds, one operating system and
// architecture per field, exactly as the toolchain's own list prints them. A pair
// absent from it is one the toolchain refuses, so a configuration naming it would
// end a run rather than load, which is why derivation pairs an atom with a pair
// this list holds.
const platformList = `
aix/ppc64
android/386
android/amd64
android/arm
android/arm64
darwin/amd64
darwin/arm64
dragonfly/amd64
freebsd/386
freebsd/amd64
freebsd/arm
freebsd/arm64
illumos/amd64
ios/amd64
ios/arm64
js/wasm
linux/386
linux/amd64
linux/arm
linux/arm64
linux/loong64
linux/mips
linux/mips64
linux/mips64le
linux/mipsle
linux/ppc64
linux/ppc64le
linux/riscv64
linux/s390x
netbsd/386
netbsd/amd64
netbsd/arm
netbsd/arm64
openbsd/386
openbsd/amd64
openbsd/arm
openbsd/arm64
openbsd/ppc64
openbsd/riscv64
plan9/386
plan9/amd64
plan9/arm
solaris/amd64
wasip1/wasm
windows/386
windows/amd64
windows/arm64
`

// pairSeparator divides one platform's two axes in the list.
const pairSeparator = "/"

// platforms maps every operating system of the list to the architectures the
// toolchain builds it for, sorted.
var platforms = sync.OnceValue(func() map[string][]string {
	bySystem := make(map[string][]string)
	for pair := range strings.FieldsSeq(platformList) {
		system, arch, ok := strings.Cut(pair, pairSeparator)
		if !ok {
			continue
		}
		bySystem[system] = append(bySystem[system], arch)
	}
	for system := range bySystem {
		slices.Sort(bySystem[system])
	}
	return bySystem
})

// retiredOS and retiredArch are the names the file-name convention still
// reserves for targets the toolchain no longer builds. A file name or a build
// expression naming one carries a platform constraint all the same, and no
// configuration satisfies it, so such a file is unreachable by derivation.
var (
	retiredOS   = []string{"hurd", "nacl", "zos"}
	retiredArch = []string{
		"amd64p32", "arm64be", "armbe", "mips64p32", "mips64p32le",
		"ppc", "riscv", "s390", "sparc", "sparc64",
	}
)

// aliases maps an operating system to the name a build expression may still use
// for the system it grew out of, which the toolchain answers for it.
var aliases = map[string]string{
	"android": "linux",
	"illumos": "solaris",
	"ios":     "darwin",
}

// unixOS is the set of operating systems the unix build tag stands for.
var unixOS = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd",
	"illumos", "ios", "linux", "netbsd", "openbsd", "solaris",
}

// The tags the toolchain answers from its own environment rather than from a
// configuration's tag list.
const (
	unixTag       = "unix"
	cgoTag        = "cgo"
	gcCompiler    = "gc"
	gccgoCompiler = "gccgo"

	// boringcryptoTag is the older spelling of one experiment tag, which the
	// toolchain rewrites before consulting its experiment list.
	boringcryptoTag        = "boringcrypto"
	boringcryptoExperiment = "goexperiment.boringcrypto"
	experimentPrefix       = "goexperiment."

	// releasePrefix opens a language release tag, which every toolchain at or
	// above that release satisfies.
	releasePrefix = "go1."
)

// architectures maps every architecture the toolchain builds to the operating
// systems it builds it for, sorted: the platform list read the other way.
var architectures = sync.OnceValue(func() map[string][]string {
	byArch := make(map[string][]string)
	for system, arches := range platforms() {
		for _, arch := range arches {
			byArch[arch] = append(byArch[arch], system)
		}
	}
	for arch := range byArch {
		slices.Sort(byArch[arch])
	}
	return byArch
})

// isOS reports whether name is an operating-system name the toolchain's
// conventions reserve, whether or not it still builds for it.
func isOS(name string) bool {
	_, built := platforms()[name]
	return built || slices.Contains(retiredOS, name)
}

// isArch reports whether name is an architecture name the toolchain's
// conventions reserve, whether or not it still builds for it.
func isArch(name string) bool {
	_, built := architectures()[name]
	return built || slices.Contains(retiredArch, name)
}

// isToolchainTag reports whether the toolchain answers tag from its own
// environment: the compiler it is, the operating-system class it targets, the
// release it implements or an experiment it was built with. A configuration
// naming one of these decides nothing, so none is an axis of the matrix.
func isToolchainTag(tag string) bool {
	switch tag {
	case unixTag, gcCompiler, gccgoCompiler, boringcryptoTag:
		return true
	}
	return strings.HasPrefix(tag, experimentPrefix) || isReleaseTag(tag)
}

// isReleaseTag reports whether tag has the form of a language release tag.
func isReleaseTag(tag string) bool {
	rest, ok := strings.CutPrefix(tag, releasePrefix)
	if !ok {
		return false
	}
	minor, patch, versioned := strings.Cut(rest, ".")
	return digits(minor) && (!versioned || digits(patch))
}

// digits reports whether s is a non-empty run of decimal digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// archFor returns the architecture a configuration for one operating system is
// built with: preferred where the toolchain builds that pair, and otherwise the
// first architecture it builds that system for. It reports false for a system
// the toolchain builds nothing for.
func archFor(system, preferred string) (string, bool) {
	return partner(platforms()[system], preferred)
}

// osFor returns the operating system a configuration for one architecture is
// built for, by the same rule archFor applies to the other axis.
func osFor(arch, preferred string) (string, bool) {
	return partner(architectures()[arch], preferred)
}

// partner picks preferred out of candidates, falling back to the first of them,
// and reports false when there are none.
func partner(candidates []string, preferred string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	if slices.Contains(candidates, preferred) {
		return preferred, true
	}
	return candidates[0], true
}

// satisfies returns the function that answers whether configuration c satisfies
// one build tag. It is the toolchain's own rule with cgo disabled: every
// configuration loads with the C toolchain out of the way, so the cgo tag holds
// only where the configuration itself names it.
func satisfies(c load.Configuration) func(tag string) bool {
	return func(tag string) bool {
		switch {
		case tag == c.OS, tag == c.Arch, tag == runtime.Compiler:
			return true
		case tag == aliases[c.OS]:
			return true
		case tag == unixTag:
			return slices.Contains(unixOS, c.OS)
		case slices.Contains(c.Tags, tag):
			return true
		case tag == cgoTag:
			return false
		case tag == boringcryptoTag:
			tag = boringcryptoExperiment
		}
		return slices.Contains(build.Default.ReleaseTags, tag) ||
			slices.Contains(build.Default.ToolTags, tag)
	}
}

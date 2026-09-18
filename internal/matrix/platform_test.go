package matrix

import (
	"go/build"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// TestPlatformsAreTheToolchainsOwnList pins the platform table against the
// toolchain that will run the loads. A new port, or a port the toolchain drops,
// fails here: the table is what keeps derivation from naming a configuration the
// toolchain refuses, so it has to be brought back into step by hand.
func TestPlatformsAreTheToolchainsOwnList(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(), "go", "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("Setup: go tool dist list: %v", err)
	}

	built := make(map[string][]string)
	for line := range strings.FieldsSeq(string(out)) {
		system, arch, ok := strings.Cut(line, "/")
		if !ok {
			t.Fatalf("Setup: go tool dist list reported %q, want an operating system and an architecture", line)
		}
		built[system] = append(built[system], arch)
	}

	for _, system := range slices.Sorted(maps.Keys(built)) {
		want := built[system]
		slices.Sort(want)
		if got := platforms()[system]; !slices.Equal(got, want) {
			t.Errorf("platforms[%q] = %v, want the toolchain's %v", system, got, want)
		}
	}
	for _, system := range slices.Sorted(maps.Keys(platforms())) {
		if _, ok := built[system]; !ok {
			t.Errorf("the platform list names %q, which the toolchain no longer builds", system)
		}
	}
}

// TestEveryReservedNameConstrainsAFileByItsName reads the platform names a
// second way, through the toolchain's own file matching: a name the tables hold
// constrains a file that ends with it, and a name they do not hold constrains
// nothing.
func TestEveryReservedNameConstrainsAFileByItsName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		axis  string
		named bool
	}{
		{name: "linux", axis: "os", named: true},
		{name: "windows", axis: "os", named: true},
		{name: "js", axis: "os", named: true},
		{name: "nacl", axis: "os", named: true},
		{name: "zos", axis: "os", named: true},
		{name: "amd64", axis: "arch", named: true},
		{name: "wasm", axis: "arch", named: true},
		{name: "sparc64", axis: "arch", named: true},
		{name: "amd64p32", axis: "arch", named: true},
		{name: "solarium", axis: "os"},
		{name: "amd65", axis: "arch"},
		{name: "integration", axis: "os"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isOS(tc.name) || isArch(tc.name); got != tc.named {
				t.Errorf("isOS(%q) || isArch(%q) = %t, want %t", tc.name, tc.name, got, tc.named)
			}

			dir := t.TempDir()
			write(t, filepath.Join(dir, "probe_"+tc.name+".go"), []byte("package probe\n"))
			matching := selectsProbe(t, dir, "probe_"+tc.name+".go", tc.axis, tc.name)
			other := selectsProbe(t, dir, "probe_"+tc.name+".go", tc.axis, "other")

			if !matching {
				t.Errorf("the toolchain does not select probe_%s.go where the %s is %q, want it selected",
					tc.name, tc.axis, tc.name)
			}
			if wantOther := !tc.named; other != wantOther {
				t.Errorf("the toolchain selects probe_%s.go under another %s = %t, want %t",
					tc.name, tc.axis, other, wantOther)
			}
		})
	}
}

// TestTheUnixTagStandsForTheToolchainsOwnSet pins the unix set against the
// toolchain, over every operating-system name the tables hold.
func TestTheUnixTagStandsForTheToolchainsOwnSet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "unix.go"), []byte("//go:build unix\n\npackage probe\n"))

	systems := append(slices.Sorted(maps.Keys(platforms())), retiredOS...)
	for _, system := range systems {
		want := slices.Contains(unixOS, system)
		if got := selectsProbe(t, dir, "unix.go", "os", system); got != want {
			t.Errorf("the toolchain selects a unix file under %q = %t, want %t", system, got, want)
		}
	}
}

// TestAliasAnswersTheNameAnOperatingSystemGrewOutOf pins the alias table both
// ways: against the package's own tag answers and against the toolchain's, which
// answers the older name for the system that grew out of it and for no other.
func TestAliasAnswersTheNameAnOperatingSystemGrewOutOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		system string
		tag    string
		want   bool
	}{
		{system: "android", tag: "linux", want: true},
		{system: "illumos", tag: "solaris", want: true},
		{system: "ios", tag: "darwin", want: true},
		{system: "linux", tag: "android", want: false},
		{system: "darwin", tag: "ios", want: false},
		{system: "windows", tag: "linux", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.system+"_answers_"+tc.tag, func(t *testing.T) {
			t.Parallel()

			answers := satisfies(load.Configuration{ID: tc.system, OS: tc.system, Arch: fixtureArch})
			if got := answers(tc.tag); got != tc.want {
				t.Errorf("a %s configuration satisfies %q = %t, want %t", tc.system, tc.tag, got, tc.want)
			}

			dir := t.TempDir()
			write(t, filepath.Join(dir, "probe.go"), []byte("//go:build "+tc.tag+"\n\npackage probe\n"))
			if got := selectsProbe(t, dir, "probe.go", "os", tc.system); got != tc.want {
				t.Errorf("the toolchain selects a %q file under %s = %t, want %t", tc.tag, tc.system, got, tc.want)
			}
		})
	}
}

func TestNoNameIsBothAnOperatingSystemAndAnArchitecture(t *testing.T) {
	t.Parallel()

	for _, system := range append(slices.Sorted(maps.Keys(platforms())), retiredOS...) {
		if isArch(system) {
			t.Errorf("isArch(%q) = true, want false: the name is an operating system, and an atom lands on one axis", system)
		}
	}
	for _, arch := range append(slices.Sorted(maps.Keys(architectures())), retiredArch...) {
		if isOS(arch) {
			t.Errorf("isOS(%q) = true, want false: the name is an architecture, and an atom lands on one axis", arch)
		}
	}
}

func TestPartnerAxisNamesAPlatformTheToolchainBuilds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		atom      string
		preferred string
		axis      string
		want      string
		wantBuilt bool
	}{
		{name: "the_host_architecture_where_the_pair_is_built", atom: "windows", preferred: "amd64", axis: "os", want: "amd64", wantBuilt: true},
		{name: "another_architecture_where_it_is_not", atom: "js", preferred: "amd64", axis: "os", want: "wasm", wantBuilt: true},
		{name: "none_for_a_system_the_toolchain_dropped", atom: "nacl", preferred: "amd64", axis: "os"},
		{name: "the_host_system_where_the_pair_is_built", atom: "arm64", preferred: "linux", axis: "arch", want: "linux", wantBuilt: true},
		{name: "another_system_where_it_is_not", atom: "wasm", preferred: "linux", axis: "arch", want: "js", wantBuilt: true},
		{name: "none_for_an_architecture_the_toolchain_dropped", atom: "sparc", preferred: "linux", axis: "arch"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, built := archFor(tc.atom, tc.preferred)
			if tc.axis == "arch" {
				got, built = osFor(tc.atom, tc.preferred)
			}
			if got != tc.want || built != tc.wantBuilt {
				t.Errorf("partner of %q preferring %q = (%q, %t), want (%q, %t)",
					tc.atom, tc.preferred, got, built, tc.want, tc.wantBuilt)
			}
		})
	}
}

func TestIsReleaseTagReadsTheReleaseTagForm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tag  string
		want bool
	}{
		{tag: "go1.21", want: true},
		{tag: "go1.27", want: true},
		{tag: "go1.21.4", want: true},
		{tag: "go1", want: false},
		{tag: "go1.", want: false},
		{tag: "go1.x", want: false},
		{tag: "go1.21.", want: false},
		{tag: "gopher", want: false},
		{tag: "integration", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			t.Parallel()

			if got := isReleaseTag(tc.tag); got != tc.want {
				t.Errorf("isReleaseTag(%q) = %t, want %t", tc.tag, got, tc.want)
			}
		})
	}
}

// selectsProbe reports whether the toolchain selects one probe file with one
// axis of the platform set to value.
func selectsProbe(t *testing.T, dir, name, axis, value string) bool {
	t.Helper()

	context := build.Default
	context.CgoEnabled = false
	context.GOOS, context.GOARCH = "linux", "amd64"
	if axis == "arch" {
		context.GOARCH = value
	} else {
		context.GOOS = value
	}

	matched, err := context.MatchFile(dir, name)
	if err != nil {
		t.Fatalf("Setup: MatchFile(%s) with the %s set to %q: %v", name, axis, value, err)
	}
	return matched
}

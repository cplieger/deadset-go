package buildconstraint

import (
	"go/build"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

			if got := IsOS(tc.name) || IsArch(tc.name); got != tc.named {
				t.Errorf("IsOS(%q) || IsArch(%q) = %t, want %t", tc.name, tc.name, got, tc.named)
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

func TestNoNameIsBothAnOperatingSystemAndAnArchitecture(t *testing.T) {
	t.Parallel()

	for _, system := range append(slices.Sorted(maps.Keys(platforms())), retiredOS...) {
		if IsArch(system) {
			t.Errorf("IsArch(%q) = true, want false: the name is an operating system, and an atom lands on one axis", system)
		}
	}
	for _, arch := range append(slices.Sorted(maps.Keys(architectures())), retiredArch...) {
		if IsOS(arch) {
			t.Errorf("IsOS(%q) = true, want false: the name is an architecture, and an atom lands on one axis", arch)
		}
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

// unixOS is the set of operating systems the unix build tag stands for.
var unixOS = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd",
	"illumos", "ios", "linux", "netbsd", "openbsd", "solaris",
}

// write puts one probe file on disk.
func write(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

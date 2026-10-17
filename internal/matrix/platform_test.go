package matrix

import (
	"go/build"
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/buildconstraint"
	"github.com/cplieger/deadset-go/v2/internal/load"
)

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

			got, built := partner(buildconstraint.ArchitecturesOf(tc.atom), tc.preferred)
			if tc.axis == "arch" {
				got, built = partner(buildconstraint.SystemsOf(tc.atom), tc.preferred)
			}
			if got != tc.want || built != tc.wantBuilt {
				t.Errorf("partner of %q preferring %q = (%q, %t), want (%q, %t)",
					tc.atom, tc.preferred, got, built, tc.want, tc.wantBuilt)
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

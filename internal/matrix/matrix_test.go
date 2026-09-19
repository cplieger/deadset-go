package matrix

import (
	"errors"
	"go/build"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

func TestDeriveEmitsOneConfigurationPerAtomAndNeverTheirProduct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		archive     string
		wantIDs     []string
		wantGuessed []string
		wantAtoms   Atoms
		wantUnbuilt []File
	}{
		{
			name:        "three_platform_atoms_derive_four_configurations",
			archive:     "three-platforms.txtar",
			wantIDs:     []string{"linux-amd64", "darwin-amd64", "windows-amd64", "linux-arm64"},
			wantGuessed: []string{"darwin-amd64", "windows-amd64", "linux-arm64"},
			wantAtoms:   Atoms{OS: []string{"darwin", "linux", "windows"}, Arch: []string{"arm64"}},
		},
		{
			name:      "a_boolean_constraint_the_atoms_do_not_satisfy_is_unreachable",
			archive:   "boolean.txtar",
			wantIDs:   []string{"linux-amd64"},
			wantAtoms: Atoms{OS: []string{"linux"}, Arch: []string{"amd64"}},
			wantUnbuilt: []File{
				{Path: "guard.go", Constraint: "linux && !amd64"},
			},
		},
		{
			name:        "each_tag_derives_the_host_carrying_that_one_tag",
			archive:     "custom-tags.txtar",
			wantIDs:     []string{"linux-amd64", "linux-amd64-cgo", "linux-amd64-integration"},
			wantGuessed: []string{"linux-amd64-cgo", "linux-amd64-integration"},
			wantAtoms:   Atoms{Tags: []string{"cgo", "integration"}},
		},
		{
			name:        "an_atom_named_only_in_a_directory_no_configuration_builds_is_collected",
			archive:     "vanished-dir.txtar",
			wantIDs:     []string{"linux-amd64", "plan9-amd64"},
			wantGuessed: []string{"plan9-amd64"},
			wantAtoms:   Atoms{OS: []string{"plan9"}},
		},
		{
			name:        "a_legacy_line_a_blank_line_separates_from_the_code_carries_its_constraint",
			archive:     "legacy-lines.txtar",
			wantIDs:     []string{"linux-amd64", "openbsd-amd64", "linux-arm64"},
			wantGuessed: []string{"openbsd-amd64", "linux-arm64"},
			wantAtoms:   Atoms{OS: []string{"openbsd"}, Arch: []string{"arm64"}},
			wantUnbuilt: []File{
				{Path: "combined.go", Constraint: "openbsd && arm64"},
			},
		},
		{
			name:      "no_path_the_toolchain_builds_nothing_from_is_read",
			archive:   "walk-skips.txtar",
			wantIDs:   []string{"linux-amd64"},
			wantAtoms: Atoms{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			derived, err := derive(extract(t, tc.archive), fixtureHost())
			if err != nil {
				t.Fatalf("derive(%s) = error %v, want the derived matrix", tc.archive, err)
			}
			if got := identifiers(derived.Configurations); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Errorf("derive(%s) derived %v, want %v", tc.archive, got, tc.wantIDs)
			}
			// Every configuration but the host's is derivation's own answer about a
			// pairing, so a load may drop one; the host's is the run's own.
			if got := derived.Guessed; !slices.Equal(got, tc.wantGuessed) {
				t.Errorf("derive(%s) guessed %v, want %v", tc.archive, got, tc.wantGuessed)
			}
			if got := derived.Atoms; !sameAtoms(got, tc.wantAtoms) {
				t.Errorf("derive(%s) collected %+v, want %+v", tc.archive, got, tc.wantAtoms)
			}
			if got := derived.Unreachable; !slices.Equal(got, tc.wantUnbuilt) {
				t.Errorf("derive(%s) recorded unreachable %+v, want %+v", tc.archive, got, tc.wantUnbuilt)
			}
		})
	}
}

func TestDeriveMakesABooleanConstraintReachableOnceTheAtomItNeedsAppears(t *testing.T) {
	t.Parallel()

	dir := extract(t, "boolean.txtar")
	write(t, filepath.Join(dir, "serve_arm64.go"), []byte("package app\n\n// Wide is the wide path.\nconst Wide = true\n"))

	derived, err := derive(dir, fixtureHost())
	if err != nil {
		t.Fatalf("derive(boolean.txtar plus an arm64 file) = error %v, want the derived matrix", err)
	}
	want := []string{"linux-amd64", "linux-arm64"}
	if got := identifiers(derived.Configurations); !reflect.DeepEqual(got, want) {
		t.Errorf("derive(boolean.txtar plus an arm64 file) derived %v, want %v", got, want)
	}
	if got := derived.Unreachable; len(got) != 0 {
		t.Errorf("derive(boolean.txtar plus an arm64 file) recorded unreachable %+v, want none: linux-arm64 builds the guard", got)
	}
}

func TestDeriveDerivesTheHostConfigurationOfTheRunningBinary(t *testing.T) {
	t.Parallel()

	derived, err := Derive(extract(t, "walk-skips.txtar"))
	if err != nil {
		t.Fatalf("Derive(walk-skips.txtar) = error %v, want the derived matrix", err)
	}
	want := []load.Configuration{load.HostConfiguration()}
	if got := derived.Configurations; !reflect.DeepEqual(got, want) {
		t.Errorf("Derive(walk-skips.txtar) derived %+v, want the host alone, %+v", got, want)
	}
	if got := derived.Guessed; len(got) != 0 {
		t.Errorf("Derive(walk-skips.txtar) guessed %v, want nothing: the host's own configuration is no guess", got)
	}
}

func TestDeriveRefusesAFileTheToolchainWouldRefuse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{
			name:    "a_build_expression_that_does_not_parse",
			content: "//go:build linux &&\n\npackage app\n",
		},
		{
			name:    "a_second_build_expression",
			content: "//go:build linux\n//go:build amd64\n\npackage app\n",
			wantErr: errMultipleConstraints,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := extract(t, "boolean.txtar")
			write(t, filepath.Join(dir, "refused.go"), []byte(tc.content))

			_, err := derive(dir, fixtureHost())
			if err == nil {
				t.Fatalf("derive(a file carrying %q) = no error, want the file refused", tc.content)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("derive(a file carrying %q) = error %v, want %v", tc.content, err, tc.wantErr)
			}
			if got := err.Error(); !strings.Contains(got, "refused.go") {
				t.Errorf("derive(a file carrying %q) = error %q, want the file named", tc.content, got)
			}
		})
	}
}

// TestDeriveAgreesWithTheToolchainOnEveryFileOfEveryConfiguration reads each
// fixture's verdict a second way, through the toolchain's own file matching, so
// the header reading, the file-name rule and the tag answers are pinned against
// the rules they implement rather than against themselves.
func TestDeriveAgreesWithTheToolchainOnEveryFileOfEveryConfiguration(t *testing.T) {
	t.Parallel()

	archives := []string{
		"three-platforms.txtar", "boolean.txtar", "custom-tags.txtar",
		"vanished-dir.txtar", "legacy-lines.txtar", "walk-skips.txtar",
	}
	for _, archive := range archives {
		t.Run(archive, func(t *testing.T) {
			t.Parallel()

			root := extract(t, archive)
			files, err := collect(root)
			if err != nil {
				t.Fatalf("collect(%s) = error %v, want every file's constraint", archive, err)
			}
			derived, err := derive(root, fixtureHost())
			if err != nil {
				t.Fatalf("derive(%s) = error %v, want the derived matrix", archive, err)
			}

			for _, c := range derived.Configurations {
				for _, file := range files {
					want := toolchainSelects(t, root, file.path, c)
					got := selects(file, c)
					if got != want {
						t.Errorf("configuration %s selects %s = %t, want the toolchain's %t (constraint %q)",
							c.ID, file.path, got, want, constraintText(file))
					}
				}
			}
		})
	}
}

// selects reports whether one configuration builds one file, by the package's
// own reading of its constraint.
func selects(file fileConstraint, c load.Configuration) bool {
	expression := file.expr()
	return expression == nil || expression.Eval(satisfies(c))
}

// constraintText renders one file's constraint for a failure message.
func constraintText(file fileConstraint) string {
	if expression := file.expr(); expression != nil {
		return expression.String()
	}
	return ""
}

// toolchainSelects reports whether one configuration builds one file, by the
// toolchain's own file matching, with cgo disabled as every load has it.
func toolchainSelects(t *testing.T, root, relative string, c load.Configuration) bool {
	t.Helper()

	context := build.Default
	context.GOOS = c.OS
	context.GOARCH = c.Arch
	context.BuildTags = c.Tags
	context.CgoEnabled = false

	path := filepath.Join(root, filepath.FromSlash(relative))
	matched, err := context.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatalf("Setup: MatchFile(%s) for %s: %v", relative, c.ID, err)
	}
	return matched
}

// sameAtoms compares two atom sets, reading an absent list and an empty one as
// the same thing.
func sameAtoms(got, want Atoms) bool {
	return slices.Equal(got.OS, want.OS) && slices.Equal(got.Arch, want.Arch) &&
		slices.Equal(got.Tags, want.Tags)
}

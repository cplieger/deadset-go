package buildconstraint

import (
	"go/build"
	"go/build/constraint"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNameConstraintReadsThePlatformSuffixConvention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "an_operating_system_suffix", base: "listen_linux.go", want: "linux"},
		{name: "an_architecture_suffix", base: "vector_arm64.go", want: "arm64"},
		{name: "both_suffixes_in_that_order", base: "sys_linux_amd64.go", want: "linux && amd64"},
		{name: "both_suffixes_on_a_test_file", base: "sys_linux_amd64_test.go", want: "linux && amd64"},
		{name: "one_suffix_on_a_test_file", base: "sys_windows_test.go", want: "windows"},
		{name: "a_name_the_whole_of_which_is_a_platform", base: "linux.go", want: ""},
		{name: "a_test_file_naming_no_platform", base: "app_test.go", want: ""},
		{name: "a_suffix_that_is_no_platform", base: "app_integration.go", want: ""},
		{name: "the_axes_in_the_other_order", base: "sys_amd64_linux.go", want: "linux"},
		{name: "a_further_field_before_the_platform", base: "one_two_freebsd.go", want: "freebsd"},
		{name: "a_second_extension", base: "notes_plan9.go.txt", want: "plan9"},
		{name: "two_platform_suffixes_in_one_order", base: "stream_js_wasm.go", want: "js && wasm"},
		{name: "two_operating_systems", base: "gate_windows_plan9.go", want: "plan9"},
		{name: "the_unix_class_is_no_suffix", base: "reader_unix.go", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ""
			if expression := nameConstraint(tc.base); expression != nil {
				got = expression.String()
			}
			if got != tc.want {
				t.Errorf("nameConstraint(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestDeclaredConstraintReadsTheHeaderTheToolchainReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "a_build_expression_above_the_package_clause",
			content: "//go:build linux && amd64\n\npackage app\n",
			want:    "linux && amd64",
		},
		{
			name:    "a_build_expression_under_a_doc_comment",
			content: "// Package app is an app.\n//\n//go:build linux\n\npackage app\n",
			want:    "linux",
		},
		{
			name:    "a_build_expression_a_block_comment_encloses",
			content: "/*\n//go:build linux\n*/\n\npackage app\n",
			want:    "",
		},
		{
			name:    "a_build_expression_below_the_package_clause",
			content: "package app\n\n//go:build linux\n",
			want:    "",
		},
		{
			name:    "a_legacy_line_a_blank_line_separates_from_the_code",
			content: "// +build openbsd\n\npackage app\n",
			want:    "openbsd",
		},
		{
			name:    "a_legacy_line_the_package_clause_follows_at_once",
			content: "// +build openbsd\npackage app\n",
			want:    "",
		},
		{
			name:    "two_legacy_lines_are_both_conditions",
			content: "// +build openbsd\n// +build arm64\n\npackage app\n",
			want:    "openbsd && arm64",
		},
		{
			name:    "one_legacy_line_naming_alternatives",
			content: "// +build openbsd netbsd\n\npackage app\n",
			want:    "openbsd || netbsd",
		},
		{
			name:    "a_build_expression_outranks_a_legacy_line",
			content: "//go:build linux\n// +build openbsd\n\npackage app\n",
			want:    "linux",
		},
		{
			name:    "no_constraint_at_all",
			content: "// Package app is an app.\npackage app\n",
			want:    "",
		},
		{
			name:    "an_empty_file",
			content: "",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expression, err := declaredConstraint([]byte(tc.content))
			if err != nil {
				t.Fatalf("declaredConstraint(%q) = error %v, want the constraint", tc.content, err)
			}
			got := ""
			if expression != nil {
				got = expression.String()
			}
			if got != tc.want {
				t.Errorf("declaredConstraint(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestTagsRecordsEveryBranchOfAnExpression(t *testing.T) {
	t.Parallel()

	expression, err := declaredConstraint([]byte("//go:build (linux && !arm64) || (darwin && cgo)\n\npackage app\n"))
	if err != nil {
		t.Fatalf("declaredConstraint(an alternation) = error %v, want the constraint", err)
	}

	want := []string{"arm64", "cgo", "darwin", "linux"}
	if got := slices.Sorted(maps.Keys(Tags(expression))); !slices.Equal(got, want) {
		t.Errorf("Tags((linux && !arm64) || (darwin && cgo)) = %v, want %v", got, want)
	}
}

func TestDeclaredConstraintRefusesASecondBuildExpression(t *testing.T) {
	t.Parallel()

	if _, err := declaredConstraint([]byte("//go:build linux\n//go:build amd64\n\npackage app\n")); err != ErrMultipleConstraints {
		t.Errorf("declaredConstraint(two //go:build lines) = error %v, want %v", err, ErrMultipleConstraints)
	}
}

func TestOfJoinsTheNameAndTheHeaderStatingAConditionOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, base, content, want string
	}{
		{name: "the_name_alone", base: "sys_linux.go", content: "package app\n", want: "linux"},
		{name: "the_header_alone", base: "sys.go", content: "//go:build extra\n\npackage app\n", want: "extra"},
		{name: "both_halves", base: "sys_linux.go", content: "//go:build extra\n\npackage app\n", want: "linux && extra"},
		{name: "a_condition_both_state", base: "sys_linux.go", content: "//go:build linux\n\npackage app\n", want: "linux"},
		{name: "a_nested_repetition_is_the_files_own", base: "sys_linux.go", content: "//go:build linux || extra\n\npackage app\n", want: "linux && (linux || extra)"},
		{name: "neither", base: "sys.go", content: "package app\n", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expression, err := Of(tc.base, []byte(tc.content))
			if err != nil {
				t.Fatalf("Of(%q) = error %v, want the constraint", tc.base, err)
			}
			got := ""
			if expression != nil {
				got = expression.String()
			}
			if got != tc.want {
				t.Errorf("Of(%q, %q) = %q, want %q", tc.base, tc.content, got, tc.want)
			}
		})
	}
}

func TestOnlyUnderIgnoreHoldsWhereNoSettingWithoutIgnoreSatisfies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expression string
		want       bool
	}{
		{expression: "ignore", want: true},
		{expression: "ignore && linux", want: true},
		{expression: "(ignore && a) || (ignore && b)", want: true},
		{expression: "ignore || extra", want: false},
		{expression: "!ignore", want: false},
		{expression: "linux", want: false},
		{expression: "ignore && !ignore", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.expression, func(t *testing.T) {
			t.Parallel()

			parsed, err := constraint.Parse("//go:build " + tc.expression)
			if err != nil {
				t.Fatalf("Setup: parse %q: %v", tc.expression, err)
			}
			if got := OnlyUnderIgnore(parsed); got != tc.want {
				t.Errorf("OnlyUnderIgnore(%q) = %t, want %t", tc.expression, got, tc.want)
			}
		})
	}
	if OnlyUnderIgnore(nil) {
		t.Errorf("OnlyUnderIgnore(nil) = true, want false: a file no constraint decides is built")
	}
}

func TestHoldsWithAnswersSomeSettingOfTheOtherTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expression string
		value      bool
		want       bool
	}{
		{expression: "gofuzz", value: true, want: true},
		{expression: "!gofuzz", value: true, want: false},
		{expression: "!gofuzz || linux", value: true, want: true},
		{expression: "linux && !linux", value: true, want: false},
		{expression: "gofuzz", value: false, want: false},
	}
	for _, tc := range tests {
		parsed, err := constraint.Parse("//go:build " + tc.expression)
		if err != nil {
			t.Fatalf("Setup: parse %q: %v", tc.expression, err)
		}
		if got := HoldsWith(parsed, "gofuzz", tc.value); got != tc.want {
			t.Errorf("HoldsWith(%q, gofuzz, %t) = %t, want %t", tc.expression, tc.value, got, tc.want)
		}
	}
}

// Of reads a file opening with a byte-order mark as the toolchain does: the mark is
// skipped and the header below it constrains the file.
func TestOfAgreesWithTheToolchainOnAByteOrderMark(t *testing.T) {
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled = "linux", "amd64", false
	dir := t.TempDir()
	for name, content := range map[string]string{
		"windows.go": byteOrderMark + "//go:build windows\n\npackage app\n",
		"linux.go":   byteOrderMark + "//go:build linux\n\npackage app\n",
		"legacy.go":  byteOrderMark + "// +build windows\n\npackage app\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		matched, err := ctxt.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("Setup: MatchFile(%s) = _, %v", name, err)
		}
		expression, err := Of(name, []byte(content))
		if err != nil {
			t.Fatalf("Of(%s) = _, %v, want the constraint", name, err)
		}
		holds := expression == nil || expression.Eval(func(tag string) bool { return tag == "linux" || tag == "amd64" || tag == "unix" })
		if holds != matched {
			t.Errorf("Of(%s) = %v, holding on linux-amd64 %t; the toolchain matches it %t", name, expression, holds, matched)
		}
	}
}

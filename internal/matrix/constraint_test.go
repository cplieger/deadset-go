package matrix

import (
	"maps"
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

func TestCollectTagsRecordsEveryBranchOfAnExpression(t *testing.T) {
	t.Parallel()

	expression, err := declaredConstraint([]byte("//go:build (linux && !arm64) || (darwin && cgo)\n\npackage app\n"))
	if err != nil {
		t.Fatalf("declaredConstraint(an alternation) = error %v, want the constraint", err)
	}

	named := make(map[string]bool)
	collectTags(expression, named)
	want := []string{"arm64", "cgo", "darwin", "linux"}
	if got := slices.Sorted(maps.Keys(named)); !slices.Equal(got, want) {
		t.Errorf("collectTags((linux && !arm64) || (darwin && cgo)) = %v, want %v", got, want)
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantStdout []string
		wantStderr []string
		wantCode   int
	}{
		{
			name:       "version_prints_both_versions",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: []string{"deadset-go 0.1.0-dev\n", "contract 0.1.0\n"},
		},
		{
			name:       "no_arguments_is_a_usage_error",
			args:       nil,
			wantCode:   2,
			wantStderr: []string{"usage: deadset-go", "analyze", "explain", "print-config", "print-roots", "print-retained", "describe", "version"},
		},
		{
			name:       "fix_flag_is_refused_by_name",
			args:       []string{"analyze", "--fix"},
			wantCode:   2,
			wantStderr: []string{"--fix", "not supported"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != tc.wantCode {
				t.Errorf("run(%q) = %d, want %d\nstdout: %q\nstderr: %q", tc.args, got, tc.wantCode, stdout.String(), stderr.String())
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("run(%q) stdout = %q, want it to contain %q", tc.args, stdout.String(), want)
				}
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("run(%q) stderr = %q, want it to contain %q", tc.args, stderr.String(), want)
				}
			}
			if tc.wantCode == 0 && stderr.Len() != 0 {
				t.Errorf("run(%q) stderr = %q, want empty on exit 0", tc.args, stderr.String())
			}
			if tc.wantCode != 0 && stdout.Len() != 0 {
				t.Errorf("run(%q) stdout = %q, want empty on exit %d", tc.args, stdout.String(), tc.wantCode)
			}
		})
	}
}

package config

import "testing"

func TestOriginString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin origin
		want   string
	}{
		{name: "a_default", origin: origin{source: sourceDefault}, want: "default"},
		{
			name:   "a_repository_configuration",
			origin: origin{source: sourceRepository, Label: "/src/app/deadset.json"},
			want:   "repository: /src/app/deadset.json",
		},
		{
			name:   "a_central_configuration",
			origin: origin{source: sourceCentral, Label: "/etc/deadset/central.json"},
			want:   "central: /etc/deadset/central.json",
		},
		{
			name:   "a_flag",
			origin: origin{source: sourceFlag, Label: "--min-confidence"},
			want:   "flag: --min-confidence",
		},
		{
			name:   "a_default_ignores_a_label",
			origin: origin{source: sourceDefault, Label: "ignored"},
			want:   "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.origin.String(); got != tc.want {
				t.Errorf("origin{%q, %q}.String() = %q, want %q", tc.origin.source, tc.origin.Label, got, tc.want)
			}
		})
	}
}

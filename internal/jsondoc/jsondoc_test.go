package jsondoc

import "testing"

func TestEncodeWritesTheCharactersAPageEscapesAsThemselves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, indent string
		value        any
		want         string
	}{
		{name: "compact", value: map[string]string{"pattern": "<a&b>"}, want: "{\"pattern\":\"<a&b>\"}\n"},
		{name: "indented", indent: "  ", value: []string{"<%", "%>"}, want: "[\n  \"<%\",\n  \"%>\"\n]\n"},
		{name: "separators", value: "a\u2028b", want: "\"a\\u2028b\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Encode(tc.value, tc.indent)
			if err != nil {
				t.Fatalf("Encode(%v, %q) = %v", tc.value, tc.indent, err)
			}
			if string(got) != tc.want {
				t.Errorf("Encode(%v, %q) = %q, want %q", tc.value, tc.indent, got, tc.want)
			}
		})
	}
}

package report

import (
	"bytes"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// TestTheJSONRenderingsWriteMarkupCharactersAsThemselves pins the string encoding of
// the two JSON renderings: `<`, `>` and `&` are written as themselves, and the line
// and paragraph separators are written as escapes.
func TestTheJSONRenderingsWriteMarkupCharactersAsThemselves(t *testing.T) {
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{findingOf("DS1002", "unused-unexported", "a.go", 4, 8,
		config.Deny, "deletable", "the function <T> & its \u2028 caller \u2029 are unreferenced")}
	envelope := built(t, &in)

	for _, format := range []string{"json", "sarif"} {
		t.Run(format, func(t *testing.T) {
			written := rendered(t, format, &envelope, Options{Read: lines()})

			if want := []byte(`the function <T> & its \u2028 caller \u2029 are unreferenced`); !bytes.Contains(written, want) {
				t.Errorf("the %s rendering holds no %s:\n%s", format, want, written)
			}
			for _, escape := range []string{`\u003c`, `\u003e`, `\u0026`} {
				if bytes.Contains(written, []byte(escape)) {
					t.Errorf("the %s rendering writes the escape %s, want the character itself", format, escape)
				}
			}
		})
	}
}

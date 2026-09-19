package report

import (
	"errors"
	"strings"
	"testing"
)

// TestTheTemplateRendersTheEnvelope pins that a template reaches the envelope's own
// members, which is what a user-supplied rendering is for.
func TestTheTemplateRendersTheEnvelope(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	var held strings.Builder
	text := "{{ .Totals.Findings }} findings under {{ .Analyzer.Name }}\n" +
		"{{ range .Findings }}{{ .Code }} {{ .Symbol.Name }}\n{{ end }}"
	if err := Template(&held, &envelope, Options{Template: text}); err != nil {
		t.Fatalf("Template() = error %v, want the rendering", err)
	}
	want := "6 findings under deadset-go\nDS1301 write-only-symbol\n"
	if !strings.HasPrefix(held.String(), want) {
		t.Errorf("Template() = %q, want a rendering starting %q", held.String(), want)
	}
}

// TestTheTemplateFailsOnAnAbsentField pins that a template naming something the envelope
// does not carry fails and names it, rather than rendering that place as nothing: a report
// with a silently empty column is one a reader draws the wrong conclusion from.
func TestTheTemplateFailsOnAnAbsentField(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	var held strings.Builder
	err := Template(&held, &envelope, Options{Template: "{{ .Totals.Findings }} {{ .Invented }}\n"})
	if !errors.Is(err, ErrOptions) {
		t.Fatalf("Template(a template naming an absent field) = error %v, want one carrying ErrOptions", err)
	}
	if !strings.Contains(err.Error(), "Invented") {
		t.Errorf("Template(a template naming an absent field) = error %q, want one naming the field", err)
	}
	if held.String() != "" {
		t.Errorf("Template() wrote %q before failing, want a rendering built whole or not at all",
			held.String())
	}
}

// TestTheTemplateNeedsATemplate pins that a rendering with no template is refused rather
// than writing nothing and reporting success.
func TestTheTemplateNeedsATemplate(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)

	if err := Template(&strings.Builder{}, &envelope, Options{}); !errors.Is(err, ErrOptions) {
		t.Errorf("Template(no template) = error %v, want one carrying ErrOptions", err)
	}
}

// TestTheTemplateReportsAnUnparseableTemplate pins that a template the parser refuses is
// named as such.
func TestTheTemplateReportsAnUnparseableTemplate(t *testing.T) {
	in := minimalInput()
	envelope := built(t, &in)

	err := Template(&strings.Builder{}, &envelope, Options{Template: "{{ .Totals"})
	if !errors.Is(err, ErrOptions) {
		t.Errorf("Template(an unparseable template) = error %v, want one carrying ErrOptions", err)
	}
}

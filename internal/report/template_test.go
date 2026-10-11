package report

import (
	"errors"
	"strings"
	"testing"
)

// parsedTemplate parses one template the test supplies.
func parsedTemplate(t *testing.T, text string) *ParsedTemplate {
	t.Helper()

	parsed, err := ParseTemplate(text)
	if err != nil {
		t.Fatalf("Setup: ParseTemplate(%q) = error %v, want the template", text, err)
	}
	return parsed
}

// TestTheTemplateRendersTheReportDocument pins that a template reads the report
// document the envelope encodes to by its JSON member names.
func TestTheTemplateRendersTheReportDocument(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	var held strings.Builder
	text := "{{ .totals.findings }} findings under {{ .analyzer.name }}\n" +
		"{{ range .findings }}{{ .code }} {{ .symbol.name }}\n{{ end }}"
	if err := Template(&held, &envelope, Options{Template: parsedTemplate(t, text)}); err != nil {
		t.Fatalf("Template() = error %v, want the rendering", err)
	}
	want := "6 findings under deadset-go\nDS1301 write-only-symbol\n"
	if !strings.HasPrefix(held.String(), want) {
		t.Errorf("Template() = %q, want a rendering starting %q", held.String(), want)
	}
}

// TestTheTemplateFailsOnAnAbsentField pins that a template naming a member the report
// document does not carry fails and names it, rather than rendering that place as
// nothing.
func TestTheTemplateFailsOnAnAbsentField(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)

	var held strings.Builder
	parsed := parsedTemplate(t, "{{ .totals.findings }} {{ .invented }}\n")
	err := Template(&held, &envelope, Options{Template: parsed})
	if !errors.Is(err, errOptions) {
		t.Fatalf("Template(a template naming an absent field) = error %v, want one carrying errOptions", err)
	}
	if !strings.Contains(err.Error(), "invented") {
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

	if err := Template(&strings.Builder{}, &envelope, Options{}); !errors.Is(err, errOptions) {
		t.Errorf("Template(no template) = error %v, want one carrying errOptions", err)
	}
}

// TestParseTemplateRefusesAnUnparseableTemplate pins that a template the parser refuses
// is refused when it is parsed, which is before any rendering exists to fail.
func TestParseTemplateRefusesAnUnparseableTemplate(t *testing.T) {
	parsed, err := ParseTemplate("{{ .totals")
	if err == nil {
		t.Fatalf("ParseTemplate(an unclosed action) = %v, nil, want an error", parsed)
	}
	if !strings.Contains(err.Error(), "parse the template") {
		t.Errorf("ParseTemplate(an unclosed action) = error %q, want one saying the parse failed", err)
	}
}

// TestParseTemplateRefusesTheFormsOutsideTheSubset pins that every number form but a
// decimal integer, a block, a definition, an octal escape in any branch and a function
// outside the subset are refused at parse.
func TestParseTemplateRefusesTheFormsOutsideTheSubset(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"leading-zero":  "{{print 01}}",
		"base-prefix":   "\n{{print 0x1f}}",
		"underscore":    "{{print 1_000}}",
		"exponent":      "{{print 1e2}}",
		"imaginary":     "{{print 1i}}",
		"block":         `{{block "x" .}}y{{end}}`,
		"define-itself": `{{define "report"}}y{{end}}`,
		"slice":         "{{slice .findings 0}}",
		"call":          "{{call .findings}}",
		"octal-in-else": `{{if .findings}}{{else}}{{print "\0"}}{{end}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseTemplate(text)
			if err == nil {
				t.Fatalf("ParseTemplate(%q) = %v, want a refusal", text, parsed)
			}
			if !strings.Contains(err.Error(), "parse the template") {
				t.Errorf("ParseTemplate(%q) = error %q, want one saying the parse failed", text, err)
			}
		})
	}
}

// TestTheTemplateFailsWhatItCannotRender pins that a range over a value other than
// an array, an object or null fails, and so does nil as a command or as the pipeline
// of a control structure, each writing nothing.
func TestTheTemplateFailsWhatItCannotRender(t *testing.T) {
	t.Parallel()

	document := []byte(`{"count": 3, "name": "x", "none": null}`)
	for _, text := range []string{
		"{{range .count}}x{{end}}",
		"{{range .name}}x{{end}}",
		"{{range $i, $v := .count}}x{{end}}",
		"before {{nil}}",
		"{{if nil}}t{{else}}f{{end}}",
		"{{with nil}}t{{else}}f{{end}}",
		"{{range nil}}t{{else}}f{{end}}",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			var out strings.Builder
			err := parsedTemplate(t, text).execute(&out, document)
			if !errors.Is(err, errOptions) || out.Len() != 0 {
				t.Errorf("render(%q) = %v and wrote %q, want a failure carrying errOptions and nothing written",
					text, err, out.String())
			}
		})
	}
}

// TestTheTemplatePrintsNull pins that null prints as <no value> in an action and as
// <nil> as an operand, and that a range over null runs its else branch.
func TestTheTemplatePrintsNull(t *testing.T) {
	t.Parallel()

	text := "{{.none}}|{{print .none}}|{{print nil 1}}|{{range .none}}x{{else}}empty{{end}}"
	var out strings.Builder
	if err := parsedTemplate(t, text).execute(&out, []byte(`{"none": null}`)); err != nil {
		t.Fatalf("render(%q) = %v, want the rendering", text, err)
	}
	if want := "<no value>|<nil>|<nil> 1|empty"; out.String() != want {
		t.Errorf("render(%q) = %q, want %q", text, out.String(), want)
	}
}

// TestTheTemplateComparesBooleansForEqualityOnly pins that eq and ne compare two
// booleans, and that an ordering of two booleans fails.
func TestTheTemplateComparesBooleansForEqualityOnly(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	text := "{{eq true true}} {{eq .on false}} {{ne .on false}}"
	if err := parsedTemplate(t, text).execute(&out, []byte(`{"on": true}`)); err != nil || out.String() != "true false true" {
		t.Errorf("render(%q) = %q, %v, want %q", text, out.String(), err, "true false true")
	}
	if err := parsedTemplate(t, "{{lt false true}}").execute(&strings.Builder{}, []byte(`{}`)); err == nil {
		t.Error(`render("{{lt false true}}") = nil, want a failure`)
	}
}

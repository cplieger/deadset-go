package exempt

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

// retainedRows renders one exemption per line as the symbol held back, the class,
// the site the evidence was found at and the detail, which is what a reader of the
// retained listing sees.
func retainedRows(t *testing.T, in *Input, got []graph.Exemption) []string {
	t.Helper()
	names := make(map[graph.SymbolID]string, len(in.Symbols))
	for i := range in.Symbols {
		names[in.Symbols[i].ID] = in.Symbols[i].Name
	}
	rows := make([]string, 0, len(got))
	for _, e := range got {
		name, held := names[e.ID]
		if !held {
			t.Errorf("exemption names %q, which the inventory does not hold", e.ID)
			name = string(e.ID)
		}
		rows = append(rows, fmt.Sprintf("%s %s %s %s", name, e.Class, e.Site, e.Detail))
	}
	return rows
}

func TestTemplateFieldDetector(t *testing.T) {
	base := inputOf(t, "template-field.txtar", Options{})

	t.Run("a configured directory retains what its templates name", func(t *testing.T) {
		in := *base
		in.Options = Options{TemplateDirs: []string{"templates"}}

		got, err := TemplateFieldDetector(&in)
		if err != nil {
			t.Fatalf("TemplateFieldDetector(template-field.txtar, dirs=[templates]) error: %v", err)
		}
		want := []string{
			"Note.Title template-field templates/page.tmpl:1:8 named by {{.Title}}",
			"Page.Title template-field templates/page.tmpl:1:8 named by {{.Title}}",
			"Page.Byline template-field templates/page.tmpl:3:9 named by {{$p.Byline}}",
			"Note.Title template-field templates/partial/footer.tmpl:1:12 named by {{.Title}}",
			"Page.Title template-field templates/partial/footer.tmpl:1:12 named by {{.Title}}",
		}
		if rows := retainedRows(t, &in, got); !slices.Equal(rows, want) {
			t.Errorf("TemplateFieldDetector(template-field.txtar, dirs=[templates]) = %q, want %q", rows, want)
		}
	})

	t.Run("no configured directory retains nothing", func(t *testing.T) {
		in := *base
		in.Options = Options{}

		got, err := TemplateFieldDetector(&in)
		if err != nil {
			t.Fatalf("TemplateFieldDetector(template-field.txtar, dirs=[]) error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("TemplateFieldDetector(template-field.txtar, dirs=[]) = %q, want no exemption",
				retainedRows(t, &in, got))
		}
	})

	t.Run("a directory the target does not hold refuses the run", func(t *testing.T) {
		in := *base
		in.Options = Options{TemplateDirs: []string{"absent"}}

		got, err := TemplateFieldDetector(&in)
		if !errors.Is(err, ErrTemplateDir) {
			t.Errorf("TemplateFieldDetector(template-field.txtar, dirs=[absent]) error = %v, want %v",
				err, ErrTemplateDir)
		}
		if got != nil {
			t.Errorf("TemplateFieldDetector(template-field.txtar, dirs=[absent]) = %q, want no exemption",
				retainedRows(t, &in, got))
		}
	})

	t.Run("a directory outside the target root refuses the run", func(t *testing.T) {
		in := *base
		in.Options = Options{TemplateDirs: []string{"../escape"}}

		if _, err := TemplateFieldDetector(&in); !errors.Is(err, ErrTemplateDir) {
			t.Errorf("TemplateFieldDetector(template-field.txtar, dirs=[../escape]) error = %v, want %v",
				err, ErrTemplateDir)
		}
	})
}

func TestTemplateFieldDetectorSkipsAnUnparsableFile(t *testing.T) {
	in := inputOf(t, "template-field-unparsed.txtar", Options{TemplateDirs: []string{"templates"}})

	got, err := TemplateFieldDetector(in)
	if err != nil {
		t.Fatalf("TemplateFieldDetector(template-field-unparsed.txtar, dirs=[templates]) error: %v", err)
	}
	want := []string{"Page.Title template-field templates/good.tmpl:1:8 named by {{.Title}}"}
	if rows := retainedRows(t, in, got); !slices.Equal(rows, want) {
		t.Errorf("TemplateFieldDetector(template-field-unparsed.txtar, dirs=[templates]) = %q, want %q",
			rows, want)
	}
}

func TestTemplatePosition(t *testing.T) {
	// The second line holds a rune outside the basic multilingual plane, which
	// is two UTF-16 code units and one rune, so a column counting runes reads
	// one less than the column a report carries.
	src := []byte("héllo\n\U0001F600{{ .Title }}\n")
	cases := []struct {
		name string
		at   int
		want string
	}{
		{name: "the first line", at: 0, want: "page.tmpl:1:1"},
		{name: "after a two-byte rune", at: 3, want: "page.tmpl:1:3"},
		{name: "the second line", at: 7, want: "page.tmpl:2:1"},
		{name: "after a rune outside the basic plane", at: 11, want: "page.tmpl:2:3"},
		{name: "past the end", at: len(src) + 10, want: "page.tmpl:3:1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := templatePosition("page.tmpl", src, c.at).String(); got != c.want {
				t.Errorf("templatePosition(page.tmpl, %d) = %s, want %s", c.at, got, c.want)
			}
		})
	}
}

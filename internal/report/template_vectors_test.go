package report

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec/v7"
)

// templateVectorRoot is where the pinned Contract publishes the template vectors.
const templateVectorRoot = "vectors/template"

// The exit codes a template vector names: 2 for a template refused at parse, before
// any analysis, and 3 for a rendering that fails.
const (
	templateRefused = 2
	renderingFailed = 3
)

// TestPublishedTemplateVectors renders every published case and compares its bytes
// with the case's expected.txt, or holds the template to the outcome its
// expected_exit names: refused at parse, or a rendering that fails, carries
// errOptions and writes nothing.
func TestPublishedTemplateVectors(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(spec.Vectors, templateVectorRoot)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", templateVectorRoot, err)
	}
	var cases int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cases++
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := path.Join(templateVectorRoot, name)
			text := string(templateVectorFile(t, dir, "template.tmpl"))
			document := templateVectorFile(t, dir, "report.json")
			want, wantExit := templateVectorOutcome(t, dir)

			parsed, err := ParseTemplate(text)
			if wantExit == templateRefused {
				if err == nil {
					t.Fatalf("ParseTemplate(%q) parsed the template, want it refused before any analysis", text)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTemplate(%q) = %v, want the template parsed", text, err)
			}
			var out bytes.Buffer
			err = parsed.execute(&out, document)
			if wantExit == renderingFailed {
				if !errors.Is(err, errOptions) || out.Len() != 0 {
					t.Errorf("render(%q) = %v and wrote %q, want a failure carrying errOptions and nothing written",
						text, err, out.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("render(%q) = %v, want %q", text, err, want)
			}
			if got := out.String(); got != want {
				t.Errorf("render(%q) = %q, want expected.txt %q", text, got, want)
			}
		})
	}
	if cases == 0 {
		t.Errorf("%s holds no case, want the published vectors", templateVectorRoot)
	}
}

// templateVectorOutcome is a case's expected rendering, or the exit code it ends the
// run with.
func templateVectorOutcome(t *testing.T, dir string) (rendering string, exit int) {
	t.Helper()

	if expected, err := fs.ReadFile(spec.Vectors, path.Join(dir, "expected.txt")); err == nil {
		return string(expected), 0
	}
	exit, err := strconv.Atoi(strings.TrimSpace(string(templateVectorFile(t, dir, "expected_exit"))))
	if err != nil || (exit != templateRefused && exit != renderingFailed) {
		t.Fatalf("Setup: %s/expected_exit = %d, %v, want %d or %d", dir, exit, err, templateRefused, renderingFailed)
	}
	return "", exit
}

// templateVectorFile is one file of a published case.
func templateVectorFile(t *testing.T, dir, name string) []byte {
	t.Helper()

	data, err := spec.Vectors.ReadFile(path.Join(dir, name))
	if err != nil {
		t.Fatalf("Setup: read %s/%s: %v", dir, name, err)
	}
	return data
}

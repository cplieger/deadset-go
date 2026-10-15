package report

import (
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
)

// TestOneAnnotationPerRecord pins that every finding, stale suppression, type-error
// skip and note is annotated, and nothing else is.
func TestOneAnnotationPerRecord(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	written := annotationLines(t, &envelope)

	if want := len(envelope.Findings) + len(envelope.StaleSuppressions) + len(envelope.TypeErrorSkips) + len(envelope.Notes); len(written) != want {
		t.Fatalf("the annotation rendering wrote %d lines, want %d", len(written), want)
	}
	for i, line := range written {
		if !strings.HasPrefix(line, "::") {
			t.Errorf("line %d is not a workflow command: %q", i+1, line)
		}
	}
}

// TestTheAnnotationCommandFollowsTheSeverityAlone pins that a finding is annotated by
// its own severity, deny as an error, warn as a warning and allow as a notice, and
// that a stale suppression is annotated as an error.
func TestTheAnnotationCommandFollowsTheSeverityAlone(t *testing.T) {
	cases := []struct {
		severity config.Severity
		want     string
	}{
		{config.Deny, "::error "},
		{config.Warn, "::warning "},
		{config.Allow, "::notice "},
	}
	for _, one := range cases {
		t.Run(string(one.severity), func(t *testing.T) {
			in := minimalInput()
			in.Result.Findings = []kinds.Finding{findingOf("DS1002", "unused-unexported", "a.go", 4, 8,
				one.severity, "deletable", "the function has no reference in the target")}
			envelope := built(t, &in)

			if got := annotationLines(t, &envelope)[0]; !strings.HasPrefix(got, one.want) {
				t.Errorf("a %s finding is annotated %q, want a line starting %q", one.severity, got, one.want)
			}
		})
	}

	in := fullInput()
	staleSuppressionsOnly(&in)
	envelope := built(t, &in)
	got := annotationLines(t, &envelope)[0]
	if !strings.HasPrefix(got, "::error ") {
		t.Errorf("a stale suppression is annotated %q, want a line starting %q", got, "::error ")
	}
}

// TestANoteIsAWarningAnnotationAtItsDirectory pins that each note is annotated as a
// warning naming the directory it is about, titled by its kind.
func TestANoteIsAWarningAnnotationAtItsDirectory(t *testing.T) {
	in := minimalInput()
	in.Notes = []Note{{Kind: "unmapped-entry", Path: "cmd/tool", Key: "k", Message: "the entry, read back, names no file: name it"}}
	envelope := built(t, &in)

	written := annotationLines(t, &envelope)
	want := "::warning file=cmd/tool,title=unmapped-entry::the entry, read back, names no file: name it"
	if len(written) != 1 || written[0] != want {
		t.Errorf("Annotations(one note) = %q, want [%q]", written, want)
	}
}

// TestTheAnnotationNamesThePositionAndTheKind pins the properties a consumer places an
// annotation by, and the title a reader identifies it by.
func TestTheAnnotationNamesThePositionAndTheKind(t *testing.T) {
	in := fullInput()
	withoutStaleSuppressions(&in)
	in.Result.Findings = in.Result.Findings[:1]
	envelope := built(t, &in)

	want := "::error file=go.mod,line=12,col=2,endLine=12,title=DS1601 unused-dependency::" +
		"required by no package in the build list"
	if got := annotationLines(t, &envelope)[0]; got != want {
		t.Errorf("the annotation = %q, want %q", got, want)
	}
}

// TestTheStaleSuppressionAnnotationNamesItsKind pins that a record with no kind of its
// own is titled from the vocabulary rather than from a spelling of this package's own.
func TestTheStaleSuppressionAnnotationNamesItsKind(t *testing.T) {
	in := fullInput()
	staleSuppressionsOnly(&in)
	envelope := built(t, &in)

	want := "::error file=deadset-ignore.json,line=7,col=5,endLine=7,title=DS1703 stale-suppression::" +
		"the ignore entry named DS1001 and the symbol it names reports DS1003"
	if got := annotationLines(t, &envelope)[0]; got != want {
		t.Errorf("the annotation = %q, want %q", got, want)
	}
}

// TestATypeErrorSkipIsAWarningAnnotation pins that each type-error skip is annotated
// as a warning at its file and line, naming the compiler's message.
func TestATypeErrorSkipIsAWarningAnnotation(t *testing.T) {
	in := minimalInput()
	in.TypeErrorSkips = []TypeErrorSkip{{Path: "app/main.go", Line: 9, Message: "undefined: missing"}}
	envelope := built(t, &in)

	written := annotationLines(t, &envelope)
	want := "::warning file=app/main.go,line=9,title=type error skipped::" +
		"the analysis did not evaluate the function or statement holding this type error: undefined: missing"
	if len(written) != 1 || written[0] != want {
		t.Errorf("Annotations(one type-error skip) = %q, want [%q]", written, want)
	}
}

// TestTheAnnotationEscapesWhatWouldTruncateIt pins the five characters a workflow
// command reads as structure, each in the place it would do damage.
func TestTheAnnotationEscapesWhatWouldTruncateIt(t *testing.T) {
	found := findingOf("DS1002", "unused-unexported", "odd:name,dir/100%.go", 4, 8,
		config.Deny, "deletable", "the function 100% has no reference, and none from a consumer")
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{found}
	envelope := built(t, &in)

	got := annotationLines(t, &envelope)[0]
	if !strings.Contains(got, "file=odd%3Aname%2Cdir/100%25.go") {
		t.Errorf("the annotation does not escape the separators of its path: %q", got)
	}
	if !strings.Contains(got, "::the function 100%25 has no reference, and none from a consumer") {
		t.Errorf("the annotation does not escape the escape character of its message: %q", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("the annotation spans more than one line: %q", got)
	}
}

// annotationLines is every line of an annotation rendering.
func annotationLines(t *testing.T, e *Envelope) []string {
	t.Helper()

	written := string(rendered(t, "annotations", e, Options{}))
	if written == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(written, "\n"), "\n")
}

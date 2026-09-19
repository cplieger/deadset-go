package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// explained is one run of the explain verb: what it wrote to each stream and the
// code it returned.
type explained struct {
	stdout string
	stderr string
	code   int
}

// runExplain invokes the verb over dir with the arguments given, from the target, so
// a position the answer prints is the target-relative one every other verb prints.
//
// A test that calls this is sequential, because the working directory is the
// process's.
func runExplain(t *testing.T, dir string, args ...string) explained {
	t.Helper()

	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	invoked := append([]string{"explain", "--target=."}, args...)
	code := run(t.Context(), invoked, &stdout, &stderr)
	return explained{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func TestExplainAnswersAboutAReportedSymbolWithWhatTheFindingCarries(t *testing.T) {
	got := runExplain(t, findingsFixture(t, `{"target": {"kind": "application"}}`), "go://example.com/app#forgotten")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over a reported symbol = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("explain wrote %q to stderr, want nothing: the answer is on stdout", got.stderr)
	}
	for _, want := range []string{
		"symbol: go://example.com/app#forgotten",
		"declaration: function forgotten",
		"position: app.go:9:6",
		"answer: reported",
		"code: DS1002 unused-unexported",
		"message: unexported function has no reference in the target",
		"class: certain",
		"confidence: certain",
		"liveness relation: reference-counting",
		"configurations: " + load.HostConfiguration().ID,
		"loaded consumers: none",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
		}
	}
}

func TestExplainAnswersAboutALiveSymbolWithAPathFromARoot(t *testing.T) {
	got := runExplain(t, findingsFixture(t, `{"target": {"kind": "application"}}`), "go://example.com/app#used")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over a live symbol = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	for _, want := range []string{
		"answer: live",
		"live under: reference-counting reachability",
		// The path is the one reference the entry point makes, from the root the
		// entry point is.
		"reached from: root main app.go:3:6",
		"reference: app.go:3:15\tcall\tapp.go:3:6 -> app.go:6:6",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
		}
	}
	// A path of real references: every hop the answer prints is a reference of the
	// graph, so the number of hops is the number of reference lines.
	if got, want := strings.Count(got.stdout, "\n  reference: "), 1; got != want {
		t.Errorf("the answer prints %d hops, want %d: the entry point reaches the declaration directly", got, want)
	}
}

func TestExplainAnswersAboutARootWithTheRootKind(t *testing.T) {
	got := runExplain(t, findingsFixture(t, `{"target": {"kind": "application"}}`), "go://example.com/app#main")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over a root = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	for _, want := range []string{"answer: live", "root: main"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
		}
	}
	// A symbol that is itself a root is reached by the empty path, so no hop is
	// printed for it.
	if strings.Contains(got.stdout, "\n  reference: ") {
		t.Errorf("explain stdout =\n%s\nwant no reference hop: the symbol is the root the walk starts at", got.stdout)
	}
}

func TestExplainAnswersAboutARetainedSymbolWithEveryClassThatHeldIt(t *testing.T) {
	got := runExplain(t, retainedModule(t), "go://example.com/app#sink.Write")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over a retained symbol = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	for _, want := range []string{
		"answer: retained",
		"held back by: 1 exemption class",
		"class: interface-satisfaction",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
		}
	}
	// The class names the site the evidence was found at and the clause naming that
	// evidence, which is what a maintainer goes and looks at.
	for line := range strings.SplitSeq(got.stdout, "\n") {
		if !strings.HasPrefix(line, "  class: ") {
			continue
		}
		if fields := strings.Split(strings.TrimPrefix(line, "  class: "), "\t"); len(fields) != 3 ||
			fields[1] == "" || fields[2] == "" {
			t.Errorf("the class line is %q, want the class, the site and the clause naming the evidence", line)
		}
	}
}

// retainedModule is a module whose one unreferenced method satisfies a standard
// interface, so an exemption class holds it back rather than the run reporting it.
func retainedModule(t *testing.T) string {
	t.Helper()

	return writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nimport (\n\t\"io\"\n\t\"strings\"\n)\n\n" +
			"func main() {\n\tvar s sink\n\tif _, err := io.Copy(&s, strings.NewReader(\"x\")); err != nil {\n" +
			"\t\tpanic(err)\n\t}\n}\n\n" +
			"// sink counts the bytes written to it.\ntype sink struct{ n int }\n\n" +
			"// Write satisfies io.Writer and is called through that interface alone.\n" +
			"func (s *sink) Write(p []byte) (int, error) {\n\ts.n += len(p)\n\treturn len(p), nil\n}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
}

func TestExplainAnswersAboutADeadSymbolNoFindingNamesWithTheKindItsConfigurationSilences(t *testing.T) {
	got := runExplain(t, findingsFixture(t,
		`{"target": {"kind": "application"}, "severity": {"DS1002": "allow"}}`),
		"go://example.com/app#forgotten")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over a silenced symbol = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	// The symbol is a candidate of the sweep and no finding names it, because the
	// configuration silences the kind that would. Calling it live would be false,
	// so the answer says what it is.
	for _, want := range []string{"answer: dead", "candidate: dead under reference-counting"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
		}
	}
}

func TestExplainResolvesASymbolByItsFragmentAndByItsDisplayName(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	for _, named := range []string{"go://example.com/app#forgotten", "forgotten"} {
		got := runExplain(t, dir, named)
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Errorf("explain %q = %d, want %d\nstderr: %s", named, got.code, want, got.stderr)
		}
		if !strings.Contains(got.stdout, "symbol: go://example.com/app#forgotten") {
			t.Errorf("explain %q answered about\n%s\nwant the declaration the reference names", named, got.stdout)
		}
	}
}

func TestExplainRefusesASymbolTheTargetDoesNotHoldAndPrintsThePartialMatches(t *testing.T) {
	got := runExplain(t, findingsFixture(t, `{"target": {"kind": "application"}}`), "forgot")

	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("explain over a symbol the target does not hold = %d, want %d", got.code, want)
	}
	if got.stdout != "" {
		t.Errorf("explain wrote %q to stdout, want nothing: no symbol was explained", got.stdout)
	}
	for _, want := range []string{`"forgot" names no one symbol`, "go://example.com/app#forgotten\tapp.go:9:6"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("explain stderr =\n%s\nwant it to contain %q", got.stderr, want)
		}
	}
}

func TestExplainRefusesASymbolSeveralDeclarationsAnswerAndNamesThem(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nimport \"example.com/app/one\"\n\nfunc main() { one.Shared() }\n\n" +
			"// Shared shares a bare name with a declaration of another package.\nfunc Shared() {}\n",
		"one/one.go": "// Package one declares a symbol whose bare name another package also declares.\npackage one\n\n" +
			"// Shared is what the entry point calls.\nfunc Shared() {}\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	got := runExplain(t, dir, "Shared")

	if want := contractExitCodes(t)["usage"]; got.code != want {
		t.Errorf("explain over an ambiguous name = %d, want %d\nstdout: %s", got.code, want, got.stdout)
	}
	for _, want := range []string{"go://example.com/app#Shared", "go://example.com/app/one#Shared"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("explain stderr =\n%s\nwant it to name the candidate %q", got.stderr, want)
		}
	}
}

func TestExplainAnswersTheThreeSpellingsTheContractsTableGivesTheRequest(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	for _, spelling := range whyFlags {
		got := runExplain(t, dir, "--"+spelling+"=go://example.com/app#forgotten")
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Errorf("explain --%s = %d, want %d\nstderr: %s", spelling, got.code, want, got.stderr)
		}
		// The four spellings are one request: the answer is the state the symbol
		// is in, whichever spelling asked.
		if !strings.Contains(got.stdout, "answer: reported") {
			t.Errorf("explain --%s answered\n%s\nwant the state the symbol is in", spelling, got.stdout)
		}
	}
}

func TestExplainAnswersFromTheAnalysisTheReportIsBuiltFrom(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)

	// Every reported symbol of the run gets the reported answer, and the code the
	// answer names is the code the report names.
	report := reportOfDir(t, dir)
	for i := range report.Findings {
		found := &report.Findings[i]
		got := runExplain(t, dir, found.Symbol.Ref)
		if want := contractExitCodes(t)["clean"]; got.code != want {
			t.Fatalf("explain %s = %d, want %d\nstderr: %s", found.Symbol.Ref, got.code, want, got.stderr)
		}
		if !strings.Contains(got.stdout, "answer: reported") {
			t.Errorf("explain %s answered\n%s\nwant the reported answer: the report names it", found.Symbol.Ref, got.stdout)
		}
		if want := "code: " + found.Code + " " + found.Kind; !strings.Contains(got.stdout, want) {
			t.Errorf("explain %s answered\n%s\nwant it to name %q as the report does", found.Symbol.Ref, got.stdout, want)
		}
	}
}

func TestExplainIsTheSameAnswerOnEveryRunOverOneTree(t *testing.T) {
	dir := findingsFixture(t, `{"target": {"kind": "application"}}`)
	first := runExplain(t, dir, "go://example.com/app#used")
	second := runExplain(t, dir, "go://example.com/app#used")

	if first.stdout != second.stdout {
		t.Errorf("two explanations of one symbol differ:\n%s\n%s", first.stdout, second.stdout)
	}
}

func TestExplainListsTheDeadComponentAtTheConfiguredCascade(t *testing.T) {
	// The orphan is the component's root and the declaration it references falls
	// with it, so the component holds two members and the cascade decides whether
	// the answer names the one that is not the root.
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() {}\n\n" +
			"// orphan references a declaration and nothing references orphan.\n" +
			"func orphan() { fallen() }\n\n" +
			"// fallen is referenced by orphan alone.\nfunc fallen() {}\n",
	}

	for _, tc := range []struct {
		cascade  string
		wantList bool
	}{
		{cascade: "roots", wantList: false},
		{cascade: "full", wantList: true},
	} {
		t.Run(tc.cascade, func(t *testing.T) {
			files[repositoryDocument] = `{"target": {"kind": "application"}, ` +
				`"severity": {"DS1002": "allow"}, "reporters": {"cascade": "` + tc.cascade + `"}}`
			got := runExplain(t, writeModule(t, files), "go://example.com/app#fallen")

			if want := contractExitCodes(t)["clean"]; got.code != want {
				t.Fatalf("explain under cascade %s = %d, want %d\nstderr: %s", tc.cascade, got.code, want, got.stderr)
			}
			if want := "listed at cascade " + tc.cascade; !strings.Contains(got.stdout, want) {
				t.Errorf("explain stdout =\n%s\nwant it to contain %q", got.stdout, want)
			}
			if held := strings.Contains(got.stdout, "  component member: "); held != tc.wantList {
				t.Errorf("explain under cascade %s lists members %t, want %t:\n%s",
					tc.cascade, held, tc.wantList, got.stdout)
			}
		})
	}
}

func TestExplainPrefersTheProductionPathOverAShorterTestPath(t *testing.T) {
	// The test reaches the declaration in one hop from the test root; production
	// reaches it in three from the entry point. The production path is the one the
	// sweep the report was built from counts, so it is the path the answer prints.
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { a() }\n\n" +
			"// a calls b.\nfunc a() { b() }\n\n" +
			"// b calls shared.\nfunc b() { shared() }\n\n" +
			"// shared is reached from production through a chain and from a test directly.\n" +
			"func shared() {}\n",
		"app_test.go": "package main\n\nimport \"testing\"\n\n" +
			"func TestShared(t *testing.T) { shared() }\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	})
	got := runExplain(t, dir, "go://example.com/app#shared")

	if want := contractExitCodes(t)["clean"]; got.code != want {
		t.Fatalf("explain over the shared declaration = %d, want %d\nstderr: %s", got.code, want, got.stderr)
	}
	if !strings.Contains(got.stdout, "reached from: root main") {
		t.Errorf("explain stdout =\n%s\nwant the path to start at the entry point rather than at a test root", got.stdout)
	}
	if got, want := strings.Count(got.stdout, "\n  reference: "), 3; got != want {
		t.Errorf("the answer prints %d hops, want %d: the production chain is three references long", got, want)
	}
	if strings.Contains(got.stdout, "from a test file") {
		t.Errorf("explain stdout =\n%s\nwant no hop from a test file: a production path reaches the declaration", got.stdout)
	}
}

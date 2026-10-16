package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/render"
	"github.com/cplieger/deadset-spec/v8/report"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// analyzeUsage names every flag the verb takes: the invocation's own, which name
// where documents are written, what is rendered beside them and whether the exit
// code carries the verdict, then the flags that supply a setting and outrank both
// configuration sources for it.
const analyzeUsage = "usage: deadset-go analyze [--target=DIR] [--scope=FILE] [--config=FILE] [--central=FILE] " +
	"--report=FILE [--format=FORMAT ...] [--template=FILE] [--baseline-write=FILE] [--exit-code=on|off] " +
	"[--min-confidence=CLASS] [--sort=ORDER] [--cascade=DEPTH] [--max-findings=N] [--fail-on=SEVERITY]"

// The two values --exit-code takes: on, which is the default and puts the verdict of
// the run in the exit code, and off, which writes every document and exits clean.
const (
	exitCodeOn  = "on"
	exitCodeOff = "off"
)

// documentMode is the mode every document this verb writes carries.
const documentMode = 0o644

// renderings is the suffix each format's file takes, and a format absent from it is
// refused. The suffix is appended to the report path rather than replacing its
// extension: the path may carry any extension or none, and appending is the one rule
// under which no rendering lands on the report or on another rendering.
var renderings = map[vocab.Format]string{
	vocab.FormatText:     ".txt",
	vocab.FormatJSON:     ".json",
	vocab.FormatGitHub:   ".annotations",
	vocab.FormatSARIF:    ".sarif",
	vocab.FormatTemplate: ".tmpl",
}

// formatNames lists the formats this analyzer renders, for a message that has to
// say which they are.
func formatNames() string {
	named := make([]string, 0, len(renderings))
	for _, format := range slices.Sorted(maps.Keys(renderings)) {
		named = append(named, string(format))
	}
	return strings.Join(named, ", ")
}

// formatList is the value of the repeatable --format flag: one format per
// occurrence, in the order the command line named them.
type formatList []vocab.Format

// String renders the formats named so far, which is what the flag package prints as
// the value of the flag.
func (l *formatList) String() string {
	named := make([]string, len(*l))
	for i, format := range *l {
		named[i] = string(format)
	}
	return strings.Join(named, ",")
}

// Set records one format, refusing a name this analyzer renders nothing for and a
// repetition: a misspelling would otherwise be a rendering nobody notices is
// missing, and a repetition would write one file twice.
func (l *formatList) Set(value string) error {
	format := vocab.Format(value)
	if _, held := renderings[format]; !held {
		return fmt.Errorf("%q is not a format: the formats are %s", value, formatNames())
	}
	if slices.Contains(*l, format) {
		return fmt.Errorf("%q is named twice", value)
	}
	*l = append(*l, format)
	return nil
}

// invocation is what one analyze invocation asked for beyond the configuration: the
// path the report is written to, the renderings written beside it, the template one
// of those renderings reads, the path a baseline of the run is written to, and
// whether the exit code carries the verdict.
type invocation struct {
	template      *render.Template
	report        string
	baselineWrite string
	formats       formatList
	exitCodeOff   bool
}

// analyze writes the report of one run and returns the verdict the Contract's
// exit-code table gives it.
//
// Nothing is written to stdout. A report is potentially megabytes and a document a
// crash truncated is indistinguishable from a short one on a stream, so the report
// is a file, its write is atomic, every rendering is a file beside it, every
// diagnostic goes to stderr and the verdict is the exit code.
func analyze(ctx context.Context, args []string, stderr io.Writer) int {
	asked, req, code := analyzeFlags(ctx, args, stderr)
	if code != vocab.ExitClean {
		return code
	}
	self, err := selfOf()
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return vocab.ExitFailure
	}
	found, err := findingsOf(ctx, req)
	if err != nil {
		return failed(ctx, stderr, err)
	}
	built, err := found.Report(self)
	if err != nil {
		return failed(ctx, stderr, err)
	}
	namedUnbuilt(stderr, found.Unbuilt)
	for _, skip := range built.TypeErrorSkips {
		fmt.Fprintf(stderr, "deadset-go: %s:%d: %s: the function or statement holding this type error is not evaluated\n",
			skip.Path, skip.Line, skip.Message)
	}

	reporters := &req.Config.Reporters
	report.Cap(built, reporters.MaxFindings, reporters.BySize)

	var recorded []core.Recorded
	if asked.baselineWrite != "" {
		recorded, err = run.BaselineRows(ctx, found.Prepared, self)
		if err != nil {
			return failed(ctx, stderr, fmt.Errorf("write the baseline: %w", err))
		}
	}

	if err := asked.write(ctx, built, reporters, recorded, run.Provenance(self)); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return vocab.ExitFailure
	}
	return asked.verdict(built, reporters.FailOn, stderr)
}

// analyzeFlags parses the invocation and resolves the configuration documents it
// names.
func analyzeFlags(ctx context.Context, args []string, stderr io.Writer) (asked invocation, req *run.Request, code int) {
	flags := configuredFlagSet("analyze", analyzeUsage, stderr)
	flags.set.StringVar(&asked.report, "report", "", "the path the JSON report is written to")
	flags.set.Var(&asked.formats, "format", "one rendering written beside the report, repeatable, and one of "+formatNames())
	templatePath := flags.set.String("template", "", "the file holding the template the template rendering reads")
	flags.set.StringVar(&asked.baselineWrite, "baseline-write", "",
		"the path a baseline recording the findings of this target is written to")
	exitCode := flags.set.String("exit-code", exitCodeOn,
		"whether the exit code carries the verdict of the run: "+exitCodeOn+" or "+exitCodeOff)

	if err := flags.set.Parse(args); err != nil {
		return invocation{}, nil, vocab.ExitUsage
	}
	if flags.set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: analyze takes no argument, got %q\n", flags.set.Arg(0))
		flags.set.Usage()
		return invocation{}, nil, vocab.ExitUsage
	}
	if err := asked.request(*exitCode); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		flags.set.Usage()
		return invocation{}, nil, vocab.ExitUsage
	}

	req, code = flags.request(ctx, stderr)
	if code != vocab.ExitClean {
		return invocation{}, nil, code
	}
	// The renderings are the invocation's where it named any and the resolved
	// configuration's otherwise, which is the flag outranking the source for the
	// one request whose value is a list: a repeated flag has no spelling inside a
	// document the resolution reads.
	if len(asked.formats) == 0 {
		asked.formats = slices.Clone(req.Config.Reporters.Formats)
	}
	if err := asked.read(ctx, *templatePath); err != nil {
		return invocation{}, nil, flags.unread(ctx, stderr, err)
	}
	return asked, req, vocab.ExitClean
}

// request checks the half of the invocation no configuration source has a say in,
// which is what makes it answerable before any document is read: a malformed
// invocation costs no resolution and no load.
//
// A report path is required because the verb writes nothing to stdout.
func (a *invocation) request(exitCode string) error {
	if a.report == "" {
		return errors.New("analyze writes its report to the path --report names, and no path was named")
	}
	switch exitCode {
	case exitCodeOn:
		a.exitCodeOff = false
	case exitCodeOff:
		a.exitCodeOff = true
	default:
		return fmt.Errorf("--exit-code=%q: the values are %s and %s", exitCode, exitCodeOn, exitCodeOff)
	}
	return nil
}

// read completes the renderings the run writes and reads the template one of them
// needs. It runs after the resolution, because the renderings are the resolved
// configuration's where the invocation named none. A format this analyzer renders
// nothing for is refused wherever it came from, a template the invocation names is
// read and parsed whether or not a rendering reads it, and the template rendering
// without a template is refused, all before the analysis runs, so none costs a load.
func (a *invocation) read(ctx context.Context, templatePath string) error {
	for _, format := range a.formats {
		if _, held := renderings[format]; !held {
			return fmt.Errorf("reporters.formats names %q, which is not a format: the formats are %s",
				format, formatNames())
		}
	}
	if templatePath != "" {
		// A template is held to the bound of a configuration document.
		body, err := jsondoc.Read(ctx, templatePath, vocab.DocumentMaxBytes)
		if err != nil {
			return fmt.Errorf("read the template %s: %w", templatePath, err)
		}
		parsed, err := render.ParseTemplate(string(body))
		if err != nil {
			return fmt.Errorf("%s: %w", templatePath, err)
		}
		a.template = parsed
	}
	if slices.Contains(a.formats, vocab.FormatTemplate) && templatePath == "" {
		return errors.New("the template rendering reads the template --template names, and no path was named")
	}
	return nil
}

// write writes every document one invocation asked for: the report at the path it
// named, one rendering per format beside it, and the baseline where it named a path
// for one. The report is held to the report schema before anything is written.
func (a *invocation) write(ctx context.Context, r *report.Report, reporters *config.Reporters, recorded []core.Recorded, p core.Provenance) error {
	if err := publish(ctx, a.report, func(w io.Writer) error { return report.Encode(w, r) }); err != nil {
		return err
	}

	// A position of the report is target-relative, so the SARIF rendering's
	// reader resolves it against the root the report names.
	targetRoot := filepath.FromSlash(r.Target.Root)
	opts := &render.Options{
		Read:     func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(targetRoot, path)) },
		Template: a.template,
		BySize:   reporters.BySize,
	}
	for _, format := range a.formats {
		err := publish(ctx, a.report+renderings[format], func(w io.Writer) error {
			if err := render.Render(w, format, r, opts); err != nil || format != vocab.FormatText {
				return err
			}
			return render.Summary(w, r)
		})
		if err != nil {
			return err
		}
	}

	if a.baselineWrite == "" {
		return nil
	}
	return writeBaseline(ctx, a.baselineWrite, recorded, p)
}

// writeBaseline writes the baseline document at path: the rows this write recorded
// and every row of another scheme the document there holds. A document it cannot
// read, a row of either scheme included, is refused before anything is written, so
// the document stays as it was.
func writeBaseline(ctx context.Context, path string, recorded []core.Recorded, p core.Provenance) error {
	held, err := suppress.BaselineRows(path)
	if err != nil {
		return err
	}
	return publish(ctx, path, func(w io.Writer) error {
		return core.WriteBaseline(w, recorded, p, core.OtherRows(held, suppress.OwnScheme))
	})
}

// verdict names on stderr what the report holds that a reader acts on, and returns
// the code the Contract's table gives the report.
//
// The two counts are named whether or not the exit code carries the verdict, because
// a run whose code is configured off is a run whose only account of those records is
// the message. The code the verdict would have been is named beside the clean code
// for the same reason.
func (a *invocation) verdict(r *report.Report, failOn vocab.Severity, stderr io.Writer) int {
	if r.Totals.Pending > 0 {
		fmt.Fprintf(stderr, "deadset-go: %s: the report names a declared cross-language edge, and no merge has resolved it\n",
			counted(r.Totals.Pending, "pending finding"))
	}
	if r.Totals.StaleSuppressions > 0 {
		fmt.Fprintf(stderr, "deadset-go: %s\n", counted(r.Totals.StaleSuppressions, "stale suppression"))
	}
	answer := render.Verdict(&r.Totals, failOn)
	if !a.exitCodeOff {
		return answer
	}
	if answer != vocab.ExitClean {
		fmt.Fprintf(stderr, "deadset-go: the exit code is configured off: the verdict of this run is %d\n", answer)
	}
	return vocab.ExitClean
}

// publish renders one document whole and publishes it at path atomically: a
// reader of the path sees the document of the previous run or this one, never a
// truncated one, which a consumer could not tell from a malformed document.
func publish(ctx context.Context, path string, write func(w io.Writer) error) error {
	var rendered bytes.Buffer
	if err := write(&rendered); err != nil {
		return fmt.Errorf("render %s: %w", path, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	if _, err := atomicfile.WriteFile(ctx, absolute, rendered.Bytes(), atomicfile.WithMode(documentMode)); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

// counted renders a count with its noun, so a message reads for one record as well
// as for several. A noun ending in s takes es.
func counted(n int, noun string) string {
	switch {
	case n == 1:
		return strconv.Itoa(n) + " " + noun
	case strings.HasSuffix(noun, "s"):
		return strconv.Itoa(n) + " " + noun + "es"
	default:
		return strconv.Itoa(n) + " " + noun + "s"
	}
}

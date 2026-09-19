package main

import (
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

	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/kinds"
	"github.com/cplieger/deadset-go/internal/report"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// analyzeUsage names every flag the verb takes, in two groups: the flags that
// supply a setting of the resolved configuration, and the flags that are the
// invocation's own.
//
// A setting flag outranks both configuration sources for the setting it supplies,
// and its value reaches every stage that reads the setting. An invocation flag
// supplies no setting: it names where a document is written, what is rendered beside
// it and whether the exit code carries the verdict, and none of the three is
// something a repository commits. --scope is neither: it selects the scope document
// the run reads rather than a value inside one.
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

// rendering is one rendering of a report: the reporter that writes it, and the
// suffix the file it is written to carries.
type rendering struct {
	write  func(w io.Writer, e *report.Envelope, opts report.Options) error
	suffix string
}

// renderings is every format this analyzer renders, each mapped to the reporter that
// renders it and the suffix its file carries. A format absent from it is refused
// rather than silently not written, so a configuration naming one is never a file a
// maintainer goes looking for.
//
// A rendering is written at the report path with its suffix APPENDED, rather than
// with the report's extension replaced: the report path is the invocation's and may
// carry any extension or none, so appending is the one rule under which no rendering
// can land on the report itself or on another rendering.
var renderings = map[config.Format]rendering{
	config.Text:     {write: report.Text, suffix: ".txt"},
	config.JSON:     {write: report.JSON, suffix: ".json"},
	config.GitHub:   {write: report.Annotations, suffix: ".annotations"},
	config.SARIF:    {write: report.SARIF, suffix: ".sarif"},
	config.Template: {write: report.Template, suffix: ".tmpl"},
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
type formatList []config.Format

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
	format := config.Format(value)
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
	report        string
	template      string
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
	asked, resolved, code := analyzeFlags(args, stderr)
	if code != exitClean {
		return code
	}

	options, err := exemptOptions(&resolved.config)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitUsage
	}
	answered, err := corpusAnswer()
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitFailure
	}
	envelope, err := reportOf(ctx, &resolved, &options, answered)
	if err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitCodeFor(err)
	}

	report.Sort(&envelope, resolved.config.Reporters.Sort)
	// The baseline records every finding of the run, which is why the rows are
	// taken before the maximum finding count bounds what a rendering prints: a
	// baseline missing a finding this run reported would fail the next run on it.
	recorded := recordedFindings(envelope.Findings)
	report.Cap(&envelope, resolved.config.Reporters.MaxFindings)

	if err := asked.write(&envelope, recorded, resolved.target); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		return exitFailure
	}
	return asked.verdict(&envelope, &resolved.config, stderr)
}

// analyzeFlags parses the invocation and resolves the configuration documents it
// names.
func analyzeFlags(args []string, stderr io.Writer) (asked invocation, resolved resolution, code int) {
	flags := configuredFlagSet("analyze", analyzeUsage, stderr, true)
	flags.set.StringVar(&asked.report, "report", "", "the path the JSON report is written to")
	flags.set.Var(&asked.formats, "format", "one rendering written beside the report, repeatable; one of "+formatNames())
	template := flags.set.String("template", "", "the file holding the template the template rendering reads")
	flags.set.StringVar(&asked.baselineWrite, "baseline-write", "",
		"the path a baseline recording every finding of this run is written to")
	exitCode := flags.set.String("exit-code", exitCodeOn,
		"whether the exit code carries the verdict of the run: "+exitCodeOn+" or "+exitCodeOff)

	if err := flags.set.Parse(args); err != nil {
		return invocation{}, resolution{}, exitUsage
	}
	if flags.set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: analyze takes no argument, got %q\n", flags.set.Arg(0))
		flags.set.Usage()
		return invocation{}, resolution{}, exitUsage
	}
	if err := asked.request(*exitCode); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		flags.set.Usage()
		return invocation{}, resolution{}, exitUsage
	}

	resolved, code = flags.resolution(stderr)
	if code != exitClean {
		return invocation{}, resolution{}, code
	}
	// The renderings are the invocation's where it named any and the resolved
	// configuration's otherwise, which is the flag outranking the source for the
	// one request whose value is a list: a repeated flag has no spelling inside a
	// document the resolution reads.
	if len(asked.formats) == 0 {
		asked.formats = slices.Clone(resolved.config.Reporters.Formats)
	}
	if err := asked.read(*template); err != nil {
		fmt.Fprintf(stderr, "deadset-go: %v\n", err)
		flags.set.Usage()
		return invocation{}, resolution{}, exitUsage
	}
	return asked, resolved, exitClean
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
// needs.
//
// It runs after the resolution, because the renderings are the resolved
// configuration's where the invocation named none. A format this analyzer renders
// nothing for is refused wherever it came from, and the template rendering without a
// template is refused before the analysis runs rather than after it, so neither
// costs a load.
func (a *invocation) read(templatePath string) error {
	for _, format := range a.formats {
		if _, held := renderings[format]; !held {
			return fmt.Errorf("reporters.formats names %q, which is not a format: the formats are %s",
				format, formatNames())
		}
	}
	if templatePath != "" {
		body, err := readDocument(templatePath, false)
		if err != nil {
			return err
		}
		a.template = string(body)
	}
	if slices.Contains(a.formats, config.Template) && templatePath == "" {
		return errors.New("the template rendering reads the template --template names, and no path was named")
	}
	return nil
}

// write writes every document one invocation asked for: the report at the path it
// named, one rendering per format beside it, and the baseline where it named a path
// for one.
func (a *invocation) write(e *report.Envelope, recorded []suppress.Recorded, targetRoot string) error {
	if err := writeAtomically(a.report, func(w io.Writer) error { return report.JSON(w, e, report.Options{}) }); err != nil {
		return err
	}

	// The SARIF rendering hashes the source line each of its results names, and a
	// position of the report is target-relative, so the reader it is given is one
	// that resolves a position against the target root.
	options := report.Options{
		Read:     func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(targetRoot, path)) },
		Template: a.template,
	}
	for _, format := range a.formats {
		one := renderings[format]
		if err := writeAtomically(a.report+one.suffix, func(w io.Writer) error { return one.write(w, e, options) }); err != nil {
			return err
		}
	}

	if a.baselineWrite == "" {
		return nil
	}
	return writeAtomically(a.baselineWrite, func(w io.Writer) error {
		return suppress.WriteBaseline(w, recorded, suppress.Provenance{Analyzer: name, Version: version()})
	})
}

// verdict names on stderr what the report holds that a reader acts on, and returns
// the code the Contract's table gives the report.
//
// The two counts are named whether or not the exit code carries the verdict, because
// a run whose code is configured off is a run whose only account of those records is
// the message. The code the verdict would have been is named beside the clean code
// for the same reason.
func (a *invocation) verdict(e *report.Envelope, cfg *config.Config, stderr io.Writer) int {
	if e.Totals.Pending > 0 {
		fmt.Fprintf(stderr, "deadset-go: %s: the report names a declared cross-language edge, and no merge has resolved it\n",
			counted(e.Totals.Pending, "pending finding"))
	}
	if e.Totals.StaleSuppressions > 0 {
		fmt.Fprintf(stderr, "deadset-go: %s\n", counted(e.Totals.StaleSuppressions, "stale suppression"))
	}
	code := report.ExitCode(e, cfg, a.exitCodeOff)
	if verdict := report.ExitCode(e, cfg, false); verdict != code {
		fmt.Fprintf(stderr, "deadset-go: the exit code is configured off: the verdict of this run is %d\n", verdict)
	}
	return code
}

// writeAtomically writes one document at path through a temporary file in the same
// directory, flushed to the filesystem and renamed into place.
//
// A run that dies partway through the write leaves no truncated document at the
// path: a reader of the path sees the document of the previous run or none at all,
// and the rename is what publishes this one. A report is potentially megabytes, and
// a truncated JSON document is exactly what a consumer cannot tell from a malformed
// one, which is why every document this verb writes goes through here.
func writeAtomically(path string, write func(w io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Every arm below removes the temporary file, and the one that runs after the
	// rename consumed it is the one whose failure means nothing.
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	if err := write(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("render %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chmod(temporary, documentMode); err != nil {
		return fmt.Errorf("set the mode of %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

// recordedFindings is every finding of the report as a baseline row records it, in
// the order the report lists them.
func recordedFindings(findings []kinds.Finding) []suppress.Recorded {
	rows := make([]suppress.Recorded, len(findings))
	for i := range findings {
		rows[i] = suppress.Recorded{
			Code:   findings[i].Code,
			Symbol: findings[i].Symbol.Ref,
			Path:   findings[i].Position.Path,
		}
	}
	return rows
}

// counted renders a count with its noun, so a message reads for one record as well
// as for several.
func counted(n int, noun string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

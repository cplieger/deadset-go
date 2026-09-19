package suppress

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/internal/graph"
)

// BaselineFileName is the name the grammar fixes for the baseline. The location is
// fixed too, the target root, so the path a finding carries for the document is
// this name.
const BaselineFileName = "deadset-baseline.json"

// baselineDescription is what a written document says about itself, so a
// maintainer who opens one learns what its rows are before reading them. It is a
// constant, because a document written from one report is the same bytes every
// time.
const baselineDescription = "Recorded findings: a run fails only on a finding this document does not hold. " +
	"Every reason is the analyzer that recorded the row, never an adjudication."

// reasonPrefix opens every reason a written row carries, and the rest of the
// reason is the analyzer's own name and version.
const reasonPrefix = "recorded by "

// ErrProvenance reports a request to write a baseline whose rows would carry no
// reason: a row with no code, symbol or path, or an analyzer identity with no name
// or no version. The grammar requires a reason on every row, so a document that
// could not carry one is refused rather than written.
var ErrProvenance = errors.New("suppress: a baseline row would carry no reason")

// baselineShape is the baseline's document, which shares the ignore file's record
// shape so that one decoder and one matcher serve both and a row can be moved into
// the ignore file by adding a maintainer's reason.
func baselineShape() *shape {
	return &shape{
		file:       BaselineFileName,
		array:      "baseline",
		arrayWant:  "an object carrying the baseline array, and an optional description",
		recordWant: "a row naming code, symbol, path and reason, each a string",
		mechanism:  MechanismBaseline,
	}
}

// wireBaseline is the closed key list of the baseline document. The description
// carries what a file header comment would have and the analysis does not read it;
// the baseline array is required and may be empty.
type wireBaseline struct {
	Baseline    *[]json.RawMessage `json:"baseline"`
	Description string             `json:"description"`
}

// Baseline reads the baseline at path and binds each of its rows to the
// declaration it names.
//
// A row is matched exactly as an ignore entry is, on the code, the symbol and the
// path together, and the same two refusals apply to a row with no reason and to one
// with no path. What differs is what the reason means: an analyzer wrote it, so it
// is the provenance of the row and never a maintainer's judgement, and a reader
// must not take a row as an adjudication. The finding a row records is real, and
// the row says only that it was already there when the baseline was written.
//
// An absent file is an empty one, and every defect the ignore file's decoder
// refuses is refused here in the same way.
func Baseline(path string, symbols []graph.Symbol) ([]Record, []Refusal, error) {
	held := baselineShape()
	body, sites, err := document(path, held)
	if body == nil || err != nil {
		return nil, nil, err
	}

	var wire wireBaseline
	if err := decodeDocument(body, &wire, held); err != nil {
		return nil, nil, err
	}
	if wire.Baseline == nil {
		return nil, nil, &MalformedError{Site: documentSite(held), Text: held.file, Want: held.arrayWant, Mechanism: held.mechanism}
	}

	return records(*wire.Baseline, sites, declarationRefs(symbols), held)
}

// Recorded is one finding a baseline row records: the code the finding carries,
// the stable symbol reference of its subject, and the path of the file the subject
// is in. It is what a written row is built from, and the three values a later run
// matches the row on.
type Recorded struct {
	Code   string
	Symbol string
	Path   string
}

// Provenance is the analyzer identity a written row carries as its reason: the name
// and version the same run's report carries. The two together identify the run that
// recorded the row, and nothing that varies between two runs over one tree is
// written, so a document written from one report is the same bytes every time.
type Provenance struct {
	Analyzer string
	Version  string
}

// reason is what a row this provenance writes carries.
func (p Provenance) reason() string { return reasonPrefix + p.Analyzer + " " + p.Version }

// WriteBaseline writes a baseline document to w holding one row per finding, in the
// order given, which is the order the report lists them in.
//
// The document adjudicates nothing. Every row carries the provenance as its
// reason, so a row is never read as a maintainer's judgement, and a later run that
// reads the document back suppresses exactly the findings recorded here and reports
// no stale row.
//
// The caller passes the findings of its report and nothing else: no row for a stale
// suppression, no row for a pending finding, which lives in an edge evaluation and
// is neither reported nor suppressed, and no row for a finding a directive or an
// entry already suppressed. A finding missing a value a row needs, and an identity
// missing its name or version, are [ErrProvenance]: the grammar requires a reason
// on every row, and a document that could not carry one is refused rather than
// written.
func WriteBaseline(w io.Writer, findings []Recorded, p Provenance) error {
	if p.Analyzer == "" || p.Version == "" {
		return fmt.Errorf("%w: the analyzer identity names %q at version %q", ErrProvenance, p.Analyzer, p.Version)
	}
	rows := make([]wireRow, 0, len(findings))
	for i := range findings {
		found := &findings[i]
		if found.Code == "" || found.Symbol == "" || found.Path == "" {
			return fmt.Errorf("%w: a finding names code %q, symbol %q and path %q",
				ErrProvenance, found.Code, found.Symbol, found.Path)
		}
		rows = append(rows, wireRow{
			Code:   found.Code,
			Symbol: found.Symbol,
			Path:   found.Path,
			Reason: p.reason(),
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// A reference carries the characters a JSON encoder escapes for HTML, so the
	// escaping is off: the document is read by this analyzer and by a maintainer,
	// and a reference is compared as the bytes the grammar spells.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wireWritten{Description: baselineDescription, Baseline: rows}); err != nil {
		return fmt.Errorf("suppress: write %s: %w", BaselineFileName, err)
	}
	return nil
}

// wireWritten is the document WriteBaseline produces, whose field order is the
// member order the written bytes carry.
type wireWritten struct {
	Description string    `json:"description"`
	Baseline    []wireRow `json:"baseline"`
}

// wireRow is one written row, whose field order is the member order of an entry of
// the ignore file, so the two documents read the same.
type wireRow struct {
	Code   string `json:"code"`
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

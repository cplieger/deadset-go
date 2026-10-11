package suppress

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"strings"

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

// errProvenance reports a request to write a baseline whose rows would carry no
// reason: a row with no code, symbol or path, or an analyzer identity with no name
// or no version. The grammar requires a reason on every row, so a document that
// could not carry one is refused rather than written.
var errProvenance = errors.New("suppress: a baseline row would carry no reason")

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
	return baselineRecords(body, sites, symbols, held)
}

// BaselineDocument reads one baseline document held in memory, as [Baseline] reads
// the one at the target root: the rows a baseline write has recorded so far, read
// back at the positions the document it writes gives them.
func BaselineDocument(body []byte, symbols []graph.Symbol) ([]Record, []Refusal, error) {
	held := baselineShape()
	sites, err := recordSites(body, held)
	if err != nil {
		return nil, nil, err
	}
	return baselineRecords(body, sites, symbols, held)
}

// baselineRecords decodes one baseline document and binds each of its rows.
func baselineRecords(body []byte, sites []token.Position, symbols []graph.Symbol, held *shape) ([]Record, []Refusal, error) {
	var wire wireBaseline
	if err := decodeDocument(body, &wire, held); err != nil {
		return nil, nil, err
	}
	if wire.Baseline == nil {
		return nil, nil, &malformedError{Site: documentSite(held), Text: held.file, Want: held.arrayWant, Mechanism: held.mechanism}
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

// Row is one row of a baseline document as written, reason included. A member the
// document leaves out is nil, so a write keeps it left out.
//
//nolint:govet // fieldalignment: a Row converts to a wireRow, whose field order is the written member order
type Row struct {
	Code   string
	Symbol string
	Path   *string
	Reason *string
}

// OtherSchemeRows is every row of the baseline document at path whose reference is
// of another language's scheme, in document order, which a write keeps as the
// document held them. An absent document holds none, and one that does not decode is
// [malformedError].
func OtherSchemeRows(path string) ([]Row, error) {
	held := baselineShape()
	body, _, err := document(path, held)
	if body == nil || err != nil {
		return nil, err
	}
	var wire wireBaseline
	if err := decodeDocument(body, &wire, held); err != nil {
		return nil, err
	}
	if wire.Baseline == nil {
		return nil, &malformedError{Site: documentSite(held), Text: held.file, Want: held.arrayWant, Mechanism: held.mechanism}
	}
	var kept []Row
	for _, raw := range *wire.Baseline {
		one, err := decodeRecord(raw, documentSite(held), held)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(*one.Symbol, ownScheme) {
			kept = append(kept, Row{Code: *one.Code, Symbol: *one.Symbol, Path: one.Path, Reason: one.Reason})
		}
	}
	return kept, nil
}

// WriteBaseline writes a baseline document to w holding one row per recorded
// finding, in the order given, each carrying the provenance as its reason so no row
// reads as a maintainer's judgement, then every row of another language's scheme the
// document keeps, so the go:// rows come first whichever analyzer writes last. A
// finding missing a value a row needs, and an identity missing its name or version,
// are [errProvenance].
func WriteBaseline(w io.Writer, findings []Recorded, p Provenance, others []Row) error {
	if p.Analyzer == "" || p.Version == "" {
		return fmt.Errorf("%w: the analyzer identity names %q at version %q", errProvenance, p.Analyzer, p.Version)
	}
	reason := p.reason()
	rows := make([]wireRow, 0, len(findings)+len(others))
	for i := range findings {
		found := &findings[i]
		if found.Code == "" || found.Symbol == "" || found.Path == "" {
			return fmt.Errorf("%w: a finding names code %q, symbol %q and path %q",
				errProvenance, found.Code, found.Symbol, found.Path)
		}
		rows = append(rows, wireRow{
			Code:   found.Code,
			Symbol: found.Symbol,
			Path:   &found.Path,
			Reason: &reason,
		})
	}
	for _, other := range others {
		rows = append(rows, wireRow(other))
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
//
//nolint:govet // fieldalignment: encoding/json writes fields in declaration order, which is the member order
type wireRow struct {
	Code   string  `json:"code"`
	Symbol string  `json:"symbol"`
	Path   *string `json:"path,omitempty"`
	Reason *string `json:"reason,omitempty"`
}

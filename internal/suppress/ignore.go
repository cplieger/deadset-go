package suppress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"io/fs"
	"os"
	"strconv"
	"unicode/utf16"

	"github.com/cplieger/deadset-go/internal/graph"
)

// IgnoreFileName is the name the grammar fixes for the ignore file. The location
// is fixed too, the target root, so the path a finding carries for the document
// is this name and nothing has to be made relative.
const IgnoreFileName = "deadset-ignore.json"

// maxDocumentBytes bounds the ignore file. One entry is one adjudication a
// maintainer wrote, so a document orders of magnitude past that is not one to
// decode.
const maxDocumentBytes = 1 << 20

// The forms a malformed value of an entry is told to take.
const (
	codeWant   = "DS followed by four digits"
	pathWant   = "a path relative to the target root, with the solidus as separator"
	symbolWant = "a stable symbol reference, go://<import-path>#<fragment> or ts://<package>/<source-path>#<fragment>"
	entryWant  = "an entry naming code, symbol, path and reason, each a string"
	arrayWant  = "an object carrying the ignore array, and an optional description"
)

// ErrTooLarge reports a document above the size bound.
var ErrTooLarge = errors.New("suppress: document too large")

// wireEntry is the closed key list of one entry. Every key the grammar declares
// appears here, so an undeclared key is a decode error, and each is a pointer so
// that an absent key is told from an empty value: the code and the symbol are
// required by the decoder, because without them the entry can name nothing, and
// the path and the reason are required by a finding instead, each with its own
// rule.
type wireEntry struct {
	Code   *string `json:"code"`
	Symbol *string `json:"symbol"`
	Path   *string `json:"path"`
	Reason *string `json:"reason"`
}

// wireDocument is the closed key list of the document. The description carries
// what a file header comment would have and the analysis does not read it; the
// ignore array is required and may be empty.
type wireDocument struct {
	Ignore      *[]json.RawMessage `json:"ignore"`
	Description string             `json:"description"`
}

// IgnoreFile reads the ignore file at path and binds each of its entries to the
// declaration it names.
//
// An entry names a code, a stable symbol reference, a path and a reason, and it
// binds to the declaration whose reference and file both equal the ones it names:
// never to a symbol of the same name in another file or another package, and
// never by glob, pattern or substring, so an adjudication written for one symbol
// masks nothing else. An entry whose reason is absent, empty or whitespace, and
// one naming a symbol and no path, are refusals that bind nothing. An absent file
// is an empty one.
//
// The document is strict JSON with a closed key list at both levels: an
// undeclared key, a value of the wrong type, a member written twice at any depth,
// a missing code or symbol and a value outside its published form are each
// [MalformedError], and no record or refusal comes back with one.
func IgnoreFile(path string, symbols []graph.Symbol) ([]Record, []Refusal, error) {
	body, err := readBounded(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	}

	sites, err := entrySites(body)
	if err != nil {
		return nil, nil, err
	}
	var wire wireDocument
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, nil, &MalformedError{Err: err, Site: documentSite(), Text: IgnoreFileName, Want: arrayWant, Mechanism: MechanismIgnore}
	}
	if wire.Ignore == nil {
		return nil, nil, &MalformedError{Site: documentSite(), Text: IgnoreFileName, Want: arrayWant, Mechanism: MechanismIgnore}
	}

	return entries(*wire.Ignore, sites, declarationRefs(symbols))
}

// entries reads every entry of the document in document order, each at the site
// the token walk recorded for it.
func entries(raw []json.RawMessage, sites []token.Position, bound map[reference][]graph.Symbol) ([]Record, []Refusal, error) {
	var records []Record
	var refusals []Refusal
	for i, body := range raw {
		at := documentSite()
		if i < len(sites) {
			at = sites[i]
		}
		held, refused, err := entry(body, at, bound)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, held...)
		refusals = append(refusals, refused...)
	}
	return records, refusals, nil
}

// entry reads one entry: the records it bound, or the refusals it carries, or the
// malformed value that ends the run.
func entry(body []byte, at token.Position, bound map[reference][]graph.Symbol) ([]Record, []Refusal, error) {
	wire, err := decodeEntry(body, at)
	if err != nil {
		return nil, nil, err
	}
	code, symbol := *wire.Code, *wire.Symbol
	path, reason := value(wire.Path), value(wire.Reason)

	if refused := refusals(wire, at); len(refused) > 0 {
		return nil, refused, nil
	}
	record := Record{
		Code:      code,
		Symbol:    symbol,
		Path:      path,
		Reason:    reason,
		Site:      at,
		Mechanism: MechanismIgnore,
	}
	held := bound[reference{ref: symbol, path: path}]
	if len(held) == 0 {
		return []Record{record}, nil, nil
	}
	records := make([]Record, 0, len(held))
	for i := range held {
		record.Bound = held[i].ID
		records = append(records, record)
	}
	return records, nil, nil
}

// decodeEntry decodes one entry and refuses every defect that ends the run: an
// undeclared key, a value of the wrong type, an absent code or symbol, and a
// value outside its published form.
func decodeEntry(body []byte, at token.Position) (*wireEntry, error) {
	refuse := func(err error, text, want string) (*wireEntry, error) {
		return nil, &MalformedError{Err: err, Site: at, Text: text, Want: want, Mechanism: MechanismIgnore}
	}

	var wire wireEntry
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return refuse(err, string(body), entryWant)
	}
	switch {
	case wire.Code == nil:
		return refuse(nil, "an entry naming no code", entryWant)
	case wire.Symbol == nil:
		return refuse(nil, "an entry naming no symbol", entryWant)
	case !codeForm.MatchString(*wire.Code):
		return refuse(nil, strconv.Quote(*wire.Code), codeWant)
	case !referenceForm(*wire.Symbol):
		return refuse(nil, strconv.Quote(*wire.Symbol), symbolWant)
	case value(wire.Path) != "" && !pathForm.MatchString(*wire.Path):
		return refuse(nil, strconv.Quote(*wire.Path), pathWant)
	}
	return &wire, nil
}

// refusals returns every refusal one well-formed entry carries. An entry lacking
// both its reason and its path carries both, because each rule reports the field
// it is about and neither outranks the other.
func refusals(wire *wireEntry, at token.Position) []Refusal {
	refusal := Refusal{
		Code:      *wire.Code,
		Symbol:    *wire.Symbol,
		Path:      value(wire.Path),
		Reason:    value(wire.Reason),
		Site:      at,
		Mechanism: MechanismIgnore,
	}
	var refused []Refusal
	if blank(wire.Reason) {
		refusal.Reported = codeNoReason
		refused = append(refused, refusal)
	}
	if value(wire.Path) == "" {
		refusal.Reported = codeUnscoped
		refused = append(refused, refusal)
	}
	return refused
}

// reference names one declaration the way an entry does: its stable symbol
// reference and the file that holds it, together, because a same-named symbol in
// another file or another package is another symbol.
type reference struct {
	ref  string
	path string
}

// declarationRefs indexes the inventory by reference and file. A blank
// declaration carries its container's reference rather than one of its own, so a
// file may hold two declarations under one reference and an entry naming it binds
// both.
func declarationRefs(symbols []graph.Symbol) map[reference][]graph.Symbol {
	byReference := make(map[reference][]graph.Symbol, len(symbols))
	for i := range symbols {
		at := reference{ref: symbols[i].Ref, path: symbols[i].Pos.Filename}
		byReference[at] = append(byReference[at], symbols[i])
	}
	return byReference
}

// value is the string a key holds, and the empty string where the key is absent.
func value(held *string) string {
	if held == nil {
		return ""
	}
	return *held
}

// blank reports whether a reason is absent, empty or whitespace only, which the
// grammar reads as no reason at all.
func blank(reason *string) bool {
	if reason == nil {
		return true
	}
	for _, r := range *reason {
		if r != ' ' && r != '\t' && r != '\r' && r != '\n' {
			return false
		}
	}
	return true
}

// documentSite names the document itself, for a defect no entry position
// describes.
func documentSite() token.Position { return token.Position{Filename: IgnoreFileName} }

// readBounded returns the document's bytes, refusing one above the size bound
// without reading it whole.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("suppress: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// One byte past the bound tells a document at the limit from one over it.
	body, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("suppress: read %s: %w", path, err)
	}
	if len(body) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, path, maxDocumentBytes)
	}
	return body, nil
}

// role is what one value of the document is, which is what decides whether the
// brace opening it is an entry's position.
type role uint8

const (
	roleDocument role = iota // the one value the document holds
	roleArray                // the array the ignore member holds
	roleEntry                // one entry of that array
	roleOther                // every value below an entry, and the description
)

// walk reads the document as tokens, which is what a decode into the closed key
// list cannot do: a member one object writes twice, at any depth, is a document
// whose later value silently replaces the earlier one, so it reads as though the
// member were written once. The walk also records where each entry's opening
// brace is, which is the position every finding about that entry carries.
type walk struct {
	body    []byte
	entries []token.Position
}

// entrySites returns the position of the brace that opens each entry, refusing a
// document that writes one member twice or carries anything after its one value.
func entrySites(body []byte) ([]token.Position, error) {
	w := &walk{body: body}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := w.value(dec, roleDocument); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, &MalformedError{Site: documentSite(), Text: "a second value after the document", Want: arrayWant, Mechanism: MechanismIgnore}
	}
	return w.entries, nil
}

// value reads the one value at the decoder's position, and its descendants.
func (w *walk) value(dec *json.Decoder, at role) error {
	tok, err := dec.Token()
	if err != nil {
		return w.refuse(dec, err)
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		if at == roleEntry {
			w.entries = append(w.entries, w.at(dec.InputOffset()-1))
		}
		return w.object(dec, at)
	case '[':
		return w.array(dec, at)
	default:
		return w.refuse(dec, fmt.Errorf("unexpected %q", delim))
	}
}

// object reads the members of the object the decoder has just opened, refusing
// one the object writes twice.
func (w *walk) object(dec *json.Decoder, at role) error {
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return w.refuse(dec, err)
		}
		name, isName := tok.(string)
		if !isName {
			return w.refuse(dec, fmt.Errorf("want a member name, got %v", tok))
		}
		if seen[name] {
			return &MalformedError{
				Site:      w.at(dec.InputOffset()),
				Text:      fmt.Sprintf("the member %s is written twice, so neither value is chosen", strconv.Quote(name)),
				Want:      arrayWant,
				Mechanism: MechanismIgnore,
			}
		}
		seen[name] = true
		if err := w.value(dec, member(at, name)); err != nil {
			return err
		}
	}
	return w.close(dec)
}

// array reads the entries of the array the decoder has just opened.
func (w *walk) array(dec *json.Decoder, at role) error {
	held := roleOther
	if at == roleArray {
		held = roleEntry
	}
	for dec.More() {
		if err := w.value(dec, held); err != nil {
			return err
		}
	}
	return w.close(dec)
}

// close reads the delimiter that ends the object or array just walked.
func (w *walk) close(dec *json.Decoder) error {
	if _, err := dec.Token(); err != nil {
		return w.refuse(dec, err)
	}
	return nil
}

// member is what the value one member holds is: the array of entries for the
// document's own ignore member, and nothing of interest below that.
func member(at role, name string) role {
	if at == roleDocument && name == "ignore" {
		return roleArray
	}
	return roleOther
}

// refuse reports a document the token walk could not read, at the position it
// stopped at.
func (w *walk) refuse(dec *json.Decoder, err error) error {
	return &MalformedError{
		Err:       err,
		Site:      w.at(dec.InputOffset()),
		Text:      IgnoreFileName,
		Want:      arrayWant,
		Mechanism: MechanismIgnore,
	}
}

// at renders one byte offset of the document as the position a finding carries:
// the line, counted from one, and the column, counting UTF-16 code units from
// one.
func (w *walk) at(offset int64) token.Position {
	if offset < 0 || offset > int64(len(w.body)) {
		return documentSite()
	}
	before := w.body[:offset]
	start := bytes.LastIndexByte(before, '\n') + 1
	column := 1
	for _, r := range string(before[start:]) {
		if units := utf16.RuneLen(r); units > 0 {
			column += units
			continue
		}
		column++
	}
	return token.Position{
		Filename: IgnoreFileName,
		Offset:   int(offset),
		Line:     1 + bytes.Count(before, []byte("\n")),
		Column:   column,
	}
}

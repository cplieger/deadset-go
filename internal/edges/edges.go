// Package edges reads the declared cross-language edges of one target: the pairs
// that say one symbol exists because a symbol in another language uses it.
//
// An edge has two sides, and an analyzer reads only the sides that carry its own
// language. It never resolves the paired symbol, because it holds no type
// information for the other language and acquires none, so the document this
// package returns is the declaration and nothing more: what an analyzer makes of
// its own side is the findings pass's answer, and what the pair means is the
// merge's.
//
// Nothing here reports a finding. A document the grammar refuses is a
// [MalformedError] before any finding exists, and a well-formed edge naming a
// symbol the analyzer enumerates nothing under is an evaluation rather than a
// refusal.
package edges

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
)

// FileName is the name the Contract fixes for the document, at the target root.
// The location is fixed too, so the path a message carries for the document is
// this name.
const FileName = "deadset-edges.json"

// The forms a refused value is told to take.
const (
	documentWant  = "an object carrying the edges array, and an optional description"
	edgeWant      = "an edge naming id, provides and used_by, each a string, and an optional because"
	idWant        = "one or more solidus-separated segments of ASCII letters, digits, underscore, full stop and hyphen"
	referenceWant = "a stable symbol reference naming one symbol exactly, <language>://<scope>#<fragment>"
)

// ErrMalformed reports a document the grammar refuses before any finding exists.
// Every [MalformedError] carries it, so a caller maps the whole class to the usage
// exit code with errors.Is and reads the site with errors.As.
var ErrMalformed = errors.New("edges: malformed edges document")

// referenceShape is the shape an edge's reference takes: a lowercase language tag,
// the three characters ://, a scope holding no fragment separator, then the
// separator and the fragment. It is deliberately weaker than the published
// reference grammar, which the language's own reader owns; see [Document.Own].
var referenceShape = regexp.MustCompile(`^[a-z][a-z0-9]*://[^\s#]+#\S*$`)

// wildcard is the one character a configured root pattern is written with. An edge
// admits none: a declaration broader than one symbol would silence findings nobody
// declared.
const wildcard = '*'

// Side names which half of an edge a reference is. The spelling is the one a
// report carries.
type Side string

// The two sides of every edge. An edge has exactly these and no third: provides
// exists because used_by uses it.
const (
	Provides Side = "provides"
	UsedBy   Side = "used_by"
)

// Edge is one declared pair: its identifier, why it was declared, and the symbol
// each side names.
type Edge struct {
	ID       string // the identifier every evaluation of this edge names
	Because  string // why the pair exists, for whoever reads the document next
	Provides string // the reference of the symbol the other side uses
	UsedBy   string // the reference of the symbol that uses it
}

// Document is the declared edges of one target, in document order.
type Document struct {
	Description string // what a file header comment would have carried
	Edges       []Edge
}

// Reference is one side of one edge: which edge, which side, and the symbol that
// side names.
type Reference struct {
	Edge   string
	Side   Side
	Symbol string
}

// MalformedError is one document, one edge or one value the grammar refuses before
// any finding is produced, because it is a declaration the analysis cannot carry
// out.
type MalformedError struct {
	// Err is the decoder's own error, and is nil where the grammar refused a
	// value the decoder accepted.
	Err  error
	Text string         // the value or the member at fault, as written
	Want string         // what the grammar fixes for it
	Site token.Position // where the defect is written
}

// Error names the site, the text at fault and the form expected, so a caller that
// prints the error prints everything a maintainer needs to correct the document.
func (e *MalformedError) Error() string {
	at := e.Site.Filename
	if e.Site.Line > 0 {
		at = fmt.Sprintf("%s:%d:%d", at, e.Site.Line, e.Site.Column)
	}
	message := fmt.Sprintf("edges: %s: %s", at, e.Text)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message + ": want " + e.Want
}

// Unwrap returns the class every malformed document carries and, where a decoder
// refused the document, the error it returned, so a caller maps the class to the
// usage code with errors.Is and still reaches the cause.
func (e *MalformedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrMalformed}
	}
	return []error{ErrMalformed, e.Err}
}

// wireEdge is the closed key list of one edge. Every key the document declares
// appears here, so an undeclared key is a decode error, and each is a pointer so
// that an absent key is told from an empty value: the identifier and both
// references are required, because without them the edge pairs nothing.
type wireEdge struct {
	ID       *string `json:"id"`
	Because  *string `json:"because"`
	Provides *string `json:"provides"`
	UsedBy   *string `json:"used_by"`
}

// wireDocument is the closed key list of the document. The description carries
// what a file header comment would have and the analysis does not read it; the
// edges array is required and may be empty.
type wireDocument struct {
	Edges       *[]json.RawMessage `json:"edges"`
	Description string             `json:"description"`
}

// Read returns the declared edges at path, and an empty document where the file is
// absent.
//
// The document is strict JSON with a closed key list at both levels: an undeclared
// key, a value of the wrong type, a member written twice at any depth, a missing
// identifier or reference, an identifier outside its published form and a
// reference naming more than one symbol are each a [MalformedError], and no edge
// comes back with one.
//
// A reference is checked for the shape that decides whether it names one symbol,
// not for the whole reference grammar: a glob, a pattern and a bare name are
// refused here, while any other spelling outside the grammar names no symbol of
// any inventory, so the side it declares evaluates absent and the merge reports
// the edge as stale. That is the answer a misspelled reference is owed, and
// refusing it at the decoder would instead end the run.
func Read(path string) (*Document, error) {
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Document{}, nil
	case err != nil:
		return nil, fmt.Errorf("edges: read %s: %w", path, err)
	}

	sites, err := edgeSites(body)
	if err != nil {
		return nil, err
	}
	var wire wireDocument
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, &MalformedError{Err: err, Site: documentSite(), Text: FileName, Want: documentWant}
	}
	if wire.Edges == nil {
		return nil, &MalformedError{Site: documentSite(), Text: FileName, Want: documentWant}
	}

	held := &Document{Description: wire.Description}
	for i, raw := range *wire.Edges {
		at := documentSite()
		if i < len(sites) {
			at = sites[i]
		}
		edge, err := decodeEdge(raw, at)
		if err != nil {
			return nil, err
		}
		held.Edges = append(held.Edges, edge)
	}
	return held, nil
}

// decodeEdge reads one edge and refuses every defect that ends the run.
func decodeEdge(body []byte, at token.Position) (Edge, error) {
	refuse := func(err error, text, want string) (Edge, error) {
		return Edge{}, &MalformedError{Err: err, Site: at, Text: text, Want: want}
	}

	var wire wireEdge
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return refuse(err, string(body), edgeWant)
	}
	switch {
	case wire.ID == nil:
		return refuse(nil, "an edge naming no id", edgeWant)
	case wire.Provides == nil:
		return refuse(nil, "an edge naming no provides side", edgeWant)
	case wire.UsedBy == nil:
		return refuse(nil, "an edge naming no used_by side", edgeWant)
	case !idForm.MatchString(*wire.ID):
		return refuse(nil, strconv.Quote(*wire.ID), idWant)
	case !namesOneSymbol(*wire.Provides):
		return refuse(nil, strconv.Quote(*wire.Provides), referenceWant)
	case !namesOneSymbol(*wire.UsedBy):
		return refuse(nil, strconv.Quote(*wire.UsedBy), referenceWant)
	}
	return Edge{
		ID:       *wire.ID,
		Because:  value(wire.Because),
		Provides: *wire.Provides,
		UsedBy:   *wire.UsedBy,
	}, nil
}

// idForm is an edge identifier: one or more solidus-separated segments of ASCII
// letters, digits, underscore, full stop and hyphen, which is the form a report's
// evaluation record carries.
var idForm = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)

// namesOneSymbol reports whether one reference names exactly one symbol: it takes
// the shape of a language-tagged reference, and it carries no wildcard.
func namesOneSymbol(ref string) bool {
	return referenceShape.MatchString(ref) && !carriesWildcard(ref)
}

// carriesWildcard reports whether a reference carries the wildcard in a position
// where it is one. The character stands for itself inside a quoted component and
// inside a computed key, so only an occurrence outside both makes the reference name
// more than the one symbol an edge may name.
func carriesWildcard(ref string) bool {
	quoted, computed, escaped := false, false, false
	for _, c := range ref {
		switch {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case quoted:
			quoted = c != '\''
		case computed:
			computed = c != ']'
		case c == '\'':
			quoted = true
		case c == '[':
			computed = true
		case c == wildcard:
			return true
		}
	}
	return false
}

// Own returns every side of every declared edge whose symbol reference carries
// language's own tag, in document order, the provides side of an edge before its
// used_by side. Both sides of one edge are returned where both name that language.
//
// It answers what an analyzer of that language evaluates. A side of another
// language is not returned at all, because a side no analyzer evaluated has no
// record in a report and is not absent: nothing looked.
func (d *Document) Own(language string) []Reference {
	if d == nil {
		return nil
	}
	tag := language + "://"
	own := make([]Reference, 0, 2*len(d.Edges))
	for i := range d.Edges {
		edge := &d.Edges[i]
		for _, side := range [...]struct {
			side   Side
			symbol string
		}{{Provides, edge.Provides}, {UsedBy, edge.UsedBy}} {
			if strings.HasPrefix(side.symbol, tag) {
				own = append(own, Reference{Edge: edge.ID, Side: side.side, Symbol: side.symbol})
			}
		}
	}
	return own
}

// value is the string a key holds, and the empty string where the key is absent.
func value(held *string) string {
	if held == nil {
		return ""
	}
	return *held
}

// documentSite names the document itself, for a defect no edge position describes.
func documentSite() token.Position { return token.Position{Filename: FileName} }

// role is what one value of the document is, which is what decides whether the
// brace opening it is an edge's position.
type role uint8

const (
	roleDocument role = iota // the one value the document holds
	roleArray                // the array the edges member holds
	roleEdge                 // one edge of that array
	roleOther                // every value below an edge, and the description
)

// walk reads the document as tokens, which is what a decode into the closed key
// list cannot do: a member one object writes twice, at any depth, is a document
// whose later value silently replaces the earlier one, so it reads as though the
// member were written once. The walk also records where each edge's opening brace
// is, which is the position a message about that edge carries.
type walk struct {
	body  []byte
	edges []token.Position
}

// edgeSites returns the position of the brace that opens each edge, refusing a
// document that writes one member twice or carries anything after its one value.
func edgeSites(body []byte) ([]token.Position, error) {
	w := &walk{body: body}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := w.value(dec, roleDocument); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, &MalformedError{
			Site: documentSite(),
			Text: "a second value after the document",
			Want: documentWant,
		}
	}
	return w.edges, nil
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
		if at == roleEdge {
			w.edges = append(w.edges, w.at(dec.InputOffset()-1))
		}
		return w.object(dec, at)
	case '[':
		return w.array(dec, at)
	default:
		return w.refuse(dec, fmt.Errorf("unexpected %q", delim))
	}
}

// object reads the members of the object the decoder has just opened, refusing one
// the object writes twice.
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
				Site: w.at(dec.InputOffset()),
				Text: fmt.Sprintf("the member %s is written twice, so neither value is chosen", strconv.Quote(name)),
				Want: documentWant,
			}
		}
		seen[name] = true
		if err := w.value(dec, member(at, name)); err != nil {
			return err
		}
	}
	return w.close(dec)
}

// array reads the values of the array the decoder has just opened.
func (w *walk) array(dec *json.Decoder, at role) error {
	held := roleOther
	if at == roleArray {
		held = roleEdge
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

// member is what the value one member holds is: the array of edges for the
// document's own edges member, and nothing of interest below that.
func member(at role, name string) role {
	if at == roleDocument && name == "edges" {
		return roleArray
	}
	return roleOther
}

// refuse reports a document the token walk could not read, at the position it
// stopped at.
func (w *walk) refuse(dec *json.Decoder, err error) error {
	return &MalformedError{
		Err:  err,
		Site: w.at(dec.InputOffset()),
		Text: FileName,
		Want: documentWant,
	}
}

// at renders one byte offset of the document as the position a message carries:
// the line, counted from one, and the column, counting UTF-16 code units from one.
func (w *walk) at(offset int64) token.Position {
	if offset < 0 || offset > int64(len(w.body)) {
		return documentSite()
	}
	before := w.body[:offset]
	start := bytes.LastIndexByte(before, '\n') + 1
	return token.Position{
		Filename: FileName,
		Offset:   int(offset),
		Line:     1 + bytes.Count(before, []byte("\n")),
		Column:   graph.Column(before[start:], len(before)-start),
	}
}

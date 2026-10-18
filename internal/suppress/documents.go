package suppress

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// OwnScheme opens every reference of this analyzer's language. A record naming
// another scheme belongs to the analyzer of that language, which reads the same
// document.
const OwnScheme = "go://"

// formChecked are the schemes whose references this analyzer checks against the
// grammar's forms: its own, and the other language's of a target analyzed in
// both, whose malformed record ends the run here as it would there. A record of
// any other scheme is read unchecked and binds nothing.
var formChecked = []string{OwnScheme, "ts://"}

// ErrUnreadable reports a suppression document present at its path that cannot
// be read. Which failure class that is depends on where the caller reads it from.
var ErrUnreadable = errors.New("cannot be read")

// symbolWant is the form a malformed reference is told to take.
const symbolWant = "a stable symbol reference, go://<import-path>#<fragment> or ts://<package>/<source-path>#<fragment>"

// IgnoreFile reads the ignore file at path and binds each of its entries to the
// declarations it names, through the shared reader and binder. An absent file is
// an empty one; a malformed document is a [core.MalformedError].
func IgnoreFile(path string, symbols []inventory.Symbol) ([]Record, []Refusal, error) {
	entries, err := documentEntries(path, core.ReadIgnore, core.Ignore)
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Ignore, MechanismIgnore)
}

// Baseline reads the baseline at path and binds each of its rows as [IgnoreFile]
// binds an entry. A row's reason is the analyzer that recorded it.
func Baseline(path string, symbols []inventory.Symbol) ([]Record, []Refusal, error) {
	entries, err := BaselineRows(path)
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Baseline, MechanismBaseline)
}

// BaselineDocument reads one baseline document held in memory, as [Baseline]
// reads the one at the target root.
func BaselineDocument(body []byte, symbols []inventory.Symbol) ([]Record, []Refusal, error) {
	entries, err := core.ReadBaseline(body)
	if err == nil {
		err = checkForms(entries, core.Baseline)
	}
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Baseline, MechanismBaseline)
}

// BaselineRows is every row of the baseline at path, read and form-checked, none
// for an absent document.
func BaselineRows(path string) ([]core.Entry, error) {
	return documentEntries(path, core.ReadBaseline, core.Baseline)
}

// documentEntries reads one suppression document's entries, an absent file
// holding none and a present one it cannot read an [ErrUnreadable].
func documentEntries(path string, read func([]byte) ([]core.Entry, error), mechanism core.Mechanism) ([]core.Entry, error) {
	body, err := readInDir(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, atomicfile.ErrFileTooLarge):
		return nil, fmt.Errorf("%w: %s: %w", core.ErrMalformed, filepath.Base(path), err)
	case err != nil:
		return nil, fmt.Errorf("%s %w: %w", filepath.Base(path), ErrUnreadable, err)
	}
	entries, err := read(body)
	if err == nil {
		err = checkForms(entries, mechanism)
	}
	return entries, err
}

// readInDir reads the regular file at path through its directory, so a name
// resolving outside that directory or to anything but a regular file is refused.
func readInDir(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return atomicfile.ReadBoundedInRoot(context.Background(), root, filepath.Base(path), vocab.DocumentMaxBytes)
}

// checkForms refuses, in document order, the first entry of a form-checked scheme
// whose reference is in none of the grammar's forms.
func checkForms(entries []core.Entry, mechanism core.Mechanism) error {
	for i := range entries {
		symbol := entries[i].Symbol
		for _, scheme := range formChecked {
			if strings.HasPrefix(symbol, scheme) && !referenceForm(symbol) {
				return &core.MalformedError{Text: strconv.Quote(symbol), Want: symbolWant, Site: entries[i].Site, Mechanism: mechanism}
			}
		}
	}
	return nil
}

// bind resolves the entries of this analyzer's scheme against the inventory, by
// reference and file together, and carries each record and refusal in the shape
// the findings pass reads.
func bind(entries []core.Entry, symbols []inventory.Symbol, mechanism core.Mechanism, own Mechanism) ([]Record, []Refusal, error) {
	byReference := make(map[[2]string][]string, len(symbols))
	for i := range symbols {
		at := [2]string{symbols[i].Ref, symbols[i].Pos.Filename}
		byReference[at] = append(byReference[at], string(symbols[i].ID))
	}
	binder := core.Binder{
		Lookup: func(symbol, path string) []string { return byReference[[2]string{symbol, path}] },
		Holder: inventory.ParameterHolder,
		Valid:  referenceForm,
		Scheme: OwnScheme,
	}
	bound, refused, err := binder.Bind(entries, mechanism)
	if err != nil {
		return nil, nil, err
	}
	var records []Record
	for i := range bound {
		one := &bound[i]
		records = append(records, Record{
			Code: one.Code, Symbol: one.Symbol, Path: one.Path, Reason: one.Reason,
			Bound: inventory.SymbolID(one.Bound), Site: position(one.Site), Mechanism: own,
		})
	}
	var refusals []Refusal
	for i := range refused {
		one := &refused[i]
		refusals = append(refusals, Refusal{
			Reported: one.Reported, Code: one.Code, Symbol: one.Symbol, Path: one.Path, Reason: one.Reason,
			Site: position(one.Site), Mechanism: own,
		})
	}
	return records, refusals, nil
}

// position is a document site as the position a finding carries.
func position(at core.Site) token.Position {
	return token.Position{Filename: at.Path, Line: at.Line, Column: at.Column}
}

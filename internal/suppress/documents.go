package suppress

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"github.com/cplieger/go/filesafe"
)

// IgnoreFile reads the ignore file at path and binds each of its entries to the
// declarations it names. An absent file is an empty one; a malformed document is
// a [core.MalformedError].
func IgnoreFile(path string, symbols []inventory.Symbol) ([]core.Record, []core.Refusal, error) {
	entries, err := documentEntries(path, core.ReadIgnore)
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Ignore)
}

// Baseline reads the baseline at path and binds each of its rows as [IgnoreFile]
// binds an entry. A row's reason is the analyzer that recorded it.
func Baseline(path string, symbols []inventory.Symbol) ([]core.Record, []core.Refusal, error) {
	entries, err := BaselineRows(path)
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Baseline)
}

// BaselineDocument reads one baseline document held in memory, as [Baseline]
// reads the one at the target root.
func BaselineDocument(body []byte, symbols []inventory.Symbol) ([]core.Record, []core.Refusal, error) {
	entries, err := core.ReadBaseline(body)
	if err != nil {
		return nil, nil, err
	}
	return bind(entries, symbols, core.Baseline)
}

// BaselineRows is every row of the baseline at path, none for an absent document.
func BaselineRows(path string) ([]core.Entry, error) {
	return documentEntries(path, core.ReadBaseline)
}

// documentEntries reads one suppression document's entries, an absent file
// holding none and a present one it cannot read an [ErrUnreadable].
func documentEntries(path string, read func([]byte) ([]core.Entry, error)) ([]core.Entry, error) {
	body, err := readInDir(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, filesafe.ErrFileTooLarge):
		return nil, fmt.Errorf("%w: %s: %w", core.ErrMalformed, filepath.Base(path), err)
	case err != nil:
		return nil, fmt.Errorf("%s %w: %w", filepath.Base(path), ErrUnreadable, err)
	}
	return read(body)
}

// readInDir reads the regular file at path through its directory, so a name
// resolving outside that directory or to anything but a regular file is refused.
func readInDir(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return filesafe.ReadBoundedInRoot(context.Background(), root, filepath.Base(path), vocab.DocumentMaxBytes)
}

// bind resolves the entries of this analyzer's scheme against the inventory, by
// reference and the file a finding's position names together.
func bind(entries []core.Entry, symbols []inventory.Symbol, mechanism core.Mechanism) ([]core.Record, []core.Refusal, error) {
	byReference := make(map[[2]string][]string, len(symbols))
	for i := range symbols {
		at := [2]string{symbols[i].Ref, symbols[i].Pos.Filename}
		byReference[at] = append(byReference[at], string(symbols[i].ID))
	}
	binder := core.Binder{
		Lookup: func(symbol, path string) []string { return byReference[[2]string{symbol, path}] },
		Holder: inventory.ParameterHolder,
		Scheme: OwnScheme,
	}
	return binder.Bind(entries, mechanism)
}

// Marked is the declarations the records mark live, which seed the sweep;
// [core.Record.MarksLive] decides which.
func Marked(records []core.Record) []inventory.SymbolID {
	var held []inventory.SymbolID
	for i := range records {
		if records[i].MarksLive() {
			held = append(held, inventory.SymbolID(records[i].Bound))
		}
	}
	return held
}

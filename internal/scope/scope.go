// Package scope reads the scope document: the module a run analyzes and the
// consumers whose references count against it. A scope document names paths on
// the local filesystem; reading one makes no network request and resolves no
// module.
package scope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Role values a scope document declares for a module.
const (
	RoleTarget   = "target"
	RoleConsumer = "consumer"
)

// maxDocumentBytes bounds a scope document, which names one target and its
// consumers. A larger file is refused rather than decoded.
const maxDocumentBytes = 1 << 20

var (
	// ErrNoTarget reports a document that names no target path.
	ErrNoTarget = errors.New("scope: no target path")

	// ErrRole reports a module whose declared role is not the one its position
	// in the document gives it.
	ErrRole = errors.New("scope: invalid role")

	// ErrUnimplemented reports a declared key the reader refuses rather than
	// ignores.
	ErrUnimplemented = errors.New("scope: unimplemented key")

	// ErrTooLarge reports a document above the size bound.
	ErrTooLarge = errors.New("scope: document too large")

	// ErrTrailingContent reports bytes after the document's closing brace.
	ErrTrailingContent = errors.New("scope: trailing content after document")

	// errNotDirectory reports a target path that exists and is not a directory.
	errNotDirectory = errors.New("not a directory")
)

// Module is one module the run loads: the target or a declared consumer.
type Module struct {
	// ID is the module path. A document may state it; otherwise it stays empty
	// until a load reports the module the path belongs to.
	ID   string
	Role string // RoleTarget or RoleConsumer
	Path string // an absolute filesystem path
}

// Document is the resolved scope: exactly one target and zero or more
// consumers, every path absolute.
type Document struct {
	Target    Module
	Consumers []Module
}

// wireModule is one module as a scope document spells it.
type wireModule struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Path string `json:"path"`
}

// wireDocument is the closed key list of a scope document. Every key the format
// declares appears here, so an undeclared key is a decode error and a declared
// key the reader does not implement is refused by name.
type wireDocument struct {
	Target    wireModule   `json:"target"`
	Workspace string       `json:"workspace"`
	Consumers []wireModule `json:"consumers"`
}

// Read decodes the scope document at path. Decoding is strict: an undeclared key
// is an error, trailing content after the document is an error, and a relative
// path inside the document resolves against the document's own directory.
//
// Read returns [ErrNoTarget], [ErrRole], [ErrUnimplemented], [ErrTooLarge] or
// [ErrTrailingContent] for a document it refuses, an error satisfying
// errors.Is(err, fs.ErrNotExist) when the document is absent, and a
// *json.SyntaxError or *json.UnmarshalTypeError for one it cannot decode.
func Read(path string) (Document, error) {
	body, err := readBounded(path)
	if err != nil {
		return Document{}, err
	}

	var wire wireDocument
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return Document{}, fmt.Errorf("scope: decode %s: %w", path, err)
	}
	if dec.More() {
		return Document{}, fmt.Errorf("%w: %s", ErrTrailingContent, path)
	}

	return resolve(&wire, filepath.Dir(path))
}

// ForDir is the scope of a run given no scope document: dir is the target and
// nothing else is loaded. It returns an error naming dir when dir is not an
// existing directory.
func ForDir(dir string) (Document, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Document{}, fmt.Errorf("scope: resolve %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Document{}, fmt.Errorf("scope: target %s: %w", abs, err)
	}
	if !info.IsDir() {
		return Document{}, fmt.Errorf("scope: target %s: %w", abs, errNotDirectory)
	}
	return Document{Target: Module{Role: RoleTarget, Path: abs}}, nil
}

// readBounded returns the document's bytes, refusing one above the size bound
// without reading it whole.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("scope: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// One byte past the bound tells a document at the limit from one over it.
	body, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("scope: read %s: %w", path, err)
	}
	if len(body) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, path, maxDocumentBytes)
	}
	return body, nil
}

// resolve turns a decoded document into a Document with absolute paths.
func resolve(wire *wireDocument, base string) (Document, error) {
	if wire.Workspace != "" {
		return Document{}, fmt.Errorf("%w: workspace", ErrUnimplemented)
	}
	if wire.Target.Path == "" {
		return Document{}, ErrNoTarget
	}
	if wire.Target.Role != "" && wire.Target.Role != RoleTarget {
		return Document{}, fmt.Errorf("%w: target declares role %q", ErrRole, wire.Target.Role)
	}

	target, err := resolveModule(wire.Target, RoleTarget, base)
	if err != nil {
		return Document{}, err
	}
	doc := Document{Target: target}

	for _, w := range wire.Consumers {
		if w.Role != "" && w.Role != RoleConsumer {
			return Document{}, fmt.Errorf("%w: consumer %s declares role %q", ErrRole, w.Path, w.Role)
		}
		consumer, err := resolveModule(w, RoleConsumer, base)
		if err != nil {
			return Document{}, err
		}
		doc.Consumers = append(doc.Consumers, consumer)
	}
	return doc, nil
}

// resolveModule resolves one decoded module against base and stamps it with role.
func resolveModule(w wireModule, role, base string) (Module, error) {
	if w.Path == "" {
		return Module{}, fmt.Errorf("scope: %s declares an empty path", role)
	}
	path := w.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Module{}, fmt.Errorf("scope: resolve %s: %w", w.Path, err)
	}
	return Module{ID: w.ID, Role: role, Path: abs}, nil
}

// Package scope resolves what a run loads: the module it analyzes and the
// consumers whose references count against it. The scope comes from a scope
// document on disk or from a workspace file that lists the target among its
// modules, and from nowhere else. Every path it names is a path on the local
// filesystem, and resolving one makes no network request and fetches no module.
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

	// ErrTooLarge reports a document above the size bound.
	ErrTooLarge = errors.New("scope: document too large")

	// ErrTrailingContent reports bytes after the document's closing brace.
	ErrTrailingContent = errors.New("scope: trailing content after document")

	// errNotDirectory reports a target path that exists and is not a directory.
	errNotDirectory = errors.New("not a directory")
)

// Module is one module the run loads: the target or a declared consumer.
//
// The role a document declares for a module is not carried here, because the
// resolved scope holds exactly one target and a list of consumers, so a module's
// role is where it sits. What a document declares is read and refused where it
// contradicts that position, which is the only place the declaration can be wrong.
type Module struct {
	// ID is the module path. A document may state it; otherwise it stays empty
	// until a load reports the module the path belongs to.
	ID   string
	Path string // an absolute filesystem path
}

// Document is the resolved scope: exactly one target and zero or more
// consumers, every path absolute.
type Document struct {
	// Workspace is the absolute path of the workspace file whose build list a
	// consumer's load resolves the target through, and is empty when the scope
	// names none. A consumer reaches the target's own directory through its
	// module file or through a workspace, and which of the two a scope offers is
	// the scope's to declare.
	Workspace string

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
// declares appears here, so an undeclared key is a decode error.
type wireDocument struct {
	Target    wireModule   `json:"target"`
	Workspace string       `json:"workspace"`
	Consumers []wireModule `json:"consumers"`
}

// Read decodes the scope document at path. Decoding is strict: an undeclared key
// is an error, trailing content after the document is an error, and a relative
// path inside the document resolves against the document's own directory.
//
// Read returns [ErrNoTarget], [ErrRole], [ErrTooLarge] or [ErrTrailingContent]
// for a document it refuses, an error satisfying errors.Is(err, fs.ErrNotExist)
// when the document is absent, and a *json.SyntaxError or
// *json.UnmarshalTypeError for one it cannot decode.
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

// ForDir is the scope of a run given no scope document and no workspace: dir is
// the target and nothing else is loaded. It returns an error naming dir when dir
// is not an existing directory.
func ForDir(dir string) (Document, error) {
	abs, err := existingDir(dir)
	if err != nil {
		return Document{}, err
	}
	return Document{Target: Module{Path: abs}}, nil
}

// existingDir returns dir as an absolute path, and an error naming it when it is
// not an existing directory.
func existingDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("scope: resolve %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("scope: target %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("scope: target %s: %w", abs, errNotDirectory)
	}
	return abs, nil
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

	if wire.Workspace != "" {
		workspace := wire.Workspace
		if !filepath.IsAbs(workspace) {
			workspace = filepath.Join(base, workspace)
		}
		doc.Workspace = filepath.Clean(workspace)
	}

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

// resolveModule resolves one decoded module against base, naming role in the
// refusal of a module that declares no path.
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
	return Module{ID: w.ID, Path: abs}, nil
}

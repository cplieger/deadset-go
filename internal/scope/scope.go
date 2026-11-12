// Package scope resolves what a run loads: the module it analyzes and the
// consumers whose references count against it. The scope comes from a scope
// document on disk or from a workspace file that lists the target among its
// modules, and from nowhere else. Every path it names is a path on the local
// filesystem, and resolving one makes no network request and fetches no module.
package scope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

var (
	// ErrNoTarget reports a scope that names no target path.
	ErrNoTarget = errors.New("scope: no target path")

	// errRefused reports a document the Contract's scope schema refuses.
	errRefused = errors.New("scope: refused document")

	// errNotDirectory reports a target path that exists and is not a directory.
	errNotDirectory = errors.New("not a directory")

	// ErrNamed reports a target or a scope document the invocation names that cannot
	// be read: absent, not a directory or a file as its role needs, or refused by the
	// scope schema. The fix is to the invocation, whatever the platform's wording.
	ErrNamed = errors.New("scope: an input the invocation names cannot be read")
)

// namedError is one [ErrNamed] failure, which keeps the message and the cause it
// wraps.
type namedError struct{ err error }

func (e namedError) Error() string { return e.err.Error() }

func (e namedError) Unwrap() error { return e.err }

func (namedError) Is(target error) bool { return target == ErrNamed }

// Module is one module the run loads: the target or a declared consumer.
//
// The role a document declares for a module is not carried here, because the
// resolved scope holds exactly one target and a list of consumers, so a module's
// role is where it sits. The schema refuses a declared role that contradicts the
// position, which is the only place the declaration can be wrong.
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
	Path string `json:"path"`
}

// wireDocument is the members of a scope document a run reads.
type wireDocument struct {
	Workspace string       `json:"workspace"`
	Target    wireModule   `json:"target"`
	Consumers []wireModule `json:"consumers"`
}

const schemaPath = "contract/scope.schema.json"

var scopeSchema = sync.OnceValues(func() (*document.Schema, error) {
	return document.Compile(spec.Contract, schemaPath)
})

// read decodes the scope document at path as one strict instance of the
// Contract's scope schema, refused as [errRefused] naming the first place at
// fault, and resolves a relative path inside it against the document's own
// directory. A document it cannot read or refuses is an [ErrNamed] error, an absent
// one also satisfying errors.Is(err, fs.ErrNotExist).
func read(ctx context.Context, path string) (Document, error) {
	body, err := jsondoc.Read(ctx, path, vocab.DocumentMaxBytes)
	if err != nil {
		return Document{}, namedError{fmt.Errorf("scope: read %s: %w", path, err)}
	}
	schema, err := scopeSchema()
	if err != nil {
		return Document{}, err
	}
	var wire wireDocument
	if err := jsondoc.Unmarshal(schema, path, body, &wire); err != nil {
		return Document{}, namedError{fmt.Errorf("%w: %w", errRefused, err)}
	}
	return resolve(&wire, filepath.Dir(path))
}

// ForDir is the scope of a run given no scope document and no workspace: dir is
// the target and nothing else is loaded. It returns an [ErrNamed] error naming dir
// when dir is not an existing directory.
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
		return "", namedError{fmt.Errorf("scope: target %s: %w", abs, err)}
	}
	if !info.IsDir() {
		return "", namedError{fmt.Errorf("scope: target %s: %w", abs, errNotDirectory)}
	}
	return abs, nil
}

// resolve turns a decoded document into a Document with absolute paths.
func resolve(wire *wireDocument, base string) (Document, error) {
	target, err := resolveModule(wire.Target, base)
	if err != nil {
		return Document{}, err
	}
	doc := Document{Target: target}
	if wire.Workspace != "" {
		doc.Workspace = filepath.Clean(absolute(wire.Workspace, base))
	}
	for _, w := range wire.Consumers {
		consumer, err := resolveModule(w, base)
		if err != nil {
			return Document{}, err
		}
		doc.Consumers = append(doc.Consumers, consumer)
	}
	return doc, nil
}

// resolveModule resolves one decoded module against base.
func resolveModule(w wireModule, base string) (Module, error) {
	abs, err := filepath.Abs(absolute(w.Path, base))
	if err != nil {
		return Module{}, fmt.Errorf("scope: resolve %s: %w", w.Path, err)
	}
	return Module{ID: w.ID, Path: abs}, nil
}

// Within is path relative to dir with forward slashes, and whether path is dir
// or below it ([Root.Within]).
func Within(dir, path string) (string, bool) { return RootOf(dir).Within(path) }

// Root is a directory paths are named relative to, as the run spells it and as
// it resolves, resolved once: one directory spelled two ways, as macOS spells
// /private/var through its /var link, is one root. The zero Root is no root, and
// its methods place nothing below it: make one with [RootOf].
type Root struct {
	path, resolved string
}

// RootOf is the root at dir, made absolute, resolved where every link on it
// resolves and as spelled otherwise.
func RootOf(dir string) Root {
	path, err := filepath.Abs(dir)
	if err != nil {
		path = filepath.Clean(dir)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	return Root{path: path, resolved: resolved}
}

// Within is path relative to the root with forward slashes, and whether path is
// the root or below it. The names decide first, so a link inside the root keeps
// its name. Where they place path outside, the shortest leading part of path that
// resolves to the root or below it places it and the rest keeps its names, so
// path sits where it resolves, and a link below that part keeps its name.
func (r Root) Within(path string) (string, bool) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if relative, inside := lexicallyWithin(r.path, path); inside {
		return relative, true
	}
	for _, lead := range slices.Backward(ancestors(path)) {
		resolved, err := filepath.EvalSymlinks(lead)
		if err != nil {
			return "", false
		}
		if _, inside := lexicallyWithin(r.resolved, resolved); inside {
			rest, _ := filepath.Rel(lead, path)
			return lexicallyWithin(r.resolved, filepath.Join(resolved, rest))
		}
	}
	return "", false
}

// Rel is path relative to the root with forward slashes, as every path a setup
// failure line or a note names is ([program.10]): [Root.Within] where the root
// holds path, and otherwise from the resolved root to where path's directory
// resolves, with `..` segments, which leave the directory a link resolves to
// rather than the one it sits in. A path with no relative form, on another
// volume, is itself.
func (r Root) Rel(path string) string {
	if relative, inside := r.Within(path); inside {
		return relative
	}
	relative, err := filepath.Rel(r.resolved, Resolved(path))
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// Respell is path in the root's own spelling where the root holds it
// ([Root.Within]), so two spellings of one file below the root are one name, and
// path itself elsewhere.
func (r Root) Respell(path string) string {
	if relative, inside := r.Within(path); inside {
		return filepath.Join(r.path, filepath.FromSlash(relative))
	}
	return path
}

// Resolved is path, made absolute, with its nearest directory that exists
// resolved and the names below that directory kept, so a path that names a link
// still names the link.
func Resolved(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	for _, dir := range ancestors(filepath.Dir(abs)) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			rest, _ := filepath.Rel(dir, abs)
			return filepath.Join(resolved, rest)
		}
	}
	return abs
}

// ancestors is path and each directory above it, path first.
func ancestors(path string) []string {
	held := []string{path}
	for parent := filepath.Dir(path); parent != path; path, parent = parent, filepath.Dir(parent) {
		held = append(held, parent)
	}
	return held
}

// lexicallyWithin is [Within] by the names alone.
func lexicallyWithin(dir, path string) (string, bool) {
	relative, err := filepath.Rel(dir, path)
	if err != nil || (relative != "." && !filepath.IsLocal(relative)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

// absolute is path, resolved against base where it is relative. A path rooted
// with no volume, which only Windows has, takes the volume of base: it names no
// directory below base, and the current drive is not the document's.
func absolute(path, base string) string {
	switch {
	case filepath.IsAbs(path):
		return path
	case path != "" && os.IsPathSeparator(path[0]):
		return filepath.Join(filepath.VolumeName(base), path)
	}
	return filepath.Join(base, path)
}

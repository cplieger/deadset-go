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
	"strings"
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

// Root is a directory paths are placed against, as the run spells it and as it
// resolves, resolved once, as macOS spells /private/var through /var. It is the
// run's one placement: every path compared with a directory, named relative to
// one or tested for being one is resolved by [resolvePath] and compared with the
// resolved directory by its names ([Root.Place], [Root.PlaceEntry]). A path whose
// resolved names are not the root's, a bind mount or a case variant, is outside
// it. The zero Root is no root, and its methods place nothing below it: make one
// with [RootOf].
type Root struct{ at *placed }

// placed is one root: its directory as the run spells it and as it resolves, each
// path it resolved, as a walk or a file set names one directory many times, and,
// where names are checked, the name each file it parsed was given, by the file's
// own name in its resolved directory ([Root.Parse]).
type placed struct {
	held           map[string]string
	names          map[string]string
	path, resolved string
	mu             sync.Mutex
}

// RootOf is the root at dir, made absolute and resolved ([resolvePath]).
func RootOf(dir string) Root {
	path, err := filepath.Abs(dir)
	if err != nil {
		path = filepath.Clean(dir)
	}
	return Root{at: &placed{path: path, resolved: resolvePath(path), held: make(map[string]string), names: make(map[string]string)}}
}

// Dir is the root's directory as the run spells it, made absolute, and empty for
// the zero Root.
func (r Root) Dir() string {
	if r.at == nil {
		return ""
	}
	return r.at.path
}

// resolvePath is the absolute path with every link on it resolved, by
// filepath.EvalSymlinks: where path does not exist, its deepest existing ancestor
// resolved and the names below that ancestor kept.
func resolvePath(path string) string {
	for _, at := range ancestors(path) {
		if resolved, err := filepath.EvalSymlinks(at); err == nil {
			rest, _ := filepath.Rel(at, path)
			return filepath.Join(resolved, rest)
		}
	}
	return path
}

// resolvedPath is the absolute path resolved ([resolvePath]), once per Root.
func (r Root) resolvedPath(path string) string {
	r.at.mu.Lock()
	held, found := r.at.held[path]
	r.at.mu.Unlock()
	if found {
		return held
	}
	held = resolvePath(path)
	r.at.mu.Lock()
	r.at.held[path] = held
	r.at.mu.Unlock()
	return held
}

// Place is the directory at path relative to the root with forward slashes, and
// whether it is the root or below it: path is resolved ([resolvePath]), every link on
// it the directory itself included, and compared with the resolved root by its
// names.
func (r Root) Place(path string) (string, bool) {
	if r.at == nil {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return lexicallyWithin(r.at.resolved, r.resolvedPath(abs))
}

// PlaceEntry is [Root.Place] for an entry of a directory, a file or a directory a
// listing names: its directory is placed and its own name kept, so an entry inside
// the root that is a link is named as the link rather than where it leads. An entry
// its directory places outside the root is placed where it leads, so a link from
// outside to the root, or into it, is the directory it leads to.
func (r Root) PlaceEntry(path string) (string, bool) {
	if r.at == nil {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if relative, inside := lexicallyWithin(r.at.resolved, r.entryPath(abs)); inside {
		return relative, true
	}
	return r.Place(abs)
}

// entryPath is the absolute path with its directory resolved and its own name
// kept.
func (r Root) entryPath(abs string) string {
	parent := filepath.Dir(abs)
	if parent == abs {
		return r.resolvedPath(abs)
	}
	return filepath.Join(r.resolvedPath(parent), filepath.Base(abs))
}

// Rel is path relative to the root with forward slashes, as every path a setup
// failure line or a note names is ([program.10]): [Root.PlaceEntry] where the root
// holds path, and otherwise from the resolved root to where path's directory
// resolves, with `..` segments, which leave the directory a link resolves to
// rather than the one it sits in. A path with no relative form, on another volume
// or against the zero Root, is itself.
func (r Root) Rel(path string) string {
	if relative, inside := r.PlaceEntry(path); inside {
		return relative
	}
	abs, err := filepath.Abs(path)
	if r.at == nil || err != nil {
		return filepath.ToSlash(path)
	}
	relative, err := filepath.Rel(r.at.resolved, r.entryPath(abs))
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// Respell is the directory at path in the root's own spelling where the root holds
// it ([Root.Place]), so every spelling of one directory below the root, through a
// link outside the root or inside it, is one name, and path itself elsewhere.
func (r Root) Respell(path string) string {
	if relative, inside := r.Place(path); inside {
		return filepath.Join(r.at.path, filepath.FromSlash(relative))
	}
	return path
}

// Is reports whether the directory at path is the root where both resolve
// ([Root.Place]).
func (r Root) Is(path string) bool {
	relative, inside := r.Place(path)
	return inside && relative == "."
}

// Same reports whether two paths name one directory where both resolve
// ([Root.Is]).
func Same(a, b string) bool { return RootOf(b).Is(a) }

// SameEntry reports whether a, the toolchain's name of an entry, is the entry b
// ([Root.PlaceEntry]): where a's directory resolves to b's directory or below it,
// whether it is b's directory and a's own name is b's base name; where a's
// directory resolves outside b's, whether a itself resolves to b. The order
// matters: a link and the file it leads to, under two base names, are one entry
// only with the link first.
func SameEntry(a, b string) bool {
	relative, inside := RootOf(filepath.Dir(b)).PlaceEntry(a)
	return inside && relative == filepath.Base(b)
}

// UnderName is name relative to prefix, empty for prefix itself, and whether name
// is prefix or below it by whole slash-separated elements: an import path below a
// module path, a cgroup below another, or one root-relative path below another.
// Such a name names no file to resolve, so its elements alone decide.
func UnderName(prefix, name string) (string, bool) {
	if name == prefix {
		return "", true
	}
	if rest, below := strings.CutPrefix(name, prefix+"/"); below {
		return rest, true
	}
	return "", false
}

// ancestors is path and each directory above it, path first.
func ancestors(path string) []string {
	held := []string{path}
	for parent := filepath.Dir(path); parent != path; path, parent = parent, filepath.Dir(parent) {
		held = append(held, parent)
	}
	return held
}

// lexicallyWithin is path relative to dir with forward slashes, and whether path is
// dir or below it, by the names alone: [Root.Place] compares two resolved paths
// with it.
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

package scope

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"golang.org/x/mod/modfile"
)

// workspaceFileName is the name of the file that lists the modules of a Go
// workspace.
const workspaceFileName = "go.work"

// errWorkspace reports a workspace file that cannot be read or parsed.
var errWorkspace = errors.New("scope: unreadable workspace")

// ErrIrregular reports a module or workspace file that is no regular file.
var ErrIrregular = errors.New("no regular file")

// IrregularFix is the fix of a file the toolchain reads that is a pipe or a
// device, which it may block on or read without end.
const IrregularFix = "replace it with a regular file"

// ReadRegularFile reads the file at path where Stat, following links, says it is
// a regular file, opening nothing else: a pipe would block the read, and a device
// never end it. Its error names path relative to root, as every path a setup
// failure names is ([program.10]).
func ReadRegularFile(root, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		err = ErrIrregular
	}
	var text []byte
	if err == nil {
		text, err = os.ReadFile(path)
	}
	if err != nil {
		if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
			err = pathErr.Err
		}
		return nil, fmt.Errorf("%s: %w", RelativeSlash(root, path), err)
	}
	return text, nil
}

// RelativeSlash renders path relative to base with forward slashes, a path outside
// base with `..` segments, falling back to path itself where no relative form
// exists.
func RelativeSlash(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// Resolve is the scope of one run, from the first source that names one: the
// scope document at document, when the invocation names one; otherwise the
// workspace that governs dir, when its file lists dir as a module; otherwise
// dir alone. Nothing else decides it: a named document gets exactly what it
// declares whatever workspace the filesystem holds, and a run with neither
// source loads the target alone rather than guessing at consumers.
func Resolve(ctx context.Context, dir, document string) (Document, error) {
	if document != "" {
		return read(ctx, document)
	}
	target, err := existingDir(dir)
	if err != nil {
		return Document{}, err
	}
	workspace, err := WorkspaceOf(ctx, target, target)
	if err != nil {
		return Document{}, err
	}
	if !workspace.Lists(target) {
		return ForDir(target)
	}
	// The module whose directory dir names is the target, a listed module below
	// it is a module of the target the load reads beside it, and every other
	// listed module is a consumer.
	doc := Document{Workspace: workspace.File, Target: Module{Path: target}}
	for _, path := range workspace.Modules {
		if path != target && !Below(target, path) {
			doc.Consumers = append(doc.Consumers, Module{Path: path})
		}
	}
	return doc, nil
}

// Workspace is the workspace file that governs a directory and the absolute
// path of every module it lists, in its order; File is empty where no file
// governs the directory. Replacements holds each of its replace directives that
// names a directory ([Replacements]).
type Workspace struct {
	File         string
	Modules      []string
	Replacements []Replacement
}

// Lists reports whether the workspace lists dir as one of its modules, which is
// when the toolchain run in dir builds through it.
func (w *Workspace) Lists(dir string) bool {
	return slices.Contains(w.Modules, filepath.Clean(dir))
}

// WorkspaceOf returns the workspace that governs dir: the file in the nearest
// directory at or above dir that holds one, the rule the toolchain applies,
// read from the filesystem so an ambient workspace setting cannot decide which
// file one target's run reads, its error naming that file relative to root. A
// cancelled ctx ends the read before it starts.
func WorkspaceOf(ctx context.Context, root, dir string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	file, found := workspaceFile(dir)
	if !found {
		return Workspace{}, nil
	}
	return ReadWorkspace(root, file)
}

// workspaceFile returns the workspace file that governs dir and reports
// whether one exists.
func workspaceFile(dir string) (string, bool) {
	for at := dir; ; {
		file := filepath.Join(at, workspaceFileName)
		if info, err := os.Stat(file); err == nil && !info.IsDir() {
			return file, true
		}
		parent := filepath.Dir(at)
		if parent == at {
			return "", false
		}
		at = parent
	}
}

// ReadWorkspace is the workspace file at file, every module it lists in its order,
// parsed from one reading of its bytes by the toolchain's own parser. A relative
// entry resolves against the file's own directory, as the toolchain resolves it. A
// file it cannot read or parse is the project's unreadable configuration, named
// relative to root.
func ReadWorkspace(root, file string) (Workspace, error) {
	content, err := ReadRegularFile(root, file)
	switch {
	case errors.Is(err, ErrIrregular):
		return Workspace{}, failure.WrapFix(vocab.SetupUnreadableConfiguration, fmt.Errorf("%w: %w", errWorkspace, err), IrregularFix)
	case err != nil:
		return Workspace{}, failure.Wrap(vocab.SetupUnreadableConfiguration, fmt.Errorf("%w: %w", errWorkspace, err))
	}
	work, err := modfile.ParseWork(RelativeSlash(root, file), content, nil)
	if err != nil {
		return Workspace{}, failure.Wrap(vocab.SetupUnreadableConfiguration, fmt.Errorf("%w: %w", errWorkspace, err))
	}
	base := filepath.Dir(file)
	w := Workspace{File: file, Replacements: Replacements(base, work.Replace)}
	used := make([]string, 0, len(work.Use))
	for _, use := range work.Use {
		// The toolchain lists no module for an entry naming no path.
		if use.Path == "" {
			continue
		}
		// The toolchain joins every entry filepath.IsAbs refuses to the file's
		// directory, a rooted entry on Windows included, so [absolute] is not its rule.
		dir := use.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		used = append(used, filepath.Clean(dir))
	}
	w.Modules = used
	return w, nil
}

// Replacement is one replace directive that names a directory: the module path it
// replaces, whatever version, and that directory, absolute.
type Replacement struct {
	Module string
	Dir    string
}

// Replacements is each of replaces that names a directory, whatever version it
// replaces, a relative one resolved against base as the toolchain resolves it, in
// their order.
func Replacements(base string, replaces []*modfile.Replace) []Replacement {
	var held []Replacement
	for _, r := range replaces {
		if !modfile.IsDirectoryPath(r.New.Path) {
			continue
		}
		dir := filepath.FromSlash(r.New.Path)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		held = append(held, Replacement{Module: r.Old.Path, Dir: filepath.Clean(dir)})
	}
	return held
}

// Below reports whether path is strictly under dir by its names, the way the
// toolchain places a workspace member or a package directory under a module.
func Below(dir, path string) bool {
	relative, inside := lexicallyWithin(dir, path)
	return inside && relative != "."
}

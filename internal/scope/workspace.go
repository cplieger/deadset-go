package scope

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/mod/modfile"
)

// workspaceFileName is the name of the file that lists the modules of a Go
// workspace.
const workspaceFileName = "go.work"

// errWorkspace reports a workspace file that cannot be read or parsed.
var errWorkspace = errors.New("scope: unreadable workspace")

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
	workspace, err := WorkspaceOf(ctx, target)
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
		if path != target && !below(target, path) {
			doc.Consumers = append(doc.Consumers, Module{Path: path})
		}
	}
	return doc, nil
}

// Workspace is the workspace file that governs a directory and the absolute
// path of every module it lists, in its order; File is empty where no file
// governs the directory.
type Workspace struct {
	File    string
	Modules []string
}

// Lists reports whether the workspace lists dir as one of its modules, which is
// when the toolchain run in dir builds through it.
func (w *Workspace) Lists(dir string) bool {
	return slices.Contains(w.Modules, filepath.Clean(dir))
}

// WorkspaceOf returns the workspace that governs dir: the file in the nearest
// directory at or above dir that holds one, the rule the toolchain applies,
// read from the filesystem so an ambient workspace setting cannot decide which
// file one target's run reads. A cancelled ctx ends the read before it starts.
func WorkspaceOf(ctx context.Context, dir string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	file, found := workspaceFile(dir)
	if !found {
		return Workspace{}, nil
	}
	modules, err := readWorkspace(file)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{File: file, Modules: modules}, nil
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

// readWorkspace returns the absolute path of every module the workspace file
// lists, in its order, parsed from one reading of its bytes by the toolchain's
// own parser. A relative entry resolves against the file's own directory, as
// the toolchain resolves it.
func readWorkspace(file string) ([]string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errWorkspace, file, err)
	}
	work, err := modfile.ParseWork(file, content, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errWorkspace, file, err)
	}
	base := filepath.Dir(file)
	used := make([]string, 0, len(work.Use))
	for _, use := range work.Use {
		// The toolchain lists no module for an entry naming no path.
		if use.Path == "" {
			continue
		}
		used = append(used, filepath.Clean(absolute(use.Path, base)))
	}
	return used, nil
}

// below reports whether path is a directory strictly under dir.
func below(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

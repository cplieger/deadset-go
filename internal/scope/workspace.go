package scope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// WorkspaceFileName is the name of the file that lists the modules of a Go
// workspace.
const WorkspaceFileName = "go.work"

// goCommand is the toolchain the workspace is read through.
const goCommand = "go"

// ErrWorkspace reports a workspace file the toolchain refused to read.
var ErrWorkspace = errors.New("scope: unreadable workspace")

// Resolve is the scope of one run, from the first source that names one:
//
//   - the scope document at document, when the invocation names one;
//   - otherwise the workspace that governs dir, when a workspace file lists dir
//     as one of its modules;
//   - otherwise dir alone, loading nothing else.
//
// The precedence is that order and nothing else decides it: an invocation that
// names a document gets exactly what the document declares, whatever workspace
// the filesystem holds, and a run with neither source loads the target alone
// rather than guessing at consumers. Cancelling ctx stops the toolchain the
// workspace is read through.
func Resolve(ctx context.Context, dir, document string) (Document, error) {
	if document != "" {
		return Read(document)
	}
	doc, found, err := forWorkspace(ctx, dir)
	if err != nil {
		return Document{}, err
	}
	if found {
		return doc, nil
	}
	return ForDir(dir)
}

// forWorkspace builds the scope from the workspace that governs dir: the module
// whose directory dir names is the target, and every other module the workspace
// lists is a consumer.
//
// It reports false when no workspace file governs dir, and when the workspace
// lists no module at dir, because a workspace the target is not a member of
// decides nothing about the target. The directory has to be a listed module
// itself: a directory below one is part of that module rather than the module the
// workspace names, and the analysis of a subtree declares its own scope.
func forWorkspace(ctx context.Context, dir string) (Document, bool, error) {
	target, err := existingDir(dir)
	if err != nil {
		return Document{}, false, err
	}
	file, found := workspaceFile(target)
	if !found {
		return Document{}, false, nil
	}

	used, err := readWorkspace(ctx, file)
	if err != nil {
		return Document{}, false, err
	}
	doc := Document{Workspace: file}
	for _, path := range used {
		if path == target {
			doc.Target = Module{Path: path}
			continue
		}
		doc.Consumers = append(doc.Consumers, Module{Path: path})
	}
	if doc.Target.Path == "" {
		return Document{}, false, nil
	}
	return doc, true, nil
}

// workspaceFile returns the workspace file that governs dir, which is the one in
// the nearest directory at or above dir that holds one, and reports whether such
// a file exists. This is the rule the toolchain itself applies, read from the
// filesystem rather than from the toolchain, so an ambient workspace setting
// cannot decide which file one target's run reads.
func workspaceFile(dir string) (string, bool) {
	for at := dir; ; {
		file := filepath.Join(at, WorkspaceFileName)
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
// lists, in the order it lists them.
//
// The toolchain prints the file as it reads it, which is the same rule applied to
// the workspace file that [deps] applies to a module file: the grammar of a
// toolchain file has one reader, and it is the toolchain. A relative entry
// resolves against the workspace file's own directory, which is what the
// toolchain resolves it against.
func readWorkspace(ctx context.Context, file string) ([]string, error) {
	printed, err := printWorkspace(ctx, file)
	if err != nil {
		return nil, err
	}

	// Unknown members are ignored rather than refused: the toolchain prints every
	// directive of the file and may gain one, and the use directives are all this
	// reads.
	var document workJSON
	if decoded := json.Unmarshal(printed, &document); decoded != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrWorkspace, file, decoded)
	}

	base := filepath.Dir(file)
	used := make([]string, 0, len(document.Use))
	for _, use := range document.Use {
		if use.DiskPath == "" {
			continue
		}
		path := use.DiskPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		used = append(used, filepath.Clean(path))
	}
	return used, nil
}

// printWorkspace runs the toolchain that prints the workspace file as it reads
// it. The file is named as an argument, so no setting of the environment decides
// which file is read, and the two settings pinned are pinned for the reason the
// load pins them: an ambient workspace or flag list reaching the child would make
// one target's answer depend on where the run was started.
func printWorkspace(ctx context.Context, file string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, goCommand, "work", "edit", "-json", file)
	cmd.Dir = filepath.Dir(file)
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")

	var printed, diagnostic bytes.Buffer
	cmd.Stdout = &printed
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s: %s: %w", ErrWorkspace, file, trimmed(&diagnostic), err)
	}
	return printed.Bytes(), nil
}

// trimmed renders what the toolchain wrote to its diagnostic stream as one line
// of a message, and says so when it wrote nothing.
func trimmed(diagnostic *bytes.Buffer) string {
	text := string(bytes.TrimSpace(diagnostic.Bytes()))
	if text == "" {
		return "no diagnostic"
	}
	return text
}

// workJSON mirrors the member of the printed workspace file this package reads.
type workJSON struct {
	Use []workUse
}

// workUse is one printed use directive. The toolchain prints the module path as
// well where the directive carries one, and nothing here reads it: the module a
// path holds is what the load reports, so a scope never states it twice.
type workUse struct {
	DiskPath string
}

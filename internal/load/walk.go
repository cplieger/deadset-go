package load

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// Kind is what one entry of a tree is to every walk of it, decided before anything
// there is opened, by the listing's type for an entry that is no symbolic link and
// by Stat for one that is: opening a pipe blocks, and reading a device never ends.
type Kind uint8

const (
	// File is a regular file, or a symbolic link to one.
	File Kind = iota + 1

	// directory is a directory a walk may enter. A symbolic link to one is leftOut
	// below the root of a walk, since the go command ignores such a link in a
	// pattern.
	directory

	// leftOut is an entry no build reads: a pipe, a device, a socket, a symbolic
	// link to a directory, or one to nothing, through a file or that loops.
	leftOut

	// Unread is an entry Stat fails on otherwise, or a directory whose listing
	// fails: one that may hold code the analysis does not see.
	Unread
)

// KindOf is the kind of the entry at path whose type a directory listing gave as
// mode, running stat on it only where it is a symbolic link.
func KindOf(path string, mode fs.FileMode, stat func(string) (fs.FileInfo, error)) Kind {
	if mode&fs.ModeSymlink == 0 {
		return kindOfType(mode)
	}
	kind := statKind(stat(path))
	if kind == directory {
		return leftOut
	}
	return kind
}

// statKind is the kind of an entry whose Stat, following links, answered info and
// err.
func statKind(info fs.FileInfo, err error) Kind {
	switch {
	case err == nil:
		return kindOfType(info.Mode().Type())
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR), errors.Is(err, errLinkLoop):
		return leftOut
	default:
		return Unread
	}
}

// kindOfType is the kind of an entry of type mode that is no symbolic link.
func kindOfType(mode fs.FileMode) Kind {
	switch {
	case mode.IsRegular():
		return File
	case mode.IsDir():
		return directory
	default:
		return leftOut
	}
}

// Walk walks the tree at root as the go command walks a module's tree, handing
// visit each entry once with its [Kind], a directory before what it holds. A
// directory below root that enters rejects is neither visited nor entered. It
// follows root where root is a symbolic link to a directory (go.dev/issue/50807);
// visit sees root as written and every path below joined onto it, the spelling the
// toolchain reports.
// An Unread or leftOut entry at or below top's root is recorded in
// [Topology.Unread] or [Topology.LeftOut]. visit's fs.SkipAll ends the walk, and
// any other error ends it with that error.
func (top *Topology) Walk(root string, enters func(dir string) bool, visit func(path string, kind Kind) error) error {
	err := top.walk(root, statKind(os.Stat(root)), enters, visit)
	if errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

// walk hands visit the entry at path of kind, then walks what it holds.
func (top *Topology) walk(path string, kind Kind, enters func(string) bool, visit func(string, Kind) error) error {
	var entries []fs.DirEntry
	if kind == directory {
		var err error
		if entries, err = os.ReadDir(path); err != nil {
			kind = Unread
		}
	}
	top.record(path, kind)
	if err := visit(path, kind); err != nil || kind != directory {
		return err
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		childKind := KindOf(child, entry.Type(), os.Stat)
		if childKind == directory && !enters(child) {
			continue
		}
		if err := top.walk(child, childKind, enters, visit); err != nil {
			return err
		}
	}
	return nil
}

// record keeps an Unread or a leftOut entry at or below the target root, once.
func (top *Topology) record(path string, kind Kind) {
	switch kind {
	case Unread:
		top.AddUnread(path)
	case leftOut:
		if top.holds(path) && !slices.Contains(top.LeftOut, path) {
			top.LeftOut = append(top.LeftOut, path)
		}
	}
}

// AddUnread records one more path at or below the target root the analysis cannot
// read.
func (top *Topology) AddUnread(path string) {
	if top.holds(path) && !slices.Contains(top.Unread, path) {
		top.Unread = append(top.Unread, path)
	}
}

// holds reports whether path is the target root or lies below it.
func (top *Topology) holds(path string) bool {
	return sameDir(top.Root, path) || scope.Below(top.Root, path)
}

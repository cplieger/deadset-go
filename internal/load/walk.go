package load

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
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

	// leftOut is an entry no walk opens: a pipe, a device, a socket, a symbolic
	// link to a directory, or one to nothing, through a file or that loops.
	leftOut

	// Unread is an entry Stat fails on otherwise, or a directory whose listing
	// fails: one that may hold code the analysis does not see.
	Unread

	// unnamed is a symbolic link to a name too long for any caller to look up:
	// no build reaches a directory through it, and a walk of the tree records it
	// as [Unread] ([Kind.recorded]).
	unnamed
)

// recorded is the kind a walk of the tree records k as: [unnamed] is [Unread].
func (k Kind) recorded() Kind {
	if k == unnamed {
		return Unread
	}
	return k
}

// KindOf is the kind of the entry at path whose type a directory listing gave as
// mode, running stat on it only where it is a symbolic link.
func KindOf(path string, mode fs.FileMode, stat func(string) (fs.FileInfo, error)) Kind {
	if mode&fs.ModeSymlink == 0 {
		return kindOfType(mode)
	}
	kind := statKind(stat(path)).recorded()
	if kind == directory {
		return leftOut
	}
	return kind
}

// statKind is the kind of an entry whose Stat, following links, answered info and
// err: a path to nothing, through a file or that loops is no entry, one whose name
// is too long for any caller to look up is [unnamed], and any other error is an
// entry the run cannot read ([program.4]).
func statKind(info fs.FileInfo, err error) Kind {
	switch {
	case err == nil:
		return kindOfType(info.Mode().Type())
	case errors.Is(err, syscall.ENAMETOOLONG):
		return unnamed
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
// visit each entry once with its [Kind], a directory before what it holds, and
// entering no directory below root that enters rejects. It follows root where it
// is a link to a directory (go.dev/issue/50807), joining every path onto root as
// written, the spelling the toolchain reports. An Unread or leftOut entry at or
// below top's root is recorded in [Topology.Unread] or [Topology.LeftOut].
// visit's fs.SkipAll ends the walk, and any other error ends it with that error.
func (top *Topology) Walk(root string, enters func(dir string) bool, visit func(path string, kind Kind) error) error {
	err := top.walk(root, statKind(os.Stat(root)).recorded(), enters, visit)
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
		if top.Holds(path) && !top.recorded(top.LeftOut, path) {
			top.LeftOut = append(top.LeftOut, path)
		}
	}
}

// AddUnread records one more path at or below the target root the analysis cannot
// read.
func (top *Topology) AddUnread(path string) {
	if top.Holds(path) && !top.recorded(top.Unread, path) {
		top.Unread = append(top.Unread, path)
	}
}

// recordedUnread reports whether [Topology.Unread] names the entry at path.
func (top *Topology) recordedUnread(path string) bool { return top.recorded(top.Unread, path) }

// recorded reports whether held names the entry at path where both resolve
// ([Topology.Rel]): the matrix's walk of a directory the toolchain names reaches an
// entry the target's walk recorded, under the toolchain's spelling.
func (top *Topology) recorded(held []string, path string) bool {
	entry := top.Rel(path)
	return slices.ContainsFunc(held, func(recorded string) bool { return top.Rel(recorded) == entry })
}

// Holds reports whether the entry at path is the target root or lies below it,
// where both directories resolve ([scope.Root.PlaceEntry]).
func (top *Topology) Holds(path string) bool {
	_, inside := top.root.PlaceEntry(path)
	return inside
}

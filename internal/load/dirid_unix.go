//go:build unix

package load

import (
	"io/fs"
	"os"
	"syscall"
)

// deviceOf is the device holding the entry at path, links followed, nil where Stat
// fails.
func deviceOf(path string) any {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Dev
	}
	return nil
}

// idOf is the dirID of the directory at path, whose Stat, following links, gave info.
func idOf(path string, info fs.FileInfo) dirID {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return dirID{dev: st.Dev, ino: st.Ino}
	}
	return resolvedID(path)
}

// listedLink is false: a listing here gives a link as a link.
func listedLink(string) bool { return false }

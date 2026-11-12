//go:build unix

package load

import (
	"io/fs"
	"syscall"
)

// idOf is the dirID of the directory at path, whose Stat, following links, gave info.
func idOf(path string, info fs.FileInfo) dirID {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return dirID{dev: st.Dev, ino: st.Ino}
	}
	return resolvedID(path)
}

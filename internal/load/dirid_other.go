//go:build !unix && !windows

package load

import "io/fs"

// idOf is the dirID of the directory at path, by its resolved path: the platform
// reports no device and inode.
func idOf(path string, _ fs.FileInfo) dirID { return resolvedID(path) }

//go:build !unix && !windows

package load

import "io/fs"

// idOf is the dirID of the directory at path, by its resolved path: the platform
// reports no device and inode.
func idOf(path string, _ fs.FileInfo) dirID { return resolvedID(path) }

// deviceOf is nil: this platform names no device, so every entry shares one.
func deviceOf(string) any { return nil }

// listedLink is false: a listing here gives a link as a link.
func listedLink(string) bool { return false }

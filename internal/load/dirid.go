package load

import "path/filepath"

// dirID is one directory however a path spells it: its device and inode, or its
// file index on Windows, else, where the platform reports neither, its path with
// every symbolic link resolved.
type dirID struct {
	dev, ino any
	path     string
}

// resolvedID is the dirID of the directory at path by its resolved path.
func resolvedID(path string) dirID {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return dirID{path: resolved}
	}
	return dirID{path: filepath.Clean(path)}
}

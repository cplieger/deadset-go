package matrix

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// goSuffix is the extension of the files derivation reads.
const goSuffix = ".go"

// moduleFile is the file that makes a directory the root of its own module.
const moduleFile = "go.mod"

// The directories the toolchain builds nothing from.
var unbuiltDirs = []string{"testdata", "vendor"}

// collect reads the build constraint of every Go file in the module tree rooted
// at root, in the order the walk meets them.
//
// The walk is the toolchain's own reading of a directory rather than a loaded
// package set, which is what lets it see a directory whose every file one
// configuration excludes: such a directory holds no package for that
// configuration and a load reports nothing at all for it.
func collect(root string) ([]fileConstraint, error) {
	var files []fileConstraint
	walk := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (unbuilt(entry.Name()) || startsAnotherModule(path)) {
				return fs.SkipDir
			}
			return nil
		}
		if !isGoFile(entry.Name()) {
			return nil
		}
		file, err := readConstraint(root, path)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, fmt.Errorf("read the build constraints under %s: %w", root, err)
	}
	return files, nil
}

// unbuilt reports whether a directory of that name holds nothing the toolchain
// builds: one it reserves for test inputs or vendored sources, or one whose name
// opens with a character that keeps every path under it out of a package.
func unbuilt(name string) bool {
	if name == "" {
		return true
	}
	return name[0] == '.' || name[0] == '_' || slices.Contains(unbuiltDirs, name)
}

// startsAnotherModule reports whether the directory at path declares a module of
// its own, whose files belong to that module rather than to the target.
func startsAnotherModule(path string) bool {
	_, err := os.Stat(filepath.Join(path, moduleFile))
	return err == nil
}

// isGoFile reports whether a file of that name is a Go source file the toolchain
// reads. A name opening with an underscore or a full stop is one it ignores.
func isGoFile(name string) bool {
	stem, ok := strings.CutSuffix(name, goSuffix)
	return ok && stem != "" && stem[0] != '_' && stem[0] != '.'
}

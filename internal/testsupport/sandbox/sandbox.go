// Package sandbox is the one way a test of this module makes, changes or links a
// file: every call resolves its target, links followed, and refuses one outside a
// temporary root a test made before it touches anything. A pipe left in a shared
// directory the toolchain reads, GOROOT or the module cache, blocks every go
// command on the host, so no computed path may reach one.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ErrOutside reports a target outside every temporary root.
var ErrOutside = errors.New("sandbox: refused a path outside every temporary root")

// roots holds the resolved path of every temporary root: a [Dir] until its test ends,
// a [MkdirTemp] one until the process ends.
var roots struct {
	held []string
	mu   sync.Mutex
}

// Dir is a new temporary directory of t ([testing.TB.TempDir]), a root every
// function of this package writes inside until t ends.
func Dir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	release, err := hold(dir)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(release)
	return dir
}

// MkdirTemp is [os.MkdirTemp] inside a root. With dir empty it makes a new
// temporary directory, itself a root until the process ends.
func MkdirTemp(dir, pattern string) (string, error) {
	if dir != "" {
		if err := inside(dir, true); err != nil {
			return "", err
		}
		return os.MkdirTemp(dir, pattern)
	}
	made, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := hold(made); err != nil {
		_ = os.Remove(made)
		return "", err
	}
	return made, nil
}

// WriteFile is [os.WriteFile] inside a root.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := inside(path, true); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

// Mkdir is [os.Mkdir] inside a root.
func Mkdir(path string, perm fs.FileMode) error {
	if err := inside(path, false); err != nil {
		return err
	}
	return os.Mkdir(path, perm)
}

// MkdirAll is [os.MkdirAll] inside a root.
func MkdirAll(path string, perm fs.FileMode) error {
	if err := inside(path, true); err != nil {
		return err
	}
	return os.MkdirAll(path, perm)
}

// Symlink is [os.Symlink] making link inside a root; target may name anything.
func Symlink(target, link string) error {
	if err := inside(link, false); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

// Chmod is [os.Chmod] inside a root.
func Chmod(path string, mode fs.FileMode) error {
	if err := inside(path, true); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// Rename is [os.Rename] from and to paths inside a root.
func Rename(from, to string) error {
	if err := inside(from, false); err != nil {
		return err
	}
	if err := inside(to, false); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// Create is [os.Create] inside a root.
func Create(path string) (*os.File, error) {
	if err := inside(path, true); err != nil {
		return nil, err
	}
	return os.Create(path)
}

// OpenFile is [os.OpenFile] inside a root.
func OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error) {
	if err := inside(path, true); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flag, perm)
}

// WriteGolden is [os.WriteFile] of a golden file inside the module holding the
// working directory, which a test rewrites only when asked to.
func WriteGolden(path string, data []byte, perm fs.FileMode) error {
	module, err := moduleRoot()
	if err != nil {
		return err
	}
	resolved, err := resolve(path, true)
	if err != nil {
		return err
	}
	if !within(module, resolved) {
		return fmt.Errorf("%w: %s is outside the module %s", ErrOutside, path, module)
	}
	return os.WriteFile(path, data, perm)
}

// hold makes dir a root, and returns the function that ends that.
func hold(dir string) (func(), error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve the root %s: %w", dir, err)
	}
	roots.mu.Lock()
	roots.held = append(roots.held, resolved)
	roots.mu.Unlock()
	return func() { release(resolved) }, nil
}

// release ends the root at resolved, once.
func release(resolved string) {
	roots.mu.Lock()
	defer roots.mu.Unlock()
	if i := slices.Index(roots.held, resolved); i >= 0 {
		roots.held = slices.Delete(roots.held, i, i+1)
	}
}

// inside is nil where path, resolved by [resolve], is a root or lies below one,
// and an error wrapping [ErrOutside] otherwise.
func inside(path string, follow bool) error {
	resolved, err := resolve(path, follow)
	if err != nil {
		return err
	}
	roots.mu.Lock()
	defer roots.mu.Unlock()
	if slices.ContainsFunc(roots.held, func(root string) bool { return within(root, resolved) }) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrOutside, path)
}

// resolve is path made absolute with every link above it resolved, and the link it
// names itself where follow is set: the deepest existing directory above it is
// resolved and the names below joined back on. A path holding a `..` element is
// refused: the kernel resolves the links before it first, which a lexical clean
// would not, so it may name a place outside the one checked.
func resolve(path string, follow bool) (string, error) {
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return "", fmt.Errorf("%w: %s holds a .. element", ErrOutside, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	if follow {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved, nil
		}
	}
	rest := filepath.Base(abs)
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if filepath.Dir(dir) == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// within reports whether path is root or lies below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// moduleRoot is the resolved directory of the module file nearest above the working
// directory.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	for at := dir; ; at = filepath.Dir(at) {
		if info, err := os.Stat(filepath.Join(at, "go.mod")); err == nil && info.Mode().IsRegular() {
			return filepath.EvalSymlinks(at)
		}
		if filepath.Dir(at) == at {
			return "", fmt.Errorf("%w: no module holds %s", ErrOutside, dir)
		}
	}
}

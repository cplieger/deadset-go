package scope

import (
	"slices"
	"strings"
)

// A path the run gave one spelling compares with another by its names: a name
// [Root.Name] gives, which every file set, listing and position holds, a path
// relative to a root that [Root.Place], [Root.PlaceEntry] or [Root.Rel] gives, and
// a path a walk joins onto the directory it starts at, compared with that
// directory or another path of the same walk. Order, SameName and Listed compare
// such paths. A path spelled by another source, the toolchain, the environment or a
// document, is compared through [Same], [SameEntry] or a [Root] instead.

// Order orders two paths the run gave one spelling by their names, the order a
// report lists them in, and is 0 for one entry.
func Order(a, b string) int { return strings.Compare(a, b) }

// SameName reports whether two paths the run gave one spelling name one entry.
func SameName(a, b string) bool { return a == b }

// Listed reports whether paths holds path, all of them given one spelling by the
// run ([SameName]).
func Listed(paths []string, path string) bool { return slices.Contains(paths, path) }

// Below reports whether the directory at path is strictly below the root, where
// both resolve ([Root.Place]): of two roots that both hold a file, the one below
// the other is the nearer, however either is spelled.
func (r Root) Below(path string) bool {
	relative, inside := r.Place(path)
	return inside && relative != "."
}

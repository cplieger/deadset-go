package main

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/graph"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

const printRootsUsage = "usage: deadset-go print-roots [--target=DIR] [--config=FILE] [--central=FILE] [--min-confidence=CLASS]"

// printRoots writes the root set of the matrix to stdout, one root per line: the
// symbol's reference, why it is a root, and the configured string that named it,
// separated by tabs, plus the configurations that detected it where the matrix holds
// more than one, so sort and cut read it.
func printRoots(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	req, code := resolve(ctx, "print-roots", printRootsUsage, args, stderr)
	if code != vocab.ExitClean {
		return code
	}
	loaded, err := run.Load(ctx, req)
	if err != nil {
		return failed(ctx, stderr, err)
	}
	namedUnbuilt(stderr, loaded.Unbuilt)
	return printLines(stdout, stderr, "print-roots", rootLines(loaded.Refs, loaded.Merged.Roots, loaded.Identifiers), loaded.Unmatched)
}

// rootLines renders one line per root the matrix holds, in the merge's order. One
// root several configurations detect is one line keyed by the symbol, the class and
// the configured string; the configurations are a further field only where the
// matrix holds more than one, with the third field then present even when empty.
func rootLines(refs map[graph.SymbolID]string, roots []graph.Root, configurations []string) []string {
	at := make(map[rootKey]int, len(roots))
	lines := make([]string, 0, len(roots))
	detected := make([][]string, 0, len(roots))
	for _, root := range roots {
		key := rootKey{id: root.ID, source: root.Source, kind: root.Kind}
		held, seen := at[key]
		if !seen {
			held = len(lines)
			at[key] = held
			lines = append(lines, rootLine(refs[root.ID], root, len(configurations) > 1))
			detected = append(detected, nil)
		}
		detected[held] = append(detected[held], configurationName(configurations, root.Config))
	}
	if len(configurations) < 2 {
		return lines
	}
	for i := range lines {
		lines[i] += "\t" + strings.Join(detected[i], " ")
	}
	return lines
}

// rootKey is what makes two roots of a matrix one root.
type rootKey struct {
	id     graph.SymbolID
	source string
	kind   graph.RootKind
}

// rootLine renders one root. The configured string is left off a detected class,
// which no configured string named, unless a field follows it.
func rootLine(ref string, root graph.Root, sourceField bool) string {
	line := ref + "\t" + root.Kind.String()
	if root.Source == "" && !sourceField {
		return line
	}
	return line + "\t" + root.Source
}

// configurationName names one configuration of the matrix, and one outside it by its
// place rather than by an empty field.
func configurationName(configurations []string, at int) string {
	if at < 0 || at >= len(configurations) {
		return strconv.Itoa(at)
	}
	return configurations[at]
}

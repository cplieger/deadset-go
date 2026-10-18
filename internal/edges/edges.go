// Package edges reads the declared cross-language edges of one target: the pairs
// that say one symbol exists because a symbol in another language uses it.
//
// An edge has two sides, and an analyzer reads only the sides that carry its own
// language. It never resolves the paired symbol, because it holds no type
// information for the other language and acquires none, so the document this
// package returns is the declaration and nothing more: what an analyzer makes of
// its own side is the findings pass's answer, and what the pair means is the
// merge's.
package edges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/deadset-go/v2/internal/failure"
	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// ErrMalformed reports an edges document the Contract refuses, which ends the
// run with the usage code before any analysis.
var ErrMalformed = errors.New("edges: the edges document is refused")

// Side names which half of an edge a reference is. The spelling is the one a
// report carries.
type Side string

// The two sides of every edge. An edge has exactly these and no third: provides
// exists because used_by uses it.
const (
	provides Side = "provides"
	usedBy   Side = "used_by"
)

// Edge is one declared pair: its identifier and the symbol each side names. Why
// it was declared is for whoever reads the document next, and the analysis does
// not read it.
type Edge struct {
	ID       string // the identifier every evaluation of this edge names
	Provides string // the reference of the symbol the other side uses
	UsedBy   string // the reference of the symbol that uses it
}

// Document is the declared edges of one target, in identifier order.
type Document struct {
	Edges []Edge
}

// Reference is one side of one edge: which edge, which side, and the symbol that
// side names.
type Reference struct {
	Edge   string
	Side   Side
	Symbol string
}

const schemaPath = "contract/edges.schema.json"

var edgesSchema = sync.OnceValues(func() (*document.Schema, error) {
	return document.Compile(spec.Contract, schemaPath)
})

// wireDocument is the members of an edges document the analysis reads.
type wireDocument struct {
	Edges map[string]struct {
		Provides string `json:"provides"`
		UsedBy   string `json:"used_by"`
	} `json:"edges"`
}

// Read returns the edges the document at the root of the target dir declares,
// and an empty document where there is none. A document the Contract's edges
// schema refuses, one that is not strict JSON and one over the bound of a
// configuration document wrap [ErrMalformed]; a name resolving outside dir or
// to anything but a regular file is an error that does not.
func Read(ctx context.Context, dir string) (*Document, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("edges: open the target root %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()
	body, err := atomicfile.ReadBoundedInRoot(ctx, root, vocab.EdgesFileName, vocab.DocumentMaxBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Document{}, nil
	case errors.Is(err, atomicfile.ErrFileTooLarge):
		return nil, fmt.Errorf("%w: %s: %w", ErrMalformed, vocab.EdgesFileName, err)
	case err != nil:
		return nil, failure.Wrap(vocab.SetupUnreadableDocument, fmt.Errorf("%s cannot be read: %w", vocab.EdgesFileName, err))
	}
	schema, err := edgesSchema()
	if err != nil {
		return nil, err
	}
	var wire wireDocument
	if err := jsondoc.Unmarshal(schema, vocab.EdgesFileName, body, &wire); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	held := &Document{Edges: make([]Edge, 0, len(wire.Edges))}
	for _, id := range slices.Sorted(maps.Keys(wire.Edges)) {
		held.Edges = append(held.Edges, Edge{ID: id, Provides: wire.Edges[id].Provides, UsedBy: wire.Edges[id].UsedBy})
	}
	return held, nil
}

// Own returns every side of every declared edge whose symbol reference carries
// language's own tag, in identifier order, the provides side of an edge before its
// used_by side. Both sides of one edge are returned where both name that language.
//
// It answers what an analyzer of that language evaluates. A side of another
// language is not returned at all, because a side no analyzer evaluated has no
// record in a report and is not absent: nothing looked.
func (d *Document) Own(language string) []Reference {
	if d == nil {
		return nil
	}
	tag := language + "://"
	own := make([]Reference, 0, 2*len(d.Edges))
	for i := range d.Edges {
		edge := &d.Edges[i]
		for _, side := range [...]struct {
			side   Side
			symbol string
		}{{provides, edge.Provides}, {usedBy, edge.UsedBy}} {
			if strings.HasPrefix(side.symbol, tag) {
				own = append(own, Reference{Edge: edge.ID, Side: side.side, Symbol: side.symbol})
			}
		}
	}
	return own
}

package testsupport

import (
	"context"

	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// Topology is the topology of doc, ending the test through sink where it cannot
// be read.
func Topology(ctx context.Context, sink Sink, doc scope.Document) *load.Topology {
	sink.Helper()

	top, err := load.Discover(ctx, doc)
	if err != nil {
		sink.Fatalf("Setup: load.Discover(%s): %v", doc.Target.Path, err)
	}
	return top
}

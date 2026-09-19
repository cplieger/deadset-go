package report

import (
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// fixtureGoldens are the fixtures a text and a JSON rendering are committed for: one
// per family of the findings this analyzer answers, so a rendering is pinned against
// the findings a real pass over real source produces rather than against a hand-built
// finding alone.
var fixtureGoldens = []struct {
	archive string
	name    string
	kind    config.TargetKind
}{
	{"declarations-unused.txtar", "declarations-unused", config.Library},
	{"narrowing-published.txtar", "narrowing-published", config.Library},
	{"interfaces-unused.txtar", "interfaces-unused", config.Application},
	{"readwrite-writeonly.txtar", "readwrite-writeonly", config.Application},
}

// TestRenderingsOfAFullEnvelope pins the four renderings of an envelope carrying
// every member the Contract declares, which is where a change to any one of them
// shows as a diff a reader reviews.
func TestRenderingsOfAFullEnvelope(t *testing.T) {
	in := fullInput()
	envelope := built(t, &in)
	opts := Options{Read: lines()}

	for _, one := range renderings() {
		t.Run(one.name, func(t *testing.T) {
			checkGolden(t, "full."+one.name, rendered(t, one.name, &envelope, opts))
		})
	}
}

// TestRenderingsOfTheFixtures pins the text and the JSON rendering of the findings a
// real pass answers over the fixtures of the findings package.
//
// A rendering here moves when the findings move, so a change to a kind is a diff in
// these goldens rather than a silent difference in what a report says.
func TestRenderingsOfTheFixtures(t *testing.T) {
	for _, fixture := range fixtureGoldens {
		t.Run(fixture.name, func(t *testing.T) {
			envelope, opts := envelopeOfDir(t, extract(t, fixture.archive), fixture.kind)
			if len(envelope.Findings) == 0 {
				t.Fatalf("the pass over %s reports no finding, so the renderings pin nothing", fixture.archive)
			}
			for _, name := range []string{"text", "json"} {
				checkGolden(t, fixture.name+"."+name, rendered(t, name, &envelope, opts))
			}
		})
	}
}

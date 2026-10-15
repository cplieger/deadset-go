package exempt

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// A method an embedded field supplies retains that field, and each field it is
// promoted through, beside the method; an embedded field supplying no method the
// interface requires is not retained.
func TestInterfaceSatisfactionRetainsTheEmbeddedFieldsThatSupplyAMethod(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "interface-satisfaction-embedded.txtar", Options{}), vocab.InterfaceSatisfaction)

	var got []string
	for _, ref := range refs {
		if strings.Contains(ref, "#Decoder.") || strings.Contains(ref, "#Base.") {
			got = append(got, ref)
		}
	}
	want := []string{
		"go://example.com/embedded#Base.Closer",
		"go://example.com/embedded#Base.Request",
		"go://example.com/embedded#Decoder.Base",
		"go://example.com/embedded#Decoder.Response",
	}
	if !slices.Equal(got, want) {
		t.Errorf("InterfaceSatisfactionDetector(interface-satisfaction-embedded.txtar) retained %v, want %v", got, want)
	}
}

// In a library, an embedded field supplying a method of a published parameter's
// interface is retained for the conversion an outside program writes.
func TestInterfaceSatisfactionRetainsAnEmbeddedFieldForAPublishedParameter(t *testing.T) {
	refs := retainedRefs(t, analysisOf(t, "interface-satisfaction-embedded-published.txtar", Options{PublishedAPI: true}), vocab.InterfaceSatisfaction)

	if !slices.Contains(refs, "go://example.com/lib/addon#Decoder.Base") {
		t.Errorf("InterfaceSatisfactionDetector(interface-satisfaction-embedded-published.txtar) retained %v, want Decoder.Base among them", refs)
	}
}

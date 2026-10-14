package kinds

import (
	"slices"
	"testing"
)

// reportedNames is the name of every declaration a pass reports, whatever its code.
func reportedNames(findings []Finding) []string {
	names := make([]string, 0, len(findings))
	for i := range findings {
		names = append(names, findings[i].Symbol.Name)
	}
	return names
}

// A conversion to an interface declared outside the program retains the methods that
// interface requires and no other, and a conversion inside a dead method retains
// nothing.
func TestAnInterfaceConversionRetainsOnlyTheInterfacesMethodsFromLiveCode(t *testing.T) {
	result := computed(t, inputOf(t, "retention-interface-conversion.txtar", applicationConfig(), Consumers{}), packageEmitters())
	reported := reportedNames(result.Findings)

	for _, name := range []string{"Items.Unrelated", "Beats.Len", "Beats.Less", "Beats.Swap", "Beats.Sorted"} {
		if !slices.Contains(reported, name) {
			t.Errorf("the pass does not report %s: it reports %v", name, summary(result.Findings))
		}
	}
	for _, name := range []string{"Items.Len", "Items.Less", "Items.Swap"} {
		if slices.Contains(reported, name) {
			t.Errorf("the pass reports %s, which sorting calls: it reports %v", name, summary(result.Findings))
		}
	}
}

package exempt

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
)

func TestErrorsDuckTypingRetainsTheHelperFormsOnATypeReachableAsAnError(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		want    []string
	}{
		{
			name:    "three_types_returned_into_an_error_result",
			archive: "errors-duck-converted.txtar",
			want: []string{
				"(*fault).Is\timplements Is(error) bool",
				"(*fault).As\timplements As(any) bool",
				"(*fault).Unwrap\timplements Unwrap() error",
				"(*joined).Unwrap\timplements Unwrap() []error",
			},
		},
		{
			name:    "the_same_methods_with_no_value_reaching_an_error_position",
			archive: "errors-duck-unconverted.txtar",
			want:    []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shared := analysisOf(t, test.archive, Options{})
			symbols := shared.inventory(t)
			found, err := shared.detect(t, ErrorsDuckTyping)
			if err != nil {
				t.Fatalf("ErrorsDuckTypingDetector(%s) error: %v", test.archive, err)
			}
			got := exemptionRows(t, symbols, ErrorsDuckTyping, found)
			if !slices.Equal(got, test.want) {
				t.Errorf("ErrorsDuckTypingDetector(%s) retained\n%v\nwant\n%v", test.archive, got, test.want)
			}
		})
	}
}

// A production run holds what a source file's conversion retains although a test
// file converts the same type at a site that sorts first, because a test file's
// site retains nothing in that run.
func TestErrorsDuckTypingHoldsTheSourceFilesSiteUnderAProductionRun(t *testing.T) {
	shared := analysisOf(t, "errors-duck-test-site.txtar", Options{})
	held, err := shared.compute(t, true)
	if err != nil {
		t.Fatalf("Compute(errors-duck-test-site.txtar, production) error: %v", err)
	}

	held = slices.DeleteFunc(held, func(e graph.Exemption) bool { return e.Class != string(ErrorsDuckTyping) })
	got := exemptionRows(t, shared.inventory(t), ErrorsDuckTyping, held)
	if want := []string{"(*fault).Is\timplements Is(error) bool"}; !slices.Equal(got, want) {
		t.Errorf("Compute(errors-duck-test-site.txtar, production) holds %v under %s, want %v", got, ErrorsDuckTyping, want)
	}
}

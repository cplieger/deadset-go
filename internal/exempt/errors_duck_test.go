package exempt

import (
	"slices"
	"testing"
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
			in := inputOf(t, test.archive, Options{})
			found, err := ErrorsDuckTypingDetector(in)
			if err != nil {
				t.Fatalf("ErrorsDuckTypingDetector(%s) error: %v", test.archive, err)
			}
			got := exemptionRows(t, in, ErrorsDuckTyping, found)
			if !slices.Equal(got, test.want) {
				t.Errorf("ErrorsDuckTypingDetector(%s) retained\n%v\nwant\n%v", test.archive, got, test.want)
			}
		})
	}
}

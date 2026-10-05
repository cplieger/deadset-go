package graph

import "testing"

func TestReferencesReadTheFieldsATypeParameterIsLaidOutAs(t *testing.T) {
	a := analyze(t, "layout.txtar")

	cases := []struct {
		name string
		from string
		to   string
		want int
	}{
		{name: "an_instantiation_of_the_converting_function", from: "addr", to: "direct.width", want: 1},
		{name: "an_instantiation_through_another_generic_function", from: "addr", to: "relayed.depth", want: 1},
		{name: "an_instantiation_of_the_type_a_converting_method_receives", from: "(*box).at", to: "boxed.flag", want: 1},
		{name: "a_struct_no_conversion_reaches", from: "addr", to: "unreached.hidden", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.count(tc.from, tc.to, RefRead); got != tc.want {
				t.Errorf("References(layout.txtar) holds %d read references from %s to %s, want %d",
					got, tc.from, tc.to, tc.want)
			}
		})
	}
}

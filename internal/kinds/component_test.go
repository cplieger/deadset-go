package kinds

import (
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/liveness"
)

func TestDeletableLinesCountALineSeveralSpansCoverOnce(t *testing.T) {
	cases := map[string]struct {
		spans []liveness.Span
		want  int
	}{
		"no span": {want: 0},
		"one span": {
			spans: []liveness.Span{{Path: "a.go", First: 3, Last: 5}},
			want:  3,
		},
		"a span inside another": {
			spans: []liveness.Span{{Path: "a.go", First: 3, Last: 9}, {Path: "a.go", First: 4, Last: 6}},
			want:  7,
		},
		"two spans that overlap": {
			spans: []liveness.Span{{Path: "a.go", First: 5, Last: 8}, {Path: "a.go", First: 3, Last: 6}},
			want:  6,
		},
		"two spans that meet without sharing a line": {
			spans: []liveness.Span{{Path: "a.go", First: 3, Last: 4}, {Path: "a.go", First: 5, Last: 6}},
			want:  4,
		},
		"one span twice": {
			spans: []liveness.Span{{Path: "a.go", First: 7, Last: 7}, {Path: "a.go", First: 7, Last: 7}},
			want:  1,
		},
		"the same lines of two files": {
			spans: []liveness.Span{{Path: "a.go", First: 3, Last: 5}, {Path: "b.go", First: 3, Last: 5}},
			want:  6,
		},
		"a span that ends before the run it follows": {
			spans: []liveness.Span{
				{Path: "a.go", First: 1, Last: 10},
				{Path: "a.go", First: 2, Last: 3},
				{Path: "a.go", First: 9, Last: 12},
			},
			want: 12,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DeletableLines(&liveness.Component{Spans: test.spans}); got != test.want {
				t.Errorf("DeletableLines(%+v) = %d, want %d", test.spans, got, test.want)
			}
		})
	}
}

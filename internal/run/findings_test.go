package run

import (
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/config"
	"github.com/cplieger/deadset-go/v2/internal/kinds"
	"github.com/cplieger/deadset-go/v2/internal/load"
)

func TestConsumersNamesWhatTheRunLoaded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		complete bool
		loads    []enumerated
		want     kinds.Consumers
	}{
		{
			name: "a_run_that_loaded_no_consumer",
			want: kinds.Consumers{},
		},
		{
			name:     "a_run_whose_configuration_declares_the_set_complete",
			complete: true,
			loads: []enumerated{{Result: load.Result{Consumers: []load.Consumer{
				{ID: "example.com/one"}, {ID: "example.com/two"},
			}}}},
			want: kinds.Consumers{
				Declared: []string{"example.com/one", "example.com/two"},
				Loaded:   []string{"example.com/one", "example.com/two"},
				Complete: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.Consumers.Complete = tc.complete
			got := (&Loaded{req: &Request{Config: cfg}, Loads: tc.loads}).consumers()
			if !slices.Equal(got.Declared, tc.want.Declared) || !slices.Equal(got.Loaded, tc.want.Loaded) ||
				got.Complete != tc.want.Complete {
				t.Errorf("consumers() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

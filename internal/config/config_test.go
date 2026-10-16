package config_test

import (
	"reflect"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestResolutionAppliesEveryDefaultTheTableDeclaresForAKeyTheGoAnalysisReads(t *testing.T) {
	t.Parallel()

	_, document := printedDocument(t, resolved(t, fromRepository(`{"target": {"kind": "library"}}`)))
	checked := 0
	for _, row := range vocab.ConfigKeys() {
		if !row.ReadIn(vocab.Go) || row.Default == "" || row.Role != vocab.RoleSetting {
			continue
		}
		checked++
		got, held := valueAt(document, row.Key)
		if want := decoded(t, row.Default); !held || !reflect.DeepEqual(got, want) {
			t.Errorf("the resolved %s = %#v (held %t), want its default %s", row.Key, got, held, row.Default)
		}
	}
	if checked == 0 {
		t.Fatal("no key the Go analysis reads declares a default, so this test pins nothing")
	}
}

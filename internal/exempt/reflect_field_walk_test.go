package exempt

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestTheReflectWalkRetainsWhatEachAssertionOnInterfaceConverts(t *testing.T) {
	t.Parallel()
	refs := retainedRefs(t, analysisOf(t, "reflect-walk.txtar", Options{}), vocab.EncodingReflection)
	var got []string
	for _, ref := range refs {
		// The walk also reads every field it reaches (a reflective field walk), so
		// only the methods of the asserted types t1 to t7 are compared here.
		if name := strings.TrimPrefix(ref, "go://example.com/rwalk#"); strings.HasPrefix(name, "t") && strings.Contains(name, ".") {
			got = append(got, name)
		}
	}
	want := []string{"t1.M", "t2.M", "t3.M", "t5.M", "t6.M", "t7.M", "t7.hint7"}
	if !slices.Equal(got, want) {
		t.Errorf("EncodingReflectionDetector(reflect-walk.txtar) retained %v, want %v: t1b is a field no constant index selects, t4 a field Interface panics on, t5.extra a method the asserted interface does not require", got, want)
	}
}

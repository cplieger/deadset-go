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

// A generic bind filling a struct by reflection reaches a field's method only
// through reflect.TypeAssert[I](fv), which is the assertion fv.Interface().(I),
// so the method I requires is retained.
func TestReflectTypeAssertInAGenericBindRetainsWhatItsInterfaceRequires(t *testing.T) {
	t.Parallel()
	got := retainedRefs(t, analysisOf(t, "reflect-type-assert-bind.txtar", Options{}), vocab.EncodingReflection)
	if !slices.Contains(got, "go://example.com/rtab#limit.Range") {
		t.Errorf("EncodingReflectionDetector(reflect-type-assert-bind.txtar) retained %v, want limit.Range among them", got)
	}
}

// The control of the reflect.TypeAssert case: the same bind spelling the
// assertion fv.Interface().(I) retains the method I requires.
func TestAnInterfaceAssertionInAGenericBindRetainsWhatItsInterfaceRequires(t *testing.T) {
	t.Parallel()
	got := retainedRefs(t, analysisOf(t, "reflect-interface-assert-bind.txtar", Options{}), vocab.EncodingReflection)
	if !slices.Contains(got, "go://example.com/riab#limit.Range") {
		t.Errorf("EncodingReflectionDetector(reflect-interface-assert-bind.txtar) retained %v, want limit.Range among them", got)
	}
}

// reflect.TypeAssert[I](v) is the assertion v.Interface().(I), and Methods and
// Fields reach what Method and Field reach.
func TestTheGenericAndIteratorEntryPointsOfReflectRetainWhatTheirCallsReach(t *testing.T) {
	t.Parallel()
	refs := retainedRefs(t, analysisOf(t, "reflect-type-assert.txtar", Options{}), vocab.EncodingReflection)
	var got []string
	for _, ref := range refs {
		got = append(got, strings.TrimPrefix(ref, "go://example.com/rta#"))
	}
	for _, want := range []string{"seq.Range", "hook.Start", "row.id", "row.name"} {
		if !slices.Contains(got, want) {
			t.Errorf("EncodingReflectionDetector(reflect-type-assert.txtar) retained %v, want %s among them", got, want)
		}
	}
	if slices.Contains(got, "seq.extra") {
		t.Errorf("EncodingReflectionDetector(reflect-type-assert.txtar) retained seq.extra, which ranged does not require")
	}
}

// reflect.TypeAssert[I] taken as a value is the assertion of I on a receiver the
// walk cannot follow, which retains every method of every type it reaches.
func TestReflectTypeAssertTakenAsAValueRetainsAsAWalkThatCannotFollow(t *testing.T) {
	t.Parallel()
	got := retainedRefs(t, analysisOf(t, "reflect-type-assert-value.txtar", Options{}), vocab.EncodingReflection)
	for _, want := range []string{"go://example.com/rtv#width.Range", "go://example.com/rtv#width.extra"} {
		if !slices.Contains(got, want) {
			t.Errorf("EncodingReflectionDetector(reflect-type-assert-value.txtar) retained %v, want %s among them", got, want)
		}
	}
}

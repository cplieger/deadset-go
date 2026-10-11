package testsupport_test

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/cplieger/deadset-go/internal/testsupport"
)

// recorder is a sink that keeps every diagnostic rather than ending the test, so a
// test can read what a refusal says.
type recorder struct {
	failures []string
}

func (*recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// shared is a value holding every kind of reference the copier follows.
type shared struct {
	Pointer   *int
	Slice     []string
	Map       map[string][]int
	Interface any
	Nested    struct{ Items []int }
	count     int
}

func TestDetachedSharesNoMemoryWithTheOriginal(t *testing.T) {
	n := 1
	original := shared{
		Pointer:   &n,
		Slice:     []string{"a"},
		Map:       map[string][]int{"k": {1}},
		Interface: []int{1},
		Nested:    struct{ Items []int }{Items: []int{1}},
		count:     7,
	}

	copied := testsupport.Detached(t, original)
	*copied.Pointer = 2
	copied.Slice[0] = "b"
	copied.Map["k"][0] = 2
	copied.Interface.([]int)[0] = 2
	copied.Nested.Items[0] = 2

	if n != 1 || original.Slice[0] != "a" || original.Map["k"][0] != 1 ||
		original.Interface.([]int)[0] != 1 || original.Nested.Items[0] != 1 {
		t.Errorf("Detached(%+v): a write through the copy reached the original, now %+v (pointer %d)", copied, original, n)
	}
	if copied.count != 7 {
		t.Errorf("Detached(a value whose unexported count is 7).count = %d, want 7", copied.count)
	}
}

func TestDetachedCopiesWhatAnInterfaceHolds(t *testing.T) {
	n := 1
	var held any = &n

	copied := testsupport.Detached(t, held)
	pointer, isPointer := copied.(*int)
	if !isPointer {
		t.Fatalf("Detached(an interface holding *int) = %T, want *int", copied)
	}
	if pointer == &n {
		t.Errorf("Detached(an interface holding a pointer) holds the original pointer, want a copy")
	}
	if *pointer != 1 {
		t.Errorf("Detached(an interface holding a pointer to 1) points to %d, want 1", *pointer)
	}
}

func TestDetachedKeepsANilInterfaceSliceAndMapNil(t *testing.T) {
	copied := testsupport.Detached(t, shared{})
	if copied.Interface != nil || copied.Slice != nil || copied.Map != nil || copied.Pointer != nil {
		t.Errorf("Detached(the zero value) = %+v, want every reference nil", copied)
	}
}

// node is one element of a linked structure, which a deep copy must reproduce
// pointer for pointer.
type node struct {
	Next *node
	Name string
}

func TestDetachedKeepsTwoPointersToOneValueAsTwoPointersToOneCopy(t *testing.T) {
	one := &node{Name: "a"}

	copied := testsupport.Detached(t, []*node{one, one})
	if copied[0] == one {
		t.Fatalf("Detached(two pointers to one node) holds the original node, want a copy")
	}
	if copied[0] != copied[1] {
		t.Errorf("Detached(two pointers to one node) = %p and %p, want two pointers to one copy", copied[0], copied[1])
	}
}

func TestDetachedCopiesACycle(t *testing.T) {
	loop := &node{Name: "a"}
	loop.Next = loop

	copied := testsupport.Detached(t, loop)
	if copied == loop || copied.Next != copied {
		t.Errorf("Detached(a node pointing to itself) = %p with next %p, want a copy pointing to itself", copied, copied.Next)
	}
}

func TestDetachedRefusesAValueItCannotCopy(t *testing.T) {
	type unexportedReference struct {
		Name  string
		names []string
	}
	n := 1
	for name, tc := range map[string]struct {
		detach func(sink testsupport.Sink)
		names  string
	}{
		"a channel": {
			detach: func(sink testsupport.Sink) { testsupport.Detached(sink, make(chan int)) },
			names:  "chan int",
		},
		"an unsafe pointer": {
			detach: func(sink testsupport.Sink) { testsupport.Detached(sink, unsafe.Pointer(&n)) },
			names:  "unsafe.Pointer",
		},
		"a channel an interface holds": {
			detach: func(sink testsupport.Sink) { testsupport.Detached(sink, any(make(chan int))) },
			names:  "chan int",
		},
		"an unexported field holding a reference": {
			detach: func(sink testsupport.Sink) {
				testsupport.Detached(sink, []unexportedReference{{Name: "a", names: []string{"b"}}})
			},
			names: "unexportedReference.names",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var sink recorder
			tc.detach(&sink)
			if len(sink.failures) != 1 || !strings.Contains(sink.failures[0], tc.names) {
				t.Errorf("Detached(%s) reported %q, want one failure naming %q", name, sink.failures, tc.names)
			}
		})
	}
}

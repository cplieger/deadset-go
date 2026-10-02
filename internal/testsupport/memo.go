// Package testsupport holds what the tests of several packages share: a memo that
// builds one value per key for every test of a package asking for it, and a deep
// copy that hands each of those tests a value of its own. Only test files import it.
package testsupport

import (
	"sync"
	"testing"
)

// Memo holds one value per key, built by the first test that asks for it and read
// by every later one. The value is built on that test, so a failure to build it is
// reported there; every other test asking for the key fails naming that test. The
// zero Memo is empty and ready, and it is safe for concurrent use.
type Memo[K comparable, V any] struct {
	entries sync.Map
}

// memoEntry is one key's value and whether building it returned.
type memoEntry[V any] struct {
	value V
	owner string
	once  sync.Once
	built bool
}

// Of is the value under key, built with build on the first request. A caller that
// hands the value to more than one test copies it with [Detached] first.
func (m *Memo[K, V]) Of(t *testing.T, key K, build func(t *testing.T) V) V {
	t.Helper()

	held, _ := m.entries.LoadOrStore(key, new(memoEntry[V]))
	entry, _ := held.(*memoEntry[V])
	entry.once.Do(func() {
		entry.owner = t.Name()
		entry.value = build(t)
		entry.built = true
	})
	if !entry.built {
		t.Fatalf("Setup: the shared value under %+v did not build; %s reports why", key, entry.owner)
	}
	return entry.value
}

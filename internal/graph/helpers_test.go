package graph

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// sharedAnalysis is what one archive's load answers for the linux-amd64
// configuration under one list of configured root patterns, read by every test that
// asks the same question of it: the inventory, the references and the test-file
// rules over it, the roots and the unmatched patterns with and without the
// published-API seed, and the files the load excluded for importing C. No part of the
// load it was computed from is held.
type sharedAnalysis struct {
	symbols       []Symbol
	refs          []Reference
	rules         []TestFileRule
	roots         map[bool][]Root
	unmatched     map[bool][]Unmatched
	excludedByCgo []string
}

// analysisKey is one shared analysis: the archive and its configured root patterns.
type analysisKey struct {
	archive  string
	patterns string
}

// analyses holds every shared analysis the package's tests built.
var analyses memo[analysisKey, *sharedAnalysis]

// analysisOf is the shared analysis of one archive under the configured root
// patterns given, loaded once for every test that reads it.
func analysisOf(t *testing.T, archive string, patterns []string) *sharedAnalysis {
	t.Helper()

	key := analysisKey{archive: archive, patterns: strings.Join(patterns, "\n")}
	return analyses.of(t, key, func(t *testing.T) *sharedAnalysis {
		return analyzeShared(t, archive, patterns)
	})
}

// analyzeShared loads one archive and runs every pass a shared analysis answers.
func analyzeShared(t *testing.T, archive string, patterns []string) *sharedAnalysis {
	t.Helper()

	result, root := loadDir(t, extract(t, archive), "linux", "amd64")
	symbols, err := Symbols(result, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(%s) error: %v", archive, err)
	}
	refs, rules, err := References(result, root, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("References(%s) error: %v", archive, err)
	}
	held := &sharedAnalysis{
		symbols:       symbols,
		refs:          refs,
		rules:         rules,
		roots:         make(map[bool][]Root),
		unmatched:     make(map[bool][]Unmatched),
		excludedByCgo: result.ExcludedByCgo,
	}
	for _, published := range []bool{false, true} {
		opts := RootOptions{Patterns: patterns, PublishedAPI: published}
		held.roots[published], held.unmatched[published], err = Roots(result, root, os.ReadFile, symbols, opts)
		if err != nil {
			t.Fatalf("Roots(%s, %+v) = _, _, %v, want no error", archive, opts, err)
		}
	}
	return held
}

// inventory is the declarations the archive's load enumerated.
func (a *sharedAnalysis) inventory(t *testing.T) []Symbol {
	t.Helper()

	return detachedOrFail(t, a.symbols)
}

// references is the references the reference pass walked over the inventory.
func (a *sharedAnalysis) references(t *testing.T) []Reference {
	t.Helper()

	return detachedOrFail(t, a.refs)
}

// testFileRules is the rule that classified each test file, as the reference pass
// reported them.
func (a *sharedAnalysis) testFileRules(t *testing.T) []TestFileRule {
	t.Helper()

	return detachedOrFail(t, a.rules)
}

// rootsUnder is the roots and the unmatched patterns one setting of the
// published-API seed detected.
func (a *sharedAnalysis) rootsUnder(t *testing.T, published bool) ([]Root, []Unmatched) {
	t.Helper()

	return detachedOrFail(t, a.roots[published]), detachedOrFail(t, a.unmatched[published])
}

// excludedFiles is the files the load excluded for importing C.
func (a *sharedAnalysis) excludedFiles(t *testing.T) []string {
	t.Helper()

	return detachedOrFail(t, a.excludedByCgo)
}

// configured is the three passes a matrix merges, under one set of root options.
func (a *sharedAnalysis) configured(t *testing.T, published bool) Configured {
	t.Helper()

	roots, _ := a.rootsUnder(t, published)
	return Configured{Symbols: a.inventory(t), References: a.references(t), Roots: roots}
}

// detachedOrFail is detached, failing the test on a value it cannot copy.
func detachedOrFail[V any](t *testing.T, value V) V {
	t.Helper()

	copied, err := detached(value)
	if err != nil {
		t.Fatalf("Setup: copy a shared value: %v", err)
	}
	return copied
}

// memo holds one value per key, built by the first test that asks for it and read by
// every later one. The value is built on that test, so a failure to build it is
// reported there; every other test asking for the key fails naming that test.
type memo[K comparable, V any] struct {
	entries sync.Map
}

// memoEntry is one key's value and whether building it returned.
type memoEntry[V any] struct {
	value V
	owner string
	once  sync.Once
	built bool
}

// of is the value under key, built with build on the first request.
func (m *memo[K, V]) of(t *testing.T, key K, build func(t *testing.T) V) V {
	t.Helper()

	held, _ := m.entries.LoadOrStore(key, new(memoEntry[V]))
	entry, _ := held.(*memoEntry[V])
	entry.once.Do(func() {
		entry.owner = t.Name()
		entry.value = build(t)
		entry.built = true
	})
	if !entry.built {
		t.Fatalf("Setup: the shared analysis of %+v did not build; %s reports why", key, entry.owner)
	}
	return entry.value
}

// detached is a copy of value that shares no memory a test could write through: every
// pointer, slice, map and interface it holds is copied in turn. An unexported field
// holding any of those is refused rather than shared.
func detached[V any](value V) (V, error) {
	copied, err := copier{seen: make(map[copiedAt]reflect.Value)}.of(reflect.ValueOf(&value).Elem())
	if err != nil {
		var none V
		return none, err
	}
	held, _ := reflect.TypeAssert[V](copied)
	return held, nil
}

// copiedAt is one pointer already copied, so two pointers to one value stay two
// pointers to one copy.
type copiedAt struct {
	of reflect.Type
	at uintptr
}

// copier is one deep copy in progress.
type copier struct {
	seen map[copiedAt]reflect.Value
}

// of is the copy of one value.
func (c copier) of(v reflect.Value) (reflect.Value, error) {
	switch v.Kind() {
	case reflect.Pointer:
		return c.pointer(v)
	case reflect.Slice:
		if v.IsNil() {
			return v, nil
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		return out, c.elements(v, out)
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		return out, c.elements(v, out)
	case reflect.Map:
		return c.mapOf(v)
	case reflect.Interface:
		if v.IsNil() {
			return v, nil
		}
		inner, err := c.of(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out, nil
	case reflect.Struct:
		return c.structOf(v)
	case reflect.Chan, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("a value of type %s cannot be copied", v.Type())
	default:
		return v, nil
	}
}

// pointer is the copy of one pointer and of what it points to.
func (c copier) pointer(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return v, nil
	}
	at := copiedAt{of: v.Type(), at: v.Pointer()}
	if done, held := c.seen[at]; held {
		return done, nil
	}
	out := reflect.New(v.Type().Elem())
	c.seen[at] = out
	inner, err := c.of(v.Elem())
	if err != nil {
		return reflect.Value{}, err
	}
	out.Elem().Set(inner)
	return out, nil
}

// elements copies every element of a slice or an array into another.
func (c copier) elements(from, into reflect.Value) error {
	for i := range from.Len() {
		inner, err := c.of(from.Index(i))
		if err != nil {
			return err
		}
		into.Index(i).Set(inner)
	}
	return nil
}

// mapOf is the copy of one map, its keys and values copied in turn.
func (c copier) mapOf(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return v, nil
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	for iter := v.MapRange(); iter.Next(); {
		key, err := c.of(iter.Key())
		if err != nil {
			return reflect.Value{}, err
		}
		value, err := c.of(iter.Value())
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetMapIndex(key, value)
	}
	return out, nil
}

// structOf is the copy of one struct: an exported field is copied in turn, and an
// unexported one, which reflection cannot set, is taken by value and must hold no
// reference.
func (c copier) structOf(v reflect.Value) (reflect.Value, error) {
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if !field.IsExported() {
			if !flat(field.Type) {
				return reflect.Value{}, fmt.Errorf("the unexported field %s.%s holds a reference a copy would share",
					v.Type(), field.Name)
			}
			continue
		}
		inner, err := c.of(v.Field(i))
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(inner)
	}
	return out, nil
}

// flat reports whether a value of one type holds no pointer, slice, map, interface or
// channel, so copying it by value shares nothing.
func flat(of reflect.Type) bool {
	switch of.Kind() {
	case reflect.Array:
		return flat(of.Elem())
	case reflect.Struct:
		for field := range of.Fields() {
			if !flat(field.Type) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan, reflect.UnsafePointer:
		return false
	default:
		return true
	}
}

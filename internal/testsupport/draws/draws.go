// Package draws sets how many draws each property test of this module makes and
// runs them. A property whose draws start no process makes its full count on every
// run; one whose draws start the go command makes a quarter of it, unless [Variable]
// is [Full], which the weekly job sets.
//
// Every property test calls [Check], and every loop over a key named seed ranges
// over [Seeds]; this package's tests refuse a rapid run outside this package and
// a seed loop over anything else, since no run could set that count.
package draws

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

// Variable is the environment variable the weekly job sets to [Full] so that every
// property makes its full count of draws.
const Variable = "DEADSET_PROPERTY_DRAWS"

// Full is the value of [Variable] that runs every property at its full count.
const Full = "full"

// loadingShare is the share of its full count a [Loading] property makes where
// [Variable] is not [Full]: one in four.
const loadingShare = 4

// shardChecks is how many draws one parallel shard of [Check] makes. rapid makes a
// property's draws one after another, so a property of a hundred loads would wait on
// two hundred go command starts in a row.
const shardChecks = 5

// Cost is what one draw of a property costs: whether it starts the go command.
type Cost int

const (
	// InMemory is a property whose draws start no process.
	InMemory Cost = iota
	// Loading is a property whose draws load or build code through the go command.
	Loading
)

// Count is how many draws a property of full draws at cost makes on this run: full
// where [Variable] is [Full] or the property starts no process, a quarter of it
// otherwise, and at least one.
func Count(full int, cost Cost) int {
	if cost == InMemory || os.Getenv(Variable) == Full {
		return full
	}
	return max(1, (full+loadingShare-1)/loadingShare)
}

// Seeds is the seeds a test drawing its trees from seeds ranges over: [Count] of
// them, from 0, so a failing seed's subtest names the tree it drew on every run.
func Seeds(full int, cost Cost) uint64 {
	return uint64(max(0, Count(full, cost)))
}

// sharded is whether [Check] runs shards, decided once before the first property
// runs: it does where the invocation gave no -rapid.checks, and then sets rapid's
// count to [shardChecks]. It also stops rapid writing a fail file into the package
// directory, the one file a test would make outside its temporary directory; the
// failure's message carries the seed that reproduces it.
var sharded = sync.OnceValues(func() (bool, error) {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == "rapid.checks" })
	if err := flag.Set("rapid.nofailfile", "true"); err != nil {
		return false, err
	}
	if given {
		return false, nil
	}
	return true, flag.Set("rapid.checks", strconv.Itoa(shardChecks))
})

// Check runs prop as rapid.Check does, [Count] of full draws at cost rounded up to a
// whole shard, as parallel subtests of [shardChecks] draws each, so a property's go
// command starts wait side by side; each shard draws from its own seed and shrinks
// its own failure. An invocation giving -rapid.checks runs that many draws in one
// rapid.Check instead, the way a deeper local run asks for them.
func Check(t *testing.T, full int, cost Cost, prop func(*rapid.T)) {
	t.Helper()
	shard, err := sharded()
	if err != nil {
		t.Fatalf("Setup: set rapid's flags: %v", err)
	}
	if !shard {
		rapid.Check(t, prop)
		return
	}
	for i := range Shards(full, cost) {
		t.Run(fmt.Sprintf("draws_%d", i), func(t *testing.T) {
			t.Parallel()
			rapid.Check(t, prop)
		})
	}
}

// Shards is how many parallel subtests [Check] runs for a property of full draws at
// cost where the invocation gives no -rapid.checks.
func Shards(full int, cost Cost) int {
	return (Count(full, cost) + shardChecks - 1) / shardChecks
}

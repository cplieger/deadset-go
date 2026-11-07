package findings

import (
	"os"
	"runtime"
	"testing"
	"weak"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/load"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
	"github.com/cplieger/deadset-go/v2/internal/scope"
	"github.com/cplieger/deadset-go/v2/internal/testsupport"
)

// reloads is a run's configurations loaded afresh on every read, as the run's store
// does under memory pressure, holding what it handed out only through weak
// pointers: before each read it collects and records whether the configuration it
// handed out last is still reachable.
type reloads struct {
	t              *testing.T
	matrix         *load.Matrix
	root           string
	configurations []load.Configuration
	last           weak.Pointer[Configured]
	lastResult     weak.Pointer[load.Result]
	reads, kept    int
}

func (r *reloads) Len() int { return len(r.configurations) }

func (r *reloads) At(config int) (*Configured, error) {
	if r.reads > 0 {
		runtime.GC()
		if r.last.Value() != nil || r.lastResult.Value() != nil {
			r.kept++
		}
	}
	r.reads++
	result, err := r.matrix.Load(r.t.Context(), &r.configurations[config])
	if err != nil {
		return nil, err
	}
	inv, err := inventory.Of(&result, r.root, os.ReadFile)
	if err != nil {
		return nil, err
	}
	resolver, err := resolve.New(&result, inv)
	if err != nil {
		return nil, err
	}
	index, err := facts.NewIndex(&result, resolver)
	if err != nil {
		return nil, err
	}
	one := &Configured{Result: &result, Resolve: resolver, Facts: index, Symbols: inv.Symbols}
	r.last, r.lastResult = weak.Make(one), weak.Make(one.Result)
	return one, nil
}

// The findings pass reads one configuration's syntax and types at a time and keeps
// only identifiers, positions and strings across them, so a configuration it read
// is collectable once it reads the next.
func TestThePassKeepsNoConfigurationItReadBefore(t *testing.T) {
	linux := load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}
	windows := load.Configuration{ID: "windows-amd64", OS: "windows", Arch: "amd64"}
	dir := extract(t, "declarations-test-support-platform.txtar")
	in := inputOfDir(t, dir, applicationConfig(), Consumers{}, linux, windows)
	doc, err := scope.ForDir(dir)
	if err != nil {
		t.Fatalf("Setup: scope.ForDir(%s): %v", dir, err)
	}
	per := &reloads{
		t:              t,
		matrix:         load.NewMatrix(testsupport.Topology(t.Context(), t, doc)),
		root:           doc.Target.Path,
		configurations: []load.Configuration{linux, windows},
	}
	in.Per = per

	if _, err := newPass(in); err != nil {
		t.Fatalf("newPass over reloaded configurations = %v", err)
	}
	if per.reads < 2*per.Len() {
		t.Fatalf("Setup: the pass read %d configurations, want at least %d", per.reads, 2*per.Len())
	}
	if per.kept > 0 {
		t.Errorf("%d of the pass's %d reads found the configuration read before still reachable, want none", per.kept, per.reads-1)
	}
}

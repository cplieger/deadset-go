package load

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// consumption is what one module's build resolves of the units, as the go
// command's module graph reports it ([program.7], [program.8]): the module path of
// each unit it builds from that unit's directory under the unit's own path, and of
// each it builds from there under another path, whose references name no
// declaration of the target.
type consumption struct {
	units, renamed []string
}

// all is the module path of every unit the build resolves, under any path.
func (c consumption) all() []string { return slices.Concat(c.units, c.renamed) }

// commandWaitDelay is how long a cancelled go command's output may stay open after
// it is killed before the run stops waiting for it.
const commandWaitDelay = 2 * time.Second

// listedModule is one module of `go list -m -json`.
type listedModule struct {
	Replace *listedModule
	Error   *struct{ Err string }
	Path    string
	Version string
	Dir     string
	Main    bool
}

// errUnanswered reports a module graph in which a main module carries an error:
// only a main module's replace directives and its workspace place a module in a
// directory (go.dev/ref/mod#go-mod-file-replace), so an error there leaves what
// its build resolves unknown.
var errUnanswered = errors.New("a main module of the graph carries an error")

// consumes is what the build of the module rooted at dir, under the GOWORK setting
// workspace, resolves of the units, from the go command's module graph (`go list
// -m -json all`) under tc's overlay and settings, never from the paths its module
// file names. A module of the graph builds from a unit's directory where it is a
// main module there, dir's own aside, or a replacement without a version names
// it, one by dir itself included. An error of the command or of a main module of
// the graph is returned: the answer is unknown.
func (top *Topology) consumes(ctx context.Context, dir, workspace string, tc toolchain) (consumption, error) {
	settings, err := loadSettings(&Configuration{}, dir, workspace, tc)
	if err != nil {
		return consumption{}, err
	}
	cmd := exec.CommandContext(ctx, goCommand, "list", "-m", "-e", "-json", "-mod=readonly", "all")
	inOwnGroup(cmd)
	cmd.WaitDelay = commandWaitDelay
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), withoutPlatform(settings)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return consumption{}, fmt.Errorf("list the module graph of %s: %w: %s", top.Rel(dir), err, strings.TrimSpace(stderr.String()))
	}
	var held consumption
	for decoder := json.NewDecoder(bytes.NewReader(out)); ; {
		var m listedModule
		if err := decoder.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return consumption{}, fmt.Errorf("read the module graph of %s: %w", top.Rel(dir), err)
		}
		if m.Main && m.Error != nil {
			return consumption{}, fmt.Errorf("list the module graph of %s: %w: %s", top.Rel(dir), errUnanswered, m.Error.Err)
		}
		top.addBuilt(&held, &m, dir)
	}
	return held, nil
}

// addBuilt adds to held each unit the graph's module m, of the module rooted at
// dir, builds from that unit's directory, under the unit's path or another.
func (top *Topology) addBuilt(held *consumption, m *listedModule, dir string) {
	from := builtFrom(m)
	if from == "" || m.Replace == nil && scope.Same(from, dir) {
		return
	}
	for i := range top.Units {
		unit := &top.Units[i]
		switch {
		case unit.Path == "" || !scope.Same(from, unit.Dir):
		case m.Path == unit.Path:
			held.units = appendNew(held.units, unit.Path)
		default:
			held.renamed = appendNew(held.renamed, unit.Path)
		}
	}
}

// builtFrom is the directory the graph's module m builds from where that is a
// directory on disk: a main module's own, or a replacement's without a version,
// and empty otherwise.
func builtFrom(m *listedModule) string {
	switch {
	case m.Replace != nil && m.Replace.Version == "":
		return m.Replace.Dir
	case m.Replace == nil && m.Main:
		return m.Dir
	}
	return ""
}

// withoutPlatform is settings without the target platform's, which a module graph
// does not depend on.
func withoutPlatform(settings []string) []string {
	return slices.DeleteFunc(slices.Clone(settings), func(v string) bool {
		return strings.HasPrefix(v, "GOOS=") || strings.HasPrefix(v, "GOARCH=")
	})
}

// moduleRole is which load of the run one module is, which [consumptionRule]
// reads with what its build resolves.
type moduleRole uint8

const (
	roleUnit     moduleRole = iota // a unit of the target, named or walked
	roleNested                     // a module below the target no scope declares
	roleReader                     // a module the workspace listing the target lists outside it
	roleConsumer                   // a declared consumer
	roleProbe                      // another module root a walk of a module's tree meets
)

// answer is what [Topology.consumes] answered of one module, or that the run
// could not ask or the go command could not answer, so its build may resolve any
// unit under any path; dropped says the run drops the module unloaded, with a
// note that carries the answer ([Dropped]).
type answer struct {
	consumption
	unknown, dropped bool
}

// verdict is what the run does with one module: whether it loads it as a reader
// of the units, where its role leaves that to the answer, and why it leaves out
// unloaded every export the module may use.
type verdict struct {
	left []LeftOutReason
	load bool
}

// consumptionRule is the one decision of what a module's answer means, for every
// role ([program.7], [program.9]). An unknown answer may consume the target: it
// loads where a reader's would and is [LeftOutUnanswered]. A unit built under
// another path is [LeftOutRenamed]. A nested module or a reader building a unit
// under its own path loads, and a probe doing so is [LeftOutConsumer]. A unit and
// a declared consumer always load. A module the run drops is left out by its
// dropped note, which counts the answer ([Result.markUnanalysedConsumers]).
func consumptionRule(role moduleRole, a answer) verdict {
	var v verdict
	switch {
	case a.dropped:
	case a.unknown:
		v.left = append(v.left, LeftOutUnanswered)
	case len(a.renamed) > 0:
		v.left = append(v.left, LeftOutRenamed)
	}
	switch role {
	case roleNested, roleReader:
		v.load = a.unknown || len(a.units) > 0
	case roleProbe:
		if !a.unknown && len(a.renamed) == 0 && len(a.units) > 0 {
			v.left = append(v.left, LeftOutConsumer)
		}
	case roleUnit, roleConsumer:
		v.load = true
	}
	return v
}

// decideConsumption asks, under the overlay that hides what the go command may
// block on, what the build of every module the run may load resolves of the units,
// and applies [consumptionRule] to each: units, nested modules, workspace readers
// and declared consumers, then each other module root a walk of one of their
// trees meets ([Topology.treeRoots]). The scan [Topology.hide] runs again where a
// module was passed over or probed, so no load reads a hidden entry only such a
// module holds.
func (top *Topology) decideConsumption(ctx context.Context) error {
	asked := len(top.Nested) + len(top.Readers)
	if err := top.trees(ctx); err != nil {
		return err
	}
	probes, err := top.treeRoots(ctx)
	if err != nil {
		return err
	}
	if err := top.withAsker(ctx, (*asker).decideLoaded); err != nil {
		return err
	}
	top.probes = probes
	probed := len(top.probes) > 0
	if probed {
		if err := top.hide(ctx); err != nil {
			return err
		}
		if err := top.withAsker(ctx, (*asker).decideProbes); err != nil {
			return err
		}
		top.probes = nil
	}
	if !probed && len(top.Nested)+len(top.Readers) == asked {
		return nil
	}
	return top.hide(ctx)
}

// withAsker runs decide with an [asker] whose toolchain reads the overlay the
// topology's hidden entries make now.
func (top *Topology) withAsker(ctx context.Context, decide func(*asker) error) error {
	overlay, remove, err := top.writeOverlay()
	if err != nil {
		return fmt.Errorf("load: write the overlay that hides what the toolchain may block on: %w", err)
	}
	defer remove()
	a := &asker{ctx: ctx, top: top, tc: toolchain{overlay: overlay, noEnvFile: top.noEnvFile}}
	a.answerOf = a.ask
	return decide(a)
}

// trees records the [goTree] of every module the run may load: each unit no
// failure refused, nested module, workspace reader and declared consumer, before
// [consumptionRule] decides which nested modules and readers load, so the module
// roots below one it passes over are decided too ([Topology.treeRoots]).
func (top *Topology) trees(ctx context.Context) error {
	for _, group := range [][]discoveredModule{top.Units, top.Nested, top.Readers, top.Consumers} {
		for i := range group {
			if m := &group[i]; m.Refused == nil {
				var err error
				if m.tree, err = top.goTreeOf(ctx, m.Dir, moduleDirOf(m)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// asker is one round of [Topology.decideConsumption]: the toolchain it asks the go
// command with, and answerOf, which asks one module ([asker.ask]).
type asker struct {
	ctx      context.Context
	top      *Topology
	answerOf func(m *discoveredModule, workspace string) answer
	tc       toolchain
}

// decideLoaded applies [consumptionRule] to every unit, nested module, workspace
// reader and declared consumer, keeping the nested modules and readers it loads.
func (a *asker) decideLoaded() error {
	var err error
	if a.top.Nested, err = a.keep(a.top.Nested, roleNested); err != nil {
		return err
	}
	if a.top.Readers, err = a.keep(a.top.Readers, roleReader); err != nil {
		return err
	}
	for i := range a.top.Units {
		if _, err := a.decide(roleUnit, &a.top.Units[i], a.top.Units[i].Workspace); err != nil {
			return err
		}
	}
	for i := range a.top.Consumers {
		if _, err := a.decide(roleConsumer, &a.top.Consumers[i], a.top.ConsumerWorkspace); err != nil {
			return err
		}
	}
	return nil
}

// decideProbes applies [consumptionRule] to every other module root a walk met.
func (a *asker) decideProbes() error {
	for i := range a.top.probes {
		if _, err := a.decide(roleProbe, &a.top.probes[i], a.top.probes[i].Workspace); err != nil {
			return err
		}
	}
	return nil
}

// keep is each of held the rule loads in role.
func (a *asker) keep(held []discoveredModule, role moduleRole) ([]discoveredModule, error) {
	kept := held[:0]
	for i := range held {
		load, err := a.decide(role, &held[i], held[i].Workspace)
		if err != nil {
			return nil, err
		}
		if load {
			kept = append(kept, held[i])
		}
	}
	return kept, nil
}

// decide records in m what its build resolves under workspace and applies
// [consumptionRule] in role, leaving out unloaded at m's directory, or a probe's
// link, each export the verdict gives up, and reports whether m loads.
func (a *asker) decide(role moduleRole, m *discoveredModule, workspace string) (bool, error) {
	got := a.answerOf(m, workspace)
	if err := a.ctx.Err(); err != nil {
		return false, fmt.Errorf("load: %w", err)
	}
	got.dropped = role != roleProbe && (m.Refused != nil || m.File == nil || m.FileErr != nil)
	m.builds, m.buildsUnknown = got.consumption, got.unknown
	if role == roleNested || role == roleReader {
		m.Consumes = got.units
	}
	at := m.Dir
	if role == roleProbe {
		at = m.linkedAt
	}
	v := consumptionRule(role, got)
	for _, reason := range v.left {
		a.top.unloaded = append(a.top.unloaded, LeftOutPackage{Dir: at, Module: m.Path, Reason: reason})
	}
	return v.load, nil
}

// ask is what the build of m under workspace resolves of the units. It is unknown
// where the run cannot read m's module file or its workspace file, and where the
// go command cannot list its module graph: a build of it may still compile, the
// go command fetching a newer toolchain or a module the offline run cannot.
func (a *asker) ask(m *discoveredModule, workspace string) answer {
	if m.File == nil || m.FileErr != nil || workspace != workspaceOff && a.top.workspaceRead(workspace) != "" {
		return answer{unknown: true}
	}
	builds, err := a.top.consumes(a.ctx, m.Dir, workspace, a.tc)
	if err != nil {
		return answer{unknown: true}
	}
	return answer{consumption: builds}
}

// treeRoots is the module at each module root a walk of a module's tree meets,
// through a link or not, that is no unit, nested module, workspace reader or
// declared consumer, each once ([Topology.probeAt]), with the roots a walk of each
// such module's own tree meets: the one enumeration of the module roots the run
// decides, which [Topology.nestedModules]'s search, entering no testdata or dot
// directory and following no link, cannot narrow. One whose module file is no
// regular file is passed over, as that search passes it over.
func (top *Topology) treeRoots(ctx context.Context) ([]discoveredModule, error) {
	var probes []discoveredModule
	known := func(dir string) bool {
		same := func(m discoveredModule) bool { return scope.Same(m.Dir, dir) }
		return slices.ContainsFunc(top.Units, same) || slices.ContainsFunc(top.Consumers, same) ||
			slices.ContainsFunc(top.Nested, same) || slices.ContainsFunc(top.Readers, same) || slices.ContainsFunc(probes, same)
	}
	var met []linkedRoot
	var passed []string
	for _, group := range [][]discoveredModule{top.Units, top.Nested, top.Readers, top.Consumers} {
		for i := range group {
			met = append(met, group[i].tree.roots...)
		}
	}
	for len(met) > 0 {
		root := met[0]
		met = met[1:]
		if known(root.dir) || scope.Listed(passed, root.dir) {
			continue
		}
		probe := top.probeAt(ctx, root)
		if irregular(probe.FileErr) {
			// A build there blocks on its module file before it compiles anything
			// ([program.6]).
			passed = append(passed, root.dir)
			continue
		}
		tree, err := top.goTreeOf(ctx, probe.Dir, moduleDirOf(&probe))
		if err != nil {
			return nil, err
		}
		met = append(met, tree.roots...)
		probes = append(probes, probe)
	}
	return probes, nil
}

// probeAt is the module at the module root a walk met, loading under the
// workspace file that governs it where that lists it, and refused where the go
// command would read a file there that is no regular file outside the overlay
// ([Topology.irregularRead]).
func (top *Topology) probeAt(ctx context.Context, root linkedRoot) discoveredModule {
	m := discoveredModule{Dir: root.dir, linkedAt: root.at, Workspace: workspaceOff}
	m.File, m.FileErr = readModuleFile(top.root, root.dir)
	if workspace, err := scope.WorkspaceOf(ctx, top.root, root.dir); err == nil && workspace.Lists(root.dir) {
		m.Workspace = workspace.File
	}
	if path := top.irregularRead(m.File, m.Dir, m.Workspace); path != "" {
		m.Refused = top.blockingFailure(path)
	}
	return m
}

// moduleDirOf is the directory of the module file governing m's directory, or
// that directory where none does.
func moduleDirOf(m *discoveredModule) string {
	if m.File == nil {
		return m.Dir
	}
	return filepath.Dir(m.File.Path)
}

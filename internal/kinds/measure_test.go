package kinds

import (
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// measureVariable gates the measurements of this file. They run the kinds over the
// modules of a whole workspace, take minutes, and assert nothing: their output is a
// table a report carries, so a battery runs them only when it is asked to.
const measureVariable = "DEADSET_MEASURE"

// measuredModules are the real modules the kinds are measured over: four libraries
// analyzed with no consumer information, and one application.
var measuredModules = []struct {
	dir  string
	kind config.TargetKind
}{
	{"/workspace/jsoncap", config.Library},
	{"/workspace/langtag", config.Library},
	{"/workspace/slogx", config.Library},
	{"/workspace/webhttp", config.Library},
	{"/workspace/deadset-go", config.Application},
}

// measuring reports whether the gate is open, and skips the measurement where it is
// not.
func measuring(t *testing.T) bool {
	t.Helper()

	if os.Getenv(measureVariable) == "1" {
		return true
	}
	t.Skipf("a measurement rather than a test: run it with %s=1", measureVariable)
	return false
}

// measured is the input and the findings of one module, or a note where the module
// is not on this machine.
func measured(t *testing.T, dir string, kind config.TargetKind) (*Input, Result) {
	t.Helper()

	if _, err := os.Stat(dir); err != nil {
		t.Skipf("%s is not on this machine: %v", dir, err)
	}
	resolved := config.Default()
	resolved.Target.Kind = kind
	in := inputOfDir(t, dir, resolved, Consumers{})

	result, err := Compute(in, packageEmitters())
	if err != nil {
		t.Fatalf("Compute(every kind of the package, %s) = error %v, want the findings of the pass", dir, err)
	}
	return in, result
}

// TestMeasureRealModules prints what every kind of the package reports over real
// modules: the count per code, the count per class, and every finding of the
// application, which is this module itself and the one a hand adjudication exists
// for.
func TestMeasureRealModules(t *testing.T) {
	if !measuring(t) {
		return
	}
	for _, one := range measuredModules {
		t.Run(one.dir, func(t *testing.T) {
			in, result := measured(t, one.dir, one.kind)

			t.Logf("%s (%s): %d candidates, %d findings",
				one.dir, one.kind, len(in.Sweep.Candidates), len(result.Findings))
			for _, line := range tallied(result.Findings, func(found *Finding) string { return found.Code }) {
				t.Logf("   %s", line)
			}
			for _, line := range tallied(result.Findings, func(found *Finding) string { return "class " + string(found.Class) }) {
				t.Logf("   %s", line)
			}
			for i := range result.Findings {
				found := &result.Findings[i]
				t.Logf("   %s %s:%d:%d %s %s [%s] %s", found.Code, found.Position.Path,
					found.Position.Line, found.Position.Column, found.Symbol.Kind, found.Symbol.Name,
					found.Confidence, found.Message)
			}
		})
	}
}

// TestMeasureWhatHeldEveryOtherDeclarationBack prints, per module, how many
// declarations an exemption retained and under which class, and which candidates no
// kind of this package reports, so a report says what answered for a declaration a
// hand adjudication named and a finding does not.
func TestMeasureWhatHeldEveryOtherDeclarationBack(t *testing.T) {
	if !measuring(t) {
		return
	}
	for _, one := range measuredModules {
		t.Run(one.dir, func(t *testing.T) {
			in, result := measured(t, one.dir, one.kind)

			retained := make([]string, 0, len(in.Sweep.Retained))
			for _, held := range in.Sweep.Retained {
				retained = append(retained, held.Class+" "+in.Refs[held.ID]+" ("+held.Detail+")")
			}
			slices.Sort(retained)
			t.Logf("%s: %d exemptions retained a declaration", one.dir, len(retained))
			for _, line := range retained {
				t.Logf("   retained %s", line)
			}

			reported := make(map[string]bool, len(result.Findings))
			for i := range result.Findings {
				reported[result.Findings[i].Symbol.Ref] = true
			}
			var silent []string
			for i := range in.Sweep.Candidates {
				candidate := &in.Sweep.Candidates[i]
				ref := in.Refs[candidate.ID]
				if reported[ref] {
					continue
				}
				silent = append(silent, ref+" production="+strconv.Itoa(candidate.ProductionRefs)+
					" test="+strconv.Itoa(candidate.TestRefs))
			}
			slices.Sort(silent)
			t.Logf("%s: %d candidates no kind of this package reports", one.dir, len(silent))
			for _, line := range silent {
				t.Logf("   unreported %s", line)
			}
		})
	}
}

// tallied is one count per value of a key over the findings, in the order of the key.
func tallied(findings []Finding, key func(*Finding) string) []string {
	counts := make(map[string]int, len(findings))
	for i := range findings {
		counts[key(&findings[i])]++
	}
	lines := make([]string, 0, len(counts))
	for value, count := range counts {
		lines = append(lines, value+" "+strconv.Itoa(count))
	}
	slices.Sort(lines)
	return lines
}

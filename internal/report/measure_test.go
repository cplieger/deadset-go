package report

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/config"
)

// measureVariable gates the measurements of this file. They run the whole analysis over
// real modules, take minutes, and assert nothing: their output is a table a report
// carries, so a battery runs them only when it is asked to.
const measureVariable = "DEADSET_MEASURE"

// measuredModules are the real modules the renderings are measured over: one library and
// one application.
var measuredModules = []struct {
	dir  string
	kind config.TargetKind
}{
	{"/workspace/webhttp", config.Library},
	{"/workspace/deadset-go", config.Application},
}

// TestMeasureTheRenderings renders the report of a real module in every format and
// reports the size of each, the first lines of the text and the result count of the
// SARIF document against the finding count.
func TestMeasureTheRenderings(t *testing.T) {
	if os.Getenv(measureVariable) != "1" {
		t.Skipf("a measurement rather than a test: run it with %s=1", measureVariable)
	}
	for _, module := range measuredModules {
		t.Run(strings.TrimPrefix(module.dir, "/workspace/"), func(t *testing.T) {
			if _, err := os.Stat(module.dir); err != nil {
				t.Skipf("%s is not on this machine: %v", module.dir, err)
			}
			envelope, opts := envelopeOfDir(t, module.dir, module.kind)

			text := rendered(t, "text", &envelope, opts)
			document := rendered(t, "json", &envelope, opts)
			annotations := rendered(t, "annotations", &envelope, opts)
			log := sarifOf(t, &envelope, opts)

			t.Logf("%s: %d findings, %d stale suppressions, %d deletable lines",
				module.dir, envelope.Totals.Findings, envelope.Totals.StaleSuppressions,
				envelope.Totals.DeletableLines)
			t.Logf("%s: text %d bytes, json %d bytes, annotations %d bytes",
				module.dir, len(text), len(document), len(annotations))
			t.Logf("%s: %d SARIF results against %d findings and %d stale suppressions, %d rules",
				module.dir, len(log.Runs[0].Results), len(envelope.Findings),
				len(envelope.StaleSuppressions), len(log.Runs[0].Tool.Driver.Rules))

			for i, line := range strings.Split(string(text), "\n") {
				if i == 20 {
					break
				}
				t.Log(strconv.Itoa(i+1) + ": " + line)
			}
		})
	}
}

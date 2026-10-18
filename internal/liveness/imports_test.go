package liveness

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The sweep is a stage before the findings: it reads the facts, the roots and the
// records, and no stage after it, so no report policy or configuration can decide
// liveness. The import graph is read from the toolchain, not from directory names.
func TestLivenessImportsNoStageAfterItAndNoReportOrConfigurationPackage(t *testing.T) {
	const module = "github.com/cplieger/deadset-go/v2/"
	list := func(format string) []string {
		out, err := exec.CommandContext(t.Context(), "go", "list", "-f", format, ".").Output()
		if err != nil {
			t.Fatalf("Setup: go list -f %s: %v", format, err)
		}
		return strings.Fields(string(out))
	}
	later := []string{
		"cmd/", "internal/config", "internal/kinds", "internal/findings", "internal/run",
		"internal/suppress", "internal/edges", "internal/deps",
	}
	for _, dep := range list(`{{join .Deps " "}}`) {
		if rest, own := strings.CutPrefix(dep, module); own && slices.ContainsFunc(later, func(p string) bool { return strings.HasPrefix(rest, p) }) {
			t.Errorf("liveness depends on %s, a stage after it", dep)
		}
		if strings.HasPrefix(dep, "github.com/cplieger/deadset-spec/v8/report") ||
			strings.HasPrefix(dep, "github.com/cplieger/deadset-spec/v8/config") {
			t.Errorf("liveness depends on %s, a report or configuration package", dep)
		}
	}
}

package run

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/scope"
)

// A workspace member under the run directory is a consumer the report names, and one
// outside it is a reader whose references still count.
func TestNameableSplitsTheWorkspaceMembersByWhetherTheReportCanNameThem(t *testing.T) {
	invoked, err := os.Getwd()
	if err != nil {
		t.Fatalf("Setup: os.Getwd = _, %v", err)
	}
	inside := scope.Module{Path: filepath.Join(invoked, "member")}
	outside := scope.Module{Path: t.TempDir()}

	named, readers := nameable([]scope.Module{inside, outside})
	if !slices.Equal(named, []scope.Module{inside}) || !slices.Equal(readers, []scope.Module{outside}) {
		t.Errorf("nameable() = %v, %v, want [%v] and [%v]", named, readers, inside, outside)
	}
}

package graph

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/load"
)

func TestATypeErrorAnotherConfigurationDoesNotReportRootsAndSkipsNothing(t *testing.T) {
	dir := extract(t, "type-error-platform.txtar")
	var results []load.Result
	for _, c := range []load.Configuration{
		{ID: "linux-amd64", OS: "linux", Arch: "amd64"},
		{ID: "windows-amd64", OS: "windows", Arch: "amd64"},
	} {
		result, _ := loadDirUnder(t.Context(), t, dir, c)
		results = append(results, *result)
	}
	elsewhere := CheckedElsewhere(results)
	if got := elsewhere[0]; len(got) != 0 {
		t.Errorf("CheckedElsewhere()[linux] = %v, want none", got)
	}
	if got, want := slices.Sorted(maps.Keys(elsewhere[1])), []string{"example.com/platform/sig"}; !slices.Equal(got, want) {
		t.Errorf("CheckedElsewhere()[windows] = %v, want %v", got, want)
	}

	windows := &results[1]
	symbols, err := Symbols(windows, dir, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(windows) error: %v", err)
	}
	for _, test := range []struct {
		name      string
		elsewhere map[string]bool
		roots     []string
		skipped   []string
	}{
		{
			name: "alone", roots: []string{"go://example.com/platform/broken#Missing", "go://example.com/platform/sig#Reload"},
			skipped: []string{"broken/broken.go", "sig/sig.go"},
		},
		{
			name: "in the matrix", elsewhere: elsewhere[1], roots: []string{"go://example.com/platform/broken#Missing"},
			skipped: []string{"broken/broken.go"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, _, err := Roots(windows, dir, os.ReadFile, symbols, RootOptions{CheckedElsewhere: test.elsewhere})
			if err != nil {
				t.Fatalf("Roots(windows) error: %v", err)
			}
			got := refsOfKind(detection{symbols: symbols, roots: roots}, RootTypeError)
			if !slices.Equal(got, test.roots) {
				t.Errorf("Roots(windows) roots %v by a type error, want %v", got, test.roots)
			}
			var skipped []string
			for _, skip := range TypeErrorSkips(windows, dir, test.elsewhere) {
				skipped = append(skipped, skip.Path)
			}
			if !slices.Equal(skipped, test.skipped) {
				t.Errorf("TypeErrorSkips(windows) lists %v, want %v", skipped, test.skipped)
			}
		})
	}
}

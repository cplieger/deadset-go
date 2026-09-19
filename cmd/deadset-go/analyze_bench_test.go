package main

import (
	"os"
	"path/filepath"
	"testing"
)

// recordTolerance is how far a measurement may sit above the recorded time before the
// benchmark reports it. Three times the record is the bound, for two measured reasons:
// one analysis of the same tree on one machine spreads by about half again as much
// depending on what else is running on it, and the record is written on one machine
// while the comparison may run on another with fewer cores. What the bound catches is
// a regression in the shape of the analysis, which is one load per configuration and
// no per-symbol query, so a regression there is a factor and not a percentage.
const recordTolerance = 3.0

// BenchmarkAnalyzeTheRecordedModules measures one analysis of each module shape the
// committed record names and compares it against the recorded time.
//
// This is the re-measurement a release runs: the unit suite generates the modules and
// checks their size, and the wall time is measured here, because a time asserted in
// the suite would measure whatever else the runner is doing. A measurement above the
// tolerance is reported rather than logged, so the comparison fails the run that asked
// for it; the numbers it prints are what the record is written from.
func BenchmarkAnalyzeTheRecordedModules(b *testing.B) {
	record := readBenchmarkRecord(b)
	if len(record.Runs) == 0 {
		b.Fatal("Setup: the benchmark record names no run, so there is nothing to measure")
	}

	for i := range record.Runs {
		recorded := &record.Runs[i]
		b.Run(recorded.Name, func(b *testing.B) {
			base := b.TempDir()
			dir := filepath.Join(base, recorded.Name)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				b.Fatalf("Setup: create %s: %v", dir, err)
			}
			size := synthesizeModule(b, dir,
				recorded.Generator.Packages, recorded.Generator.FilesPerPackage, recorded.Generator.DeclarationsPerFile)
			b.Chdir(base)

			var measured, findings int
			var total float64
			for b.Loop() {
				elapsed, reported := analyzeSynthesized(b.Context(), b, base, recorded.Name)
				total += elapsed.Seconds()
				findings = reported
				measured++
			}

			seconds := total / float64(measured)
			b.ReportMetric(seconds, "s/analysis")
			b.Logf("%s: %+v, %d findings, %.2f s per analysis, recorded %.2f s",
				recorded.Name, size, findings, seconds, recorded.Seconds)
			if seconds > recorded.Seconds*recordTolerance {
				b.Errorf("one analysis of the %s module took %.2f s, want at most %.2f s, which is the recorded %.2f s within a factor of %.0f (%s)",
					recorded.Name, seconds, recorded.Seconds*recordTolerance, recorded.Seconds, recordTolerance, recordPath)
			}
			if seconds > record.Budget.BudgetSeconds {
				b.Errorf("one analysis of the %s module took %.2f s, want at most the %.0f s budget",
					recorded.Name, seconds, record.Budget.BudgetSeconds)
			}
		})
	}
}

package report

import "github.com/cplieger/deadset-go/internal/config"

// The verdict codes one report yields, which are the codes
// contract/exit-codes.json names for a run that produced an answer. The codes for
// an invocation this analyzer refuses and for a run that produced no answer are
// not among them: neither is a verdict about a report, so neither is a value this
// function could return.
const (
	verdictClean    = 0
	verdictFindings = 1
	verdictPending  = 4
)

// ExitCode is the verdict of one run, read from the report that run produced.
//
// A pending finding outranks everything else, because a report holding one is not
// an answer until a merge resolves the finding against the other language's
// report. A stale suppression yields the findings code whatever severity the
// configuration assigns any kind, so the severity map cannot switch it off. A
// finding yields that code when its severity is at or above reporters.fail_on,
// which makes a warn-severity finding alone a clean run under the default.
//
// The counts come from the totals rather than from the arrays, so a report bounded
// by a maximum finding count still returns the verdict of the whole run: the
// totals describe the finding set and the bound moved only what a rendering
// prints.
//
// exitCodeOff is the invocation's request that the code carry no verdict, and it
// yields the clean code for any report. The caller still writes the report and
// still names the verdict, which is this function's answer for the same report
// with the request withdrawn.
func ExitCode(e *Envelope, cfg *config.Config, exitCodeOff bool) int {
	if exitCodeOff {
		return verdictClean
	}
	switch {
	case e.Totals.Pending > 0:
		return verdictPending
	case e.Totals.StaleSuppressions > 0:
		return verdictFindings
	case failingFindings(&e.Totals.BySeverity, failOn(cfg)) > 0:
		return verdictFindings
	default:
		return verdictClean
	}
}

// failOn is the lowest severity that fails a run: the configured one, and the
// documented default where the configuration names none or names a value outside
// the three severities. A resolved configuration always names one, so the fallback
// is for a caller holding no configuration at all rather than for a document.
func failOn(cfg *config.Config) config.Severity {
	if cfg == nil {
		return config.Deny
	}
	switch cfg.Reporters.FailOn {
	case config.Allow, config.Warn, config.Deny:
		return cfg.Reporters.FailOn
	default:
		return config.Deny
	}
}

// failingFindings is how many findings of the run carry a severity at or above the
// failing one, over the whole finding set.
func failingFindings(counted *BySeverity, at config.Severity) int {
	failing := counted.Deny
	if at == config.Warn || at == config.Allow {
		failing += counted.Warn
	}
	if at == config.Allow {
		failing += counted.Allow
	}
	return failing
}

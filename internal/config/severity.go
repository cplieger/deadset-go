package config

import (
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
)

// severitySection is the name of the severity object, whose member names the
// closed key list leaves open.
const severitySection = "severity"

// familyKeyLength is the length of a severity key naming a whole family: the
// prefix and two digits, where a code carries four.
const familyKeyLength = 4

// unusedExportedCode is the kind reporting an exported symbol nothing references.
// It is the one kind whose default turns on what the run knows about the target's
// consumers.
const unusedExportedCode = "DS1001"

// kind is one issue kind this analyzer ships, as resolution reads it: the code
// the vocabulary assigns it, the severity a configuration naming neither the code
// nor its family resolves to, and whether the kind reports at all.
type kind struct {
	code     string
	severity Severity
	enabled  bool
}

// liveKinds are the issue kinds this analyzer ships, in the vocabulary's order,
// each carrying the default the vocabulary declares for it. A retired code is
// absent from the vocabulary, so it names no live kind here and a severity key
// naming one is refused like a code the vocabulary never held.
//
// The vocabulary is [catalog]'s, which is pinned equal to the Contract's
// issue-kind document, so a kind the Contract adds or a default it moves arrives
// here without a second table to keep in step.
func liveKinds() []kind {
	rows := catalog.Kinds()
	live := make([]kind, 0, len(rows))
	for i := range rows {
		live = append(live, kind{
			code:     rows[i].Code,
			severity: Severity(rows[i].DefaultSeverity),
			enabled:  rows[i].DefaultEnabled,
		})
	}
	return live
}

// liveKind returns the issue kind one code names and whether this analyzer ships
// it. A family prefix names no kind of its own, so it is not one.
func liveKind(code string) (kind, bool) {
	row, live := catalog.Kind(code)
	if !live {
		return kind{}, false
	}
	return kind{code: row.Code, severity: Severity(row.DefaultSeverity), enabled: row.DefaultEnabled}, true
}

// namesLiveKind reports whether one severity key names at least one issue kind
// this analyzer ships: the code itself, or one code of the family its two-digit
// prefix names. A retired code and a family whose range holds no live kind both
// name none.
func namesLiveKind(key string) bool {
	if _, live := catalog.Kind(key); live {
		return true
	}
	if len(key) != familyKeyLength {
		return false
	}
	published := catalog.Kinds()
	for i := range published {
		if strings.HasPrefix(published[i].Code, key) {
			return true
		}
	}
	return false
}

// fixedSeverityCodes are the codes whose severity the vocabulary fixes. A severity
// key naming one, or a family prefix whose range holds one, is an unimplemented
// key rather than a setting.
func fixedSeverityCodes() []string {
	var fixed []string
	published := catalog.Kinds()
	for i := range published {
		if published[i].Fixed {
			fixed = append(fixed, published[i].Code)
		}
	}
	return fixed
}

// familyPrefix is the two-digit family prefix one issue-kind code belongs to,
// which is the key a configuration addresses the whole family by.
func familyPrefix(code string) string {
	if len(code) < familyKeyLength {
		return code
	}
	return code[:familyKeyLength]
}

// EffectiveSeverity returns what a finding of one issue kind does to this run:
// Allow reports nothing, Warn reports and never fails the run, Deny reports and
// fails it. The configuration's key for the code wins, then its key for the code's
// family, then the default the Contract declares for the kind.
//
// consumersLoaded is the run's own fact rather than a setting: it is true when
// every consumer the scope declared loaded, so a run that loaded none passes
// false. With consumers.complete it decides whether a library's published API is
// closed world, and a library whose published API is not is the one case that
// moves a default: the unreferenced-exported kind reports nothing, because a
// caller the analysis cannot see may reference the symbol.
//
// The answer is which kinds report and how loudly. Where a kind may report is the
// kind's own rule, not this one: the narrowing kinds report over the target's
// internal tree and its main packages until the consumer set is known, and that
// scope is theirs to apply.
//
// A code no live kind carries reports nothing, because it names nothing this
// analyzer reports. A configuration cannot name one: resolution refuses a severity
// key that names no live kind.
func (c *Config) EffectiveSeverity(code string, consumersLoaded bool) Severity {
	declared, live := liveKind(code)
	if !live {
		return Allow
	}
	if severity, named := c.Severity[code]; named {
		return severity
	}
	if severity, named := c.Severity[familyPrefix(code)]; named {
		return severity
	}
	if !declared.enabled {
		return Allow
	}
	if declared.code == unusedExportedCode && c.publishedAPIIsOpenWorld(consumersLoaded) {
		return Allow
	}
	return declared.severity
}

// publishedAPIIsOpenWorld reports whether the target's published API may have
// references the analysis cannot see: the target is a library, and either its
// configuration does not declare the consumer set complete or a declared consumer
// did not load.
func (c *Config) publishedAPIIsOpenWorld(consumersLoaded bool) bool {
	return c.Target.Kind == Library && (!c.Consumers.Complete || !consumersLoaded)
}

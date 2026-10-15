package config

import (
	"slices"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// severitySection is the name of the severity object, whose member names the
// closed key list leaves open.
const severitySection = "severity"

// namesLiveKind reports whether one severity key names at least one issue kind
// this analyzer ships: the code itself, or one code of the family its two-digit
// prefix names. A retired code and a family whose range holds no live kind both
// name none.
func namesLiveKind(key string) bool {
	return len(vocab.CodesOf(key)) > 0
}

// fixedByContract returns every code whose severity the Contract fixes that one
// severity key names, in ascending code order: the code itself, or every code of
// the family its two-digit prefix names. A family key covers every such code, so
// the refusal names them all rather than the first one found.
func fixedByContract(key string) []string {
	var fixed []string
	for _, code := range vocab.CodesOf(key) {
		if kind, _ := vocab.KindOf(code); kind.Fixed {
			fixed = append(fixed, string(code))
		}
	}
	slices.Sort(fixed)
	return fixed
}

// EffectiveSeverity returns what a finding of one issue kind does to this run:
// Allow reports nothing, Warn reports and never fails the run, Deny reports and
// fails it. The configuration's key for the code wins, then its key for the code's
// family, then the default the Contract declares for the kind. No default depends
// on the target kind: a library's published API is withheld by
// analysis.min_confidence alone.
//
// A code no live kind carries reports nothing, because it names nothing this
// analyzer reports. A configuration cannot name one: resolution refuses a severity
// key that names no live kind.
func (c *Config) EffectiveSeverity(code vocab.Code) vocab.Severity {
	declared, live := vocab.KindOf(code)
	if !live {
		return vocab.Allow
	}
	if severity, named := c.Severity[string(code)]; named {
		return severity
	}
	if severity, named := c.Severity[vocab.FamilyOf(code)]; named {
		return severity
	}
	if !declared.DefaultEnabled {
		return vocab.Allow
	}
	return declared.DefaultSeverity
}

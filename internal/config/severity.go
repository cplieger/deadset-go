package config

import (
	"slices"
	"strings"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// severityKeyRefused says why a well-formed severity key the generated
// vocabulary does not hold is not a setting: every code it names has a severity
// the Contract fixes, or it names no live kind.
func severityKeyRefused(key string) string {
	var fixed []string
	for _, code := range vocab.CodesOf(key) {
		if kind, _ := vocab.KindOf(code); kind.Fixed {
			fixed = append(fixed, string(code))
		}
	}
	switch {
	case len(fixed) > 0:
		slices.Sort(fixed)
		return "the Contract fixes the severity of " + spellCodes(fixed) + ", which this key names"
	case len(key) == vocab.FamilyKeyLength:
		return "no issue kind carries a code of the family " + key
	default:
		return "no issue kind carries the code " + key
	}
}

// spellCodes lists codes as a refusal names them: one on its own, several
// separated by commas with the last joined by and.
func spellCodes(codes []string) string {
	if len(codes) < 2 {
		return strings.Join(codes, "")
	}
	return strings.Join(codes[:len(codes)-1], ", ") + " and " + codes[len(codes)-1]
}

// EffectiveSeverity returns what a finding of one issue kind does to this run:
// Allow reports nothing, Warn reports and never fails the run, Deny reports and
// fails it. The configuration's key for the code wins, then its key for the
// code's family, then the default the Contract declares for the kind. A code no
// live kind carries reports nothing; resolution refuses a key naming one.
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

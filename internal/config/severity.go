package config

import "strings"

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

// kind is one issue kind this analyzer ships: the code the Contract assigns it,
// the severity a configuration naming neither the code nor its family resolves to,
// whether the kind reports at all, and whether the Contract fixes both against
// configuration.
type kind struct {
	code     string
	severity Severity
	enabled  bool
	fixed    bool
}

// liveKinds are the issue kinds this analyzer ships, in ascending code order, each
// carrying the default the Contract declares for it. A retired code is absent, so
// it names no live kind and a severity key naming one is refused like a code the
// vocabulary never held. A test pins the table equal to the Contract's issue-kind
// vocabulary, so a kind the Contract adds fails there rather than resolving to
// nothing here.
func liveKinds() []kind {
	return []kind{
		{code: unusedExportedCode, severity: Deny, enabled: true},
		{code: "DS1002", severity: Deny, enabled: true},
		{code: "DS1003", severity: Deny, enabled: true},
		{code: "DS1004", severity: Deny, enabled: true},
		{code: "DS1005", severity: Deny, enabled: true},
		{code: "DS1006", severity: Deny, enabled: true},
		{code: "DS1101", severity: Warn, enabled: true},
		{code: "DS1102", severity: Warn, enabled: true},
		{code: "DS1103", severity: Deny, enabled: true},
		{code: "DS1104", severity: Warn, enabled: true},
		{code: "DS1201", severity: Deny, enabled: true},
		{code: "DS1203", severity: Warn, enabled: true},
		{code: "DS1204", severity: Deny, enabled: true},
		{code: "DS1301", severity: Deny, enabled: true},
		{code: "DS1302", severity: Deny, enabled: true},
		{code: "DS1303", severity: Deny, enabled: true},
		{code: "DS1501", severity: Deny, enabled: true},
		{code: "DS1502", severity: Deny, enabled: true},
		{code: "DS1601", severity: Deny, enabled: true},
		{code: "DS1605", severity: Warn, enabled: true},
		{code: "DS1701", severity: Deny, enabled: true},
		{code: "DS1702", severity: Deny, enabled: true},
		{code: "DS1703", severity: Deny, enabled: true, fixed: true},
		{code: "DS1704", severity: Deny, enabled: true, fixed: true},
		{code: "DS1705", severity: Deny, enabled: true},
		{code: "DS1801", severity: Warn, enabled: true},
		{code: "DS1802", severity: Warn, enabled: true},
		{code: "DS1803", severity: Warn, enabled: true},
		{code: "DS1805", severity: Deny, enabled: true},
		{code: "DS1807", severity: Deny, enabled: true},
		{code: "DS1809", severity: Deny, enabled: true},
	}
}

// liveKind returns the issue kind one code names and whether this analyzer ships
// it. A family prefix names no kind of its own, so it is not one.
func liveKind(code string) (kind, bool) {
	for _, declared := range liveKinds() {
		if declared.code == code {
			return declared, true
		}
	}
	return kind{}, false
}

// namesLiveKind reports whether one severity key names at least one issue kind
// this analyzer ships: the code itself, or one code of the family its two-digit
// prefix names. A retired code and a family whose range holds no live kind both
// name none.
func namesLiveKind(key string) bool {
	for _, declared := range liveKinds() {
		if key == declared.code || (len(key) == familyKeyLength && strings.HasPrefix(declared.code, key)) {
			return true
		}
	}
	return false
}

// fixedSeverityCodes are the codes whose severity the Contract fixes. A severity
// key naming one, or a family prefix whose range holds one, is an unimplemented
// key rather than a setting.
func fixedSeverityCodes() []string {
	var fixed []string
	for _, declared := range liveKinds() {
		if declared.fixed {
			fixed = append(fixed, declared.code)
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

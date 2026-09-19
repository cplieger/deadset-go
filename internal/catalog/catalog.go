// Package catalog is the issue-kind vocabulary this analyzer reports under: one
// row per live kind, carrying the languages it applies to, the default a
// configuration overrides, the ceiling on the confidence a finding of the kind may
// claim, and what a mechanical edit may do with it.
//
// The rows are the vocabulary's own, in its order, and a retired code is absent,
// so a lookup of one answers that this analyzer reports nothing under it. The
// table is written here rather than decoded from the vocabulary document at run
// time, because the built binary links the standard library and the analysis
// packages alone; a test pins every row and the order equal to the document, so a
// row the vocabulary changes fails there rather than drifting silently.
package catalog

import (
	"slices"
	"sync"
)

// The values a row's members are drawn from: the severity a configuration
// overrides, the ceiling on a finding's confidence, and what a mechanical edit may
// do with a finding.
const (
	severityWarn = "warn"
	severityDeny = "deny"

	classCertain = "certain"

	fixDeletable  = "deletable"
	fixNarrowable = "narrowable"
	fixManual     = "manual"
	fixNone       = "none"
)

// Row is one issue kind: the code a finding carries, the kind name a report
// spells, the languages the kind applies to, whether it reports unless a
// configuration disables it, the severity a configuration naming neither the code
// nor its family resolves to, the highest reachability class a finding of the kind
// may carry as its confidence, what a mechanical edit may do with the finding, and
// whether the default and the severity are fixed against configuration.
type Row struct {
	Code            string
	Name            string
	DefaultSeverity string
	MaxClass        string
	Fixability      string
	Languages       []string
	DefaultEnabled  bool
	Fixed           bool
}

// clone returns a row that shares nothing with the table, so a caller reading one
// cannot change what the next reader sees.
func (r *Row) clone() Row {
	held := *r
	held.Languages = slices.Clone(r.Languages)
	return held
}

// rows are the live issue kinds, in the vocabulary's order.
func rows() []Row {
	return []Row{
		{Code: "DS1001", Name: "unused-exported", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1002", Name: "unused-unexported", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1003", Name: "unused-member", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1004", Name: "test-only-use", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1005", Name: "test-of-dead-code", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1006", Name: "deprecated-unused", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1101", Name: "unnecessary-export", Languages: []string{"go", "ts"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixNarrowable, DefaultEnabled: true},
		{Code: "DS1102", Name: "unnecessary-exposure", Languages: []string{"go"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixNarrowable, DefaultEnabled: true},
		{Code: "DS1103", Name: "unreachable-export", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1104", Name: "redundant-export-keyword", Languages: []string{"ts"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixNarrowable, DefaultEnabled: true},
		{Code: "DS1201", Name: "unused-interface", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1203", Name: "uncalled-interface-method", Languages: []string{"go", "ts"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixManual, DefaultEnabled: true},
		{Code: "DS1204", Name: "unused-satisfaction-assertion", Languages: []string{"go"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1301", Name: "write-only-symbol", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1302", Name: "unused-enum-member", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1303", Name: "unused-type-parameter", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1501", Name: "file-never-built", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1502", Name: "file-never-imported", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1601", Name: "unused-dependency", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixManual, DefaultEnabled: true},
		{Code: "DS1605", Name: "unused-module-directive", Languages: []string{"go"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1701", Name: "suppression-without-reason", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixNone, DefaultEnabled: true},
		{Code: "DS1702", Name: "unscoped-ignore-entry", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixNone, DefaultEnabled: true},
		{Code: "DS1703", Name: "stale-suppression", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixNone, DefaultEnabled: true, Fixed: true},
		{Code: "DS1704", Name: "unmatched-root", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixNone, DefaultEnabled: true, Fixed: true},
		{Code: "DS1705", Name: "stale-cross-language-edge", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixNone, DefaultEnabled: true},
		{Code: "DS1801", Name: "unused-parameter", Languages: []string{"go", "ts"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixManual, DefaultEnabled: true},
		{Code: "DS1802", Name: "unused-receiver", Languages: []string{"go"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1803", Name: "unused-result", Languages: []string{"go", "ts"}, DefaultSeverity: severityWarn, MaxClass: classCertain, Fixability: fixManual, DefaultEnabled: true},
		{Code: "DS1805", Name: "unreachable-statement", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1807", Name: "dead-store", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
		{Code: "DS1809", Name: "unreachable-case", Languages: []string{"go", "ts"}, DefaultSeverity: severityDeny, MaxClass: classCertain, Fixability: fixDeletable, DefaultEnabled: true},
	}
}

// table is the vocabulary indexed by code, built once because every lookup of
// every finding reads it.
var table = sync.OnceValue(func() map[string]Row {
	all := rows()
	held := make(map[string]Row, len(all))
	for _, r := range all {
		held[r.Code] = r
	}
	return held
})

// Kind returns the row one code names, and whether this analyzer reports under it.
// A retired code and a code the vocabulary never held both name no row.
func Kind(code string) (Row, bool) {
	row, live := table()[code]
	if !live {
		return Row{}, false
	}
	return row.clone(), true
}

// Kinds returns every live kind, in the vocabulary's order. The rows share nothing
// with the table, so a caller may keep and change what it is given.
func Kinds() []Row {
	return rows()
}

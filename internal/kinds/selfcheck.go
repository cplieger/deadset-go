package kinds

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/suppress"
)

// The codes of the self-check kinds this analyzer reports. The stale-edge code is
// absent: an analyzer publishes one evaluation per edge side it enumerated and the
// merge is what reports a stale edge.
const (
	suppressionWithoutReasonCode = "DS1701"
	unscopedEntryCode            = "DS1702"
	staleSuppressionCode         = "DS1703"
	unmatchedRootCode            = "DS1704"
)

// The two words the subject vocabulary has for what a self-check finding is about.
const (
	suppressionSubject = "suppression"
	rootSubject        = "root"
)

// configurationDocument is the repository configuration, whose name and location
// the Contract fixes. It is where a configured root is written, and a configured
// root carries no line of its own, so a finding about one renders at this
// document's fixed position.
const configurationDocument = "deadset.json"

// documentPosition is the fixed position of a finding about a document rather than
// about a line of one.
const documentPosition = 1

// SuppressionsWithoutReason reports every suppression the grammar refused for
// carrying no reason, at any of the three mechanisms.
//
// The subject is the suppression as written, so the finding carries the mechanism and
// the record with a field empty exactly where the suppression lacks it. A refusal
// binds nothing, so nothing was suppressed and the finding the suppression was meant
// to cover stays reported beside this one.
//
// A directive naming several codes and carrying no reason is one finding per code, at
// the one position the directive is written on, because each code is a record of its
// own and a reader is told which of them is now unsuppressed.
func SuppressionsWithoutReason(in *Input) ([]Finding, error) {
	return in.refused(suppressionWithoutReasonCode)
}

// UnscopedEntries reports every ignore-file entry and baseline row the grammar
// refused for naming a symbol and no path, which is an instruction that would mask a
// match anywhere in the project.
//
// An entry lacking both its reason and its path is reported here and under the
// reason-free kind both, at one position: the two rules are separate checks over the
// same record, so a maintainer who supplies the missing reason still sees the missing
// path.
func UnscopedEntries(in *Input) ([]Finding, error) {
	return in.refused(unscopedEntryCode)
}

// refused is the findings of one refusal kind: the refusals the readers reported
// under its code, in the order they read them.
func (in *Input) refused(code string) ([]Finding, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}
	found := make([]Finding, 0, len(in.Refusals))
	for i := range in.Refusals {
		refused := &in.Refusals[i]
		message, err := refusalMessage(refused)
		if err != nil {
			return nil, err
		}
		if refused.Reported != code {
			continue
		}
		one := selfCheck(refused.Reported, suppressionSubject,
			in.suppressionRef(refused.Symbol, refused.Path), refused.Site, message)
		one.Details.Mechanism = refused.Mechanism.String()
		one.Details.Entry = &Entry{
			Code:   refused.Code,
			Symbol: refused.Symbol,
			Path:   refused.Path,
			Reason: refused.Reason,
		}
		found = append(found, one)
	}
	return found, nil
}

// refusalMessage is the sentence one refusal reports, which names the mechanism,
// the code the suppression named and the rule it broke.
func refusalMessage(refused *suppress.Refusal) (string, error) {
	switch refused.Reported {
	case suppressionWithoutReasonCode:
		return written(refused.Mechanism) + " for " + refused.Code + " carries no reason", nil
	case unscopedEntryCode:
		return written(refused.Mechanism) + " for " + refused.Code + " names a symbol and no path", nil
	default:
		return "", fmt.Errorf("%w: a suppression is refused under %s, which is no refusal of the grammar",
			ErrEmitter, refused.Reported)
	}
}

// StaleSuppressions reports every suppression that matched no current finding, at
// either mechanism and in the baseline alike.
//
// A record is stale when it bound to nothing, and when the symbol it bound is one
// the sweep would not have reported without the mark: a mark in effect for nothing
// is a claim nobody checks, so the kind is fixed on at deny and no configuration
// reduces it.
//
// A record naming a code this analyzer reports nothing under, a retired code, is
// stale whatever it bound: nothing can match it. It is not dormant either, because
// nothing silenced it.
//
// A record bound to a symbol whose code the configuration silences is dormant
// instead: neither in effect nor stale, and reported by nothing. Silencing a kind
// must not fail a run over the adjudications that turning it back on would need, so
// both ways of silencing one count, the severity set to allow and a confidence the
// configured minimum excludes.
//
// Several stale records at one site are one finding naming every code, because one
// directive above a line that declares several symbols is one record per symbol and
// a maintainer edits one line.
func StaleSuppressions(in *Input) ([]Finding, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}
	suppressed := make(map[graph.SymbolID]bool, len(in.staleInput().Suppressed))
	for _, id := range in.staleInput().Suppressed {
		suppressed[id] = true
	}

	stale := make([]*suppress.Record, 0, len(in.Marks))
	for i := range in.Marks {
		mark := &in.Marks[i]
		if in.dormant(mark) || in.inEffect(mark, suppressed) {
			continue
		}
		stale = append(stale, mark)
	}
	return in.collapsed(stale)
}

// inEffect reports whether one record held a finding back: it bound a declaration
// the sweep would otherwise have reported, under a code this analyzer reports.
func (in *Input) inEffect(mark *suppress.Record, suppressed map[graph.SymbolID]bool) bool {
	if mark.Bound == "" || !suppressed[mark.Bound] {
		return false
	}
	_, live := catalog.Kind(mark.Code)
	return live
}

// staleInput is the sweep the staleness answer is read from, and an empty answer
// where the run carries none, so a caller reads one shape.
func (in *Input) staleInput() *graph.Result {
	if in.Sweep == nil {
		return &graph.Result{}
	}
	return in.Sweep
}

// dormant reports whether one bound record is silent rather than in effect or
// stale: the configuration silences the code it names, or the finding that code
// would produce for the symbol it bound falls below the configured minimum
// confidence.
//
// An unbound record is never dormant. Nothing silenced it: it names a site that
// resolves to no declaration, which is a claim about code that has gone whatever
// the configuration holds.
func (in *Input) dormant(mark *suppress.Record) bool {
	if mark.Bound == "" {
		return false
	}
	row, live := catalog.Kind(mark.Code)
	if !live {
		return false
	}
	if in.Config.EffectiveSeverity(mark.Code, in.consumersAllLoaded()) == config.Allow {
		return true
	}
	least := Class(in.Config.Analysis.MinConfidence)
	if least.rank() == 0 {
		return false
	}
	return in.ClassOf(mark.Bound).lower(Class(row.MaxClass)).rank() < least.rank()
}

// collapsed turns the stale records into one finding per site, each naming every
// code the site's records carry.
//
// The sites are reported in the order the records were read, which is the order the
// readers return them in: the inline directives by position, then the ignore
// entries in document order, then the baseline rows. The codes of one site are
// named in ascending order, so two runs over one tree name them the same way.
func (in *Input) collapsed(stale []*suppress.Record) ([]Finding, error) {
	at := make(map[token.Position][]*suppress.Record, len(stale))
	order := make([]token.Position, 0, len(stale))
	for _, mark := range stale {
		site := token.Position{Filename: mark.Site.Filename, Line: mark.Site.Line, Column: mark.Site.Column}
		if _, held := at[site]; !held {
			order = append(order, site)
		}
		at[site] = append(at[site], mark)
	}

	found := make([]Finding, 0, len(order))
	for _, site := range order {
		records := at[site]
		first := records[0]
		one := selfCheck(staleSuppressionCode, suppressionSubject,
			in.suppressionRef(first.Symbol, first.Path), first.Site,
			written(first.Mechanism)+" for "+spellCodes(codesAt(records))+" matches no current finding")
		one.Details.Mechanism = first.Mechanism.String()
		one.Details.Entry = &Entry{
			Code:   first.Code,
			Symbol: first.Symbol,
			Path:   first.Path,
			Reason: first.Reason,
		}
		found = append(found, one)
	}
	return found, nil
}

// codesAt is every code the records at one site name, in ascending order and each
// once.
func codesAt(records []*suppress.Record) []string {
	codes := make([]string, 0, len(records))
	for _, mark := range records {
		codes = append(codes, mark.Code)
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

// UnmatchedRoots reports every configured root and every configured root pattern
// that named no symbol of the inventory.
//
// The subject is the configured string as the configuration spells it, pattern and
// all, because that is the string a maintainer corrects. A root set nobody checks
// silently changes every result, so the kind is fixed on at deny.
func UnmatchedRoots(in *Input) ([]Finding, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}
	found := make([]Finding, 0, len(in.Unmatched))
	for _, unmatched := range in.Unmatched {
		message := "configured root matches no symbol of the inventory"
		if strings.ContainsAny(unmatched.Source, "*?") {
			message = "configured root pattern matches no symbol of the inventory"
		}
		found = append(found, selfCheck(unmatchedRootCode, rootSubject, unmatched.Source,
			token.Position{Filename: configurationDocument, Line: documentPosition, Column: documentPosition},
			message))
	}
	return found, nil
}

// Totals are the two suppression counts a report's envelope prints: how many
// records bound to a symbol the sweep would otherwise have reported, and how many
// directives, entries and rows carry a reason.
//
// The two count different things on purpose. A record is one code at one site, so a
// directive naming two codes is two records and either may be in effect on its own;
// a reason is written once per directive, entry or row, so the same directive is one
// reason. A dormant record carries a reason and is counted there, and is in effect
// for nothing and is not counted here, which is what makes silencing a kind safe.
func Totals(in *Input) (inEffect, reasons int) {
	if in == nil || in.Config == nil {
		return 0, 0
	}
	suppressed := make(map[graph.SymbolID]bool, len(in.staleInput().Suppressed))
	for _, id := range in.staleInput().Suppressed {
		suppressed[id] = true
	}
	sites := make(map[token.Position]bool, len(in.Marks))
	for i := range in.Marks {
		mark := &in.Marks[i]
		if mark.Reason != "" {
			sites[token.Position{Filename: mark.Site.Filename, Line: mark.Site.Line, Column: mark.Site.Column}] = true
		}
		if !in.dormant(mark) && in.inEffect(mark, suppressed) {
			inEffect++
		}
	}
	return inEffect, len(sites)
}

// selfCheck starts one finding of a self-check kind: a row of a document, which the
// framework completes like every other finding.
//
// The subject is the record as the document writes it, and the position is the
// record's own site. A record spans no range, so it ends on the line it starts on and
// its size is that one line.
func selfCheck(code, subject, ref string, site token.Position, message string) Finding {
	return findingAt(code, Subject{
		Ref:       ref,
		Kind:      subject,
		Name:      ref,
		SizeLines: 1,
	}, Position{
		Path:    site.Filename,
		Line:    site.Line,
		Column:  site.Column,
		EndLine: site.Line,
	}, message)
}

// suppressionRef is the reference a finding about one suppression carries: the
// declaration the record names, and the file form of the document that holds the
// record where it names none, which is the inline directive that bound nothing.
func (in *Input) suppressionRef(symbol, path string) string {
	if symbol != "" {
		return symbol
	}
	if ref := in.fileRef(path); ref != "" {
		return ref
	}
	return path
}

// fileRef is the reference of the file one target-relative path names, and the
// empty string where the inventory holds no file under it.
func (in *Input) fileRef(path string) string {
	if in.Merged == nil {
		return ""
	}
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind == graph.KindFile && symbol.Pos.Filename == path {
			return symbol.Ref
		}
	}
	return ""
}

// written is the reader's name for one mechanism, which a message spells out where
// the vocabulary carries the mechanism alone.
func written(m suppress.Mechanism) string {
	switch m {
	case suppress.MechanismInline:
		return "inline directive"
	case suppress.MechanismIgnore:
		return "ignore entry"
	case suppress.MechanismBaseline:
		return "baseline row"
	default:
		return m.String()
	}
}

// spellCodes lists the codes one message names: one code on its own, and several
// separated by commas with the last joined by and.
func spellCodes(codes []string) string {
	if len(codes) < 2 {
		return strings.Join(codes, "")
	}
	return strings.Join(codes[:len(codes)-1], ", ") + " and " + codes[len(codes)-1]
}

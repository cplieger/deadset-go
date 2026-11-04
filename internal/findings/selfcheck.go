package findings

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The words the subject vocabulary has for what a self-check finding is about. No
// Go configuration key names a declaration, so this analyzer reports no finding
// about a configured declaration.
const (
	suppressionSubject = "suppression"
	rootSubject        = "root"
)

// configurationDocument is the repository configuration at its conventional name and
// location, which the Contract fixes. It is the document a finding about a configured
// root carries where the run read its roots from no document of the target: the
// finding is about a document and has to name one, and this is the document a
// maintainer writes a configured root in.
const configurationDocument = "deadset.json"

// documentPosition is the fixed position of a finding about a document rather than
// about a line of one.
const documentPosition = 1

// suppressionsWithoutReason reports every suppression the grammar refused for
// carrying no reason, at any of the three mechanisms, as written: the record's field
// is empty exactly where the suppression lacks it. A refusal binds nothing, so the
// finding it was meant to cover stays reported. A reasonless directive naming several
// codes is one finding per code, each a record of its own.
func suppressionsWithoutReason(in *pass) ([]draft, error) {
	return in.refused(vocab.SuppressionWithoutReason)
}

// unscopedEntries reports every ignore-file entry and baseline row the grammar
// refused for naming a symbol and no path, which is an instruction that would mask a
// match anywhere in the project.
//
// An entry lacking both its reason and its path is reported here and under the
// reason-free kind both, at one position: the two rules are separate checks over the
// same record, so a maintainer who supplies the missing reason still sees the missing
// path.
func unscopedEntries(in *pass) ([]draft, error) {
	return in.refused(vocab.UnscopedIgnoreEntry)
}

// refused is the findings of one refusal kind: the refusals the readers reported
// under its code, in the order they read them.
func (in *pass) refused(code vocab.Code) ([]draft, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}
	found := make([]draft, 0, len(in.Refusals))
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
		one.Details.Entry = &suppressionEntry{
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
	case vocab.SuppressionWithoutReason:
		return written(refused.Mechanism) + " for " + string(refused.Code) + " carries no reason", nil
	case vocab.UnscopedIgnoreEntry:
		return written(refused.Mechanism) + " for " + string(refused.Code) + " names a symbol and no path", nil
	default:
		return "", fmt.Errorf("%w: a suppression is refused under %s, which is no refusal of the grammar",
			errEmitter, refused.Reported)
	}
}

// staleSuppressions reports every record that is neither in effect ([pass.inEffect])
// nor dormant ([pass.dormant]). It reads what the pass withheld, so it runs after
// every other emitter. The records one row binds are stale together or not at all,
// and the stale records of one site are one finding naming every code, because a
// maintainer edits one line.
func staleSuppressions(in *pass) ([]draft, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}

	stale := make([]*suppress.Record, 0, len(in.Marks))
	held := make(map[recordKey]bool, len(in.Marks))
	for i := range in.Marks {
		mark := &in.Marks[i]
		if matched := in.inEffect(i); matched || in.dormant(i, matched) {
			held[keyOf(mark)] = true
			continue
		}
		stale = append(stale, mark)
	}
	stale = slices.DeleteFunc(stale, func(mark *suppress.Record) bool { return held[keyOf(mark)] })
	return in.collapsed(stale)
}

// recordKey is what the records one suppression binds share: the site it is
// written at, the code it names and the reference it names. A row whose reference
// and path bind several declarations, as two init functions of one file do, is one
// record per declaration under one key, and it is stale only when every record it
// binds is.
type recordKey struct {
	code   vocab.Code
	symbol string
	site   token.Position
}

func keyOf(mark *suppress.Record) recordKey {
	return recordKey{site: mark.Site, code: mark.Code, symbol: mark.Symbol}
}

// inEffect reports whether the record at one place in Marks matched a finding,
// whether or not a dial then withholds it. A record the pass withheld a finding
// through matched it. A liveness record's finding is never produced, because its mark
// made the symbol live before the sweep, so the sweep's answer of which marks held a
// symbol back stands for it: read for a live code that is no part kind, whose shape
// code, where it names one, is the declaration's own, and for the first record in
// Marks' reading order that bound the declaration under the code.
func (in *pass) inEffect(at int) bool {
	if in.dialed.matched[at] {
		return true
	}
	mark := &in.Marks[at]
	if mark.Bound == "" || !in.suppressed[mark.Bound] || !mark.Code.Live() || mark.Code.Subject() == vocab.SubjectPart {
		return false
	}
	if symbol := in.symbol(mark.Bound); symbol != nil && shapeCodes[mark.Code] {
		if code := in.shapeCode(symbol); code != "" && code != mark.Code {
			return false
		}
	}
	return in.firstToBind(at)
}

// shapeCodes are the liveness codes the shape of a declaration alone decides: the
// codes another kind derives its candidates from.
var shapeCodes = func() map[vocab.Code]bool {
	held := map[vocab.Code]bool{}
	for _, kind := range vocab.Kinds() {
		for _, code := range kind.DerivedFrom {
			held[code] = true
		}
	}
	return held
}()

// firstToBind reports whether no record before this one in the run's reading order
// bound the same declaration under the same code.
//
// The order is the one Marks carries, which the composition root reads the documents
// in: the inline directives by position, then the ignore file in document order, then
// the baseline. It is a documented order rather than a file order, so two runs over
// one tree answer the same way and a maintainer reading the rule knows which of two
// records to delete.
func (in *pass) firstToBind(at int) bool {
	mark := &in.Marks[at]
	for i := range in.Marks[:at] {
		if earlier := &in.Marks[i]; earlier.Code == mark.Code && earlier.Bound == mark.Bound {
			return false
		}
	}
	return true
}

// dormant reports whether the record at one place in Marks is silent rather than in
// effect or stale. A record naming a code no kind holds is never dormant; one whose
// only match is a finding a type error skips, produced or withheld by its mark,
// always is. A record that withholds findings is dormant when the dials withhold
// every one ([dialed.withholds]). A liveness record's finding was never produced, so
// it is read as [pass.wouldBe] and the component [pass.dormantNear] joins it to. A
// record that matched nothing is dormant only while its own code is set to allow.
func (in *pass) dormant(at int, matched bool) bool {
	mark := &in.Marks[at]
	row, live := vocab.KindOf(mark.Code)
	switch {
	case !live:
		return false
	case !in.dialed.matched[at] && (in.dialed.skipped[at] || matched && in.skippedWithin(mark.Bound)):
		return true
	case in.dialed.matched[at]:
		return !in.dialed.reports[at]
	case mark.Bound == "":
		return false
	case in.Config.EffectiveSeverity(mark.Code) == vocab.Allow:
		return true
	case !matched:
		return false
	case in.dialed.dormant[mark.Bound]:
		return true
	}
	return Withholds(in.Config.Analysis.MinConfidence, in.wouldBe(at, &row))
}

// collapsed turns the stale records into one finding per site, each naming every
// code the site's records carry.
//
// The sites are reported in the order the records were read, which is the order the
// readers return them in: the inline directives by position, then the ignore
// entries in document order, then the baseline rows. The codes of one site are
// named in ascending order, so two runs over one tree name them the same way.
func (in *pass) collapsed(stale []*suppress.Record) ([]draft, error) {
	at := make(map[token.Position][]*suppress.Record, len(stale))
	order := make([]token.Position, 0, len(stale))
	for _, mark := range stale {
		site := token.Position{Filename: mark.Site.Filename, Line: mark.Site.Line, Column: mark.Site.Column}
		if _, held := at[site]; !held {
			order = append(order, site)
		}
		at[site] = append(at[site], mark)
	}

	found := make([]draft, 0, len(order))
	for _, site := range order {
		records := at[site]
		first := records[0]
		one := selfCheck(vocab.StaleSuppression, suppressionSubject,
			in.suppressionRef(first.Symbol, first.Path), first.Site,
			written(first.Mechanism)+" for "+spellCodes(codesAt(records))+" matches no current finding")
		one.Details.Mechanism = first.Mechanism.String()
		one.Details.Entry = &suppressionEntry{
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
func codesAt(records []*suppress.Record) []vocab.Code {
	codes := make([]vocab.Code, 0, len(records))
	for _, mark := range records {
		codes = append(codes, mark.Code)
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

// unmatchedRoots reports every configured root and every configured root pattern
// that named no symbol of the inventory. The subject is the configured string as
// written, the string a maintainer corrects. The finding sits at the first position
// of the document that declared the root, because the roots are an array whose
// members carry no line of their own.
func unmatchedRoots(in *pass) ([]draft, error) {
	if in == nil || in.Config == nil {
		return nil, nil
	}
	at := token.Position{Filename: in.rootsDocument(), Line: documentPosition, Column: documentPosition}
	found := make([]draft, 0, len(in.Unmatched))
	for _, unmatched := range in.Unmatched {
		message := "configured root matches no symbol of the inventory"
		if strings.ContainsAny(unmatched.Source, "*?") {
			message = "configured root pattern matches no symbol of the inventory"
		}
		found = append(found, selfCheck(vocab.UnmatchedRoot, rootSubject, unmatched.Source, at, message))
	}
	return found, nil
}

// rootsDocument is the target-relative path of the document that declared the
// configured roots, and the repository configuration's conventional path where the
// run names no document.
func (in *pass) rootsDocument() string {
	if in.RootsDocument == "" {
		return configurationDocument
	}
	return in.RootsDocument
}

// totals are the two suppression counts a report's envelope prints: the records in
// effect, each one code at one site, and the directives, entries and rows carrying a
// reason, each written once whatever the codes it names. A dormant record counts
// as a reason and not as in effect. It reads the dials' answer, so it runs after
// the pass.
func (in *pass) totals() (inEffect, reasons int) {
	if in.Config == nil {
		return 0, 0
	}
	sites := make(map[token.Position]bool, len(in.Marks))
	for i := range in.Marks {
		mark := &in.Marks[i]
		if mark.Reason != "" {
			sites[token.Position{Filename: mark.Site.Filename, Line: mark.Site.Line, Column: mark.Site.Column}] = true
		}
		if in.inEffect(i) && !in.dormant(i, true) {
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
func selfCheck(code vocab.Code, subject, ref string, site token.Position, message string) draft {
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
func (in *pass) suppressionRef(symbol, path string) string {
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
func (in *pass) fileRef(path string) string {
	if in.Merged == nil {
		return ""
	}
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind == inventory.KindFile && symbol.Pos.Filename == path {
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
func spellCodes(codes []vocab.Code) string {
	spelled := make([]string, len(codes))
	for i, code := range codes {
		spelled[i] = string(code)
	}
	if len(spelled) < 2 {
		return strings.Join(spelled, "")
	}
	return strings.Join(spelled[:len(spelled)-1], ", ") + " and " + spelled[len(spelled)-1]
}

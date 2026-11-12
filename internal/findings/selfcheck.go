package findings

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/v2/internal/inventory"
	core "github.com/cplieger/deadset-spec/v8/suppress"
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
		ref := in.suppressionRef(refused.Symbol, refused.Path)
		message, err := refusalMessage(refused, ref)
		if err != nil {
			return nil, err
		}
		if refused.Reported != code {
			continue
		}
		one := selfCheck(refused.Reported, suppressionSubject, ref, sitePosition(refused.Site), message)
		one.record = refused.Site.Written
		one.Details.Mechanism = string(refused.Mechanism)
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
// the code the suppression named, the subject it suppresses, which is ref, and the
// rule it broke.
func refusalMessage(refused *core.Refusal, ref string) (string, error) {
	named := written(refused.Mechanism) + " for " + string(refused.Code) + " on " + ref
	switch refused.Reported {
	case vocab.SuppressionWithoutReason:
		return named + " carries no reason", nil
	case vocab.UnscopedIgnoreEntry:
		return named + " names a symbol and no path", nil
	default:
		return "", fmt.Errorf("%w: a suppression is refused under %s, which is no refusal of the grammar",
			errEmitter, refused.Reported)
	}
}

// codesAt is every code the records at one site name, in ascending order and each
// once.
func codesAt(records []core.Record) []vocab.Code {
	codes := make([]vocab.Code, 0, len(records))
	for i := range records {
		codes = append(codes, records[i].Code)
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
		message := "configured root " + unmatched.Source + " matches no symbol of the inventory"
		if strings.ContainsAny(unmatched.Source, "*?") {
			message = "configured root pattern " + unmatched.Source + " matches no symbol of the inventory"
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
func written(m core.Mechanism) string {
	switch m {
	case core.Inline:
		return "inline directive"
	case core.Ignore:
		return "ignore entry"
	case core.Baseline:
		return "baseline row"
	default:
		return string(m)
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

// sitePosition is a suppression's site as the position a finding carries.
func sitePosition(at core.Site) token.Position {
	return token.Position{Filename: at.Path, Line: at.Line, Column: at.Column}
}

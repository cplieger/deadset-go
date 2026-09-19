package report

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/deadset-go/internal/catalog"
	"github.com/cplieger/deadset-go/internal/config"
	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/kinds"
)

// The document's own fixed values: the schema every producer names, the version of
// the format, the unit a column counts, the base identifier every location resolves
// against, and the prefix of the automation identifier.
const (
	sarifSchema      = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"
	sarifVersion     = "2.1.0"
	sarifColumnKind  = "utf16CodeUnits"
	sarifURIBaseID   = "%SRCROOT%"
	sarifAutomation  = "deadset/"
	sarifRootComment = "The target root, the directory the analyzer was run on."
)

// The labels a related location carries, one per list a finding names positions in.
const (
	labelImplementation = "implementation"
	labelWrite          = "write"
)

// maxRelatedLocations is the number of related locations a result carries: a
// consumer rejects a result with more than a thousand locations and shows a hundred,
// and the report document stays complete whatever this bound removes.
const maxRelatedLocations = 100

// The levels of a result and the precisions of a rule, mapped from the severity the
// configuration resolved and from the ceiling the kind declares.
const (
	levelError   = "error"
	levelWarning = "warning"
	levelNote    = "note"

	problemRecommendation = "recommendation"

	precisionVeryHigh = "very-high"
	precisionHigh     = "high"
	precisionMedium   = "medium"
)

// SARIF writes the report as one SARIF 2.1.0 log with one run.
//
// The mapping is the Contract's, object by object. A suppressed finding is absent
// from the document, because a matched suppression marks its symbol live before the
// sweep and no finding exists to render; the suppression counts of the totals are
// where that is visible, and this document carries them. A stale suppression is a
// result like any other finding. Every result carries both fingerprint keys: the
// line hash a code-scanning service joins alerts on, and the symbol digest a
// baseline joins on, which a line move leaves unchanged.
//
// The reader of [Options] is what the line hash needs, so a rendering without one is
// refused rather than written with a key that service would recompute differently.
func SARIF(w io.Writer, e *Envelope, opts Options) error {
	if opts.Read == nil {
		return fmt.Errorf("%w: a SARIF rendering reads the source lines its line fingerprint hashes", ErrOptions)
	}
	rules := rulesOf(e.Analyzer.Languages)
	index := make(map[string]int, len(rules))
	for at := range rules {
		index[rules[at].ID] = at
	}

	hashes := &lineHashCache{read: opts.Read, held: make(map[string][]string)}
	results := make([]sarifResult, 0, len(e.Findings)+len(e.StaleSuppressions))
	for i := range e.Findings {
		result, err := findingResult(&e.Findings[i], index, hashes)
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	for i := range e.StaleSuppressions {
		result, err := staleResult(&e.StaleSuppressions[i], index, hashes)
		if err != nil {
			return err
		}
		results = append(results, result)
	}

	log := sarifLog{
		Schema:  sarifSchema,
		Version: sarifVersion,
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:            e.Analyzer.Name,
				Version:         e.Analyzer.Version,
				SemanticVersion: e.Analyzer.Version,
				Rules:           rules,
			}},
			AutomationDetails: sarifAutomationDetails{ID: sarifAutomation + e.languageID() + "/"},
			ColumnKind:        sarifColumnKind,
			OriginalURIBaseIDs: map[string]sarifURIBase{
				sarifURIBaseID: {Description: sarifMessage{Text: sarifRootComment}},
			},
			Results:    results,
			Properties: sarifRunProperties{Totals: e.wire().Totals},
		}},
	}
	encoded, err := json.MarshalIndent(log, "", jsonIndent)
	if err != nil {
		return fmt.Errorf("report: write the SARIF log: %w", err)
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("report: write the SARIF log: %w", err)
	}
	return nil
}

// rulesOf is one rule per live kind of the run's languages, ordered by code, whether
// or not the run holds a result for it and whether or not the configuration enabled
// it: a fixed list keeps a rule's index stable across runs and configurations, which
// is what a consumer of the document reads it by.
//
// A rule carries the kind's code, its name, the severity the vocabulary defaults it
// to and the precision its confidence ceiling maps to. It carries no description,
// because the vocabulary this analyzer links is the table of codes, names,
// severities, ceilings and fixabilities, and the rule text a description renders is
// not in it.
func rulesOf(languages []string) []sarifRule {
	rows := catalog.Kinds()
	rules := make([]sarifRule, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if !claims(row.Languages, languages) {
			continue
		}
		rules = append(rules, sarifRule{
			ID:                   row.Code,
			Name:                 row.Name,
			DefaultConfiguration: sarifConfiguration{Level: levelOf(config.Severity(row.DefaultSeverity))},
			Properties: sarifRuleProperties{
				Precision: precisionOf(row.MaxClass),
				Problem:   sarifProblem{Severity: problemOf(config.Severity(row.DefaultSeverity))},
			},
		})
	}
	slices.SortFunc(rules, func(a, b sarifRule) int { return strings.Compare(a.ID, b.ID) })
	return rules
}

// claims reports whether one kind applies to any language of the run.
func claims(kindLanguages, runLanguages []string) bool {
	for _, language := range runLanguages {
		if slices.Contains(kindLanguages, language) {
			return true
		}
	}
	return false
}

// findingResult is one finding as a result: the rule it reports under, the level its
// severity maps to, its message with a link per related location, its own location,
// the positions it names beyond it, both fingerprints, and every field of the finding
// the mapping does not name, for a consumer that reads the document rather than the
// alert.
func findingResult(found *kinds.Finding, index map[string]int, hashes *lineHashCache) (sarifResult, error) {
	hash, err := hashes.at(found.Position.Path, found.Position.Line)
	if err != nil {
		return sarifResult{}, err
	}
	related := relatedLocations(found)
	properties := resultProperties(found)
	return sarifResult{
		RuleID:    found.Code,
		RuleIndex: index[found.Code],
		Level:     levelOf(found.Severity),
		Message:   sarifMessage{Text: messageWithLinks(found.Message, related)},
		Locations: []sarifLocation{{
			PhysicalLocation: physicalLocation(found.Position.Path,
				found.Position.Line, found.Position.Column, found.Position.EndLine),
		}},
		RelatedLocations: related,
		PartialFingerprints: sarifFingerprints{
			PrimaryLocationLineHash: hash,
			DeadsetSymbolRef:        symbolFingerprint(found.Code, found.Symbol.Ref),
		},
		Properties: &properties,
	}, nil
}

// staleResult is one stale suppression as a result, at the failing level and located
// at the suppression's own site. It names no position beyond its own and carries no
// field bag: the mapping names what a result carries for a finding, and a
// stale-suppression record is a record of another shape.
func staleResult(stale *StaleSuppression, index map[string]int, hashes *lineHashCache) (sarifResult, error) {
	hash, err := hashes.at(stale.Position.Path, stale.Position.Line)
	if err != nil {
		return sarifResult{}, err
	}
	return sarifResult{
		RuleID:    stale.Code,
		RuleIndex: index[stale.Code],
		Level:     levelError,
		Message:   sarifMessage{Text: stale.Message},
		Locations: []sarifLocation{{
			PhysicalLocation: physicalLocation(stale.Position.Path,
				stale.Position.Line, stale.Position.Column, stale.Position.Line),
		}},
		PartialFingerprints: sarifFingerprints{
			PrimaryLocationLineHash: hash,
			DeadsetSymbolRef:        symbolFingerprint(stale.Code, stale.Symbol),
		},
	}, nil
}

// relatedLocations is every position a finding names beyond its own, numbered from
// one in the order the mapping fixes: the implementations of an interface, then the
// positions a subject is written at.
func relatedLocations(found *kinds.Finding) []sarifLocation {
	var held []sarifLocation
	add := func(label, path string, line, column, endLine int) {
		if len(held) >= maxRelatedLocations {
			return
		}
		message := sarifMessage{Text: label}
		held = append(held, sarifLocation{
			ID:               len(held) + 1,
			PhysicalLocation: physicalLocation(path, line, column, endLine),
			Message:          &message,
		})
	}
	for _, one := range found.Details.Implementations {
		add(labelImplementation, one.Position.Path, one.Position.Line, one.Position.Column, one.Position.EndLine)
	}
	for _, at := range found.Details.WritePositions {
		add(labelWrite, at.Path, at.Line, at.Column, at.EndLine)
	}
	return held
}

// messageWithLinks is a result's message text: the finding's message, and where the
// result names a position beyond its own, a link to each, because a consumer shows a
// related location only where the message links to it. The finding's message never
// ends in a full stop, so the clause joins the same sentence.
func messageWithLinks(message string, related []sarifLocation) string {
	if len(related) == 0 {
		return message
	}
	links := make([]string, 0, len(related))
	for i := range related {
		at := &related[i].PhysicalLocation
		links = append(links, "["+related[i].Message.Text+" "+unescaped(at.ArtifactLocation.URI)+":"+
			strconv.Itoa(at.Region.StartLine)+":"+strconv.Itoa(at.Region.StartColumn)+"]("+
			strconv.Itoa(related[i].ID)+")")
	}
	return message + " (see " + strings.Join(links, ", ") + ")"
}

// resultProperties is every field of the finding the mapping does not name, under
// the name the schema gives it and with its value unchanged.
func resultProperties(found *kinds.Finding) sarifResultProperties {
	return sarifResultProperties{
		Language:          found.Language,
		Symbol:            wireSubject(found.Symbol),
		ReachabilityClass: string(found.Class),
		Confidence:        string(found.Confidence),
		LivenessRelation:  relationWritten(found),
		TestOnly:          found.TestOnly,
		Generated:         found.Generated,
		Component:         wireComponent(found.Component),
		RetainedBy:        list(found.RetainedBy),
		Configurations:    list(found.Configurations),
		ConsumersLoaded:   list(found.ConsumersLoaded),
		Fixability:        found.Fixability,
		Details:           wireDetailsOf(&found.Details),
	}
}

// physicalLocation is one position as a location: the path as a relative reference
// against the declared base, and the region from the first character to the end of
// the subject's last line. No end column is written, because a subject that is
// deletable whole runs to the end of that line.
func physicalLocation(path string, line, column, endLine int) sarifPhysical {
	return sarifPhysical{
		ArtifactLocation: sarifArtifactLocation{URI: escapedURI(path), URIBaseID: sarifURIBaseID},
		Region:           sarifRegion{StartLine: line, StartColumn: column, EndLine: endLine},
	}
}

// escapedURI is one target-relative path as a relative reference, each segment
// percent-encoded the way a path segment is.
func escapedURI(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// unescaped is one relative reference back as the path it encodes, which is what a
// link in a message renders, and the reference itself where it does not decode.
func unescaped(uri string) string {
	path, err := url.PathUnescape(uri)
	if err != nil {
		return uri
	}
	return path
}

// levelOf is the level one severity maps to.
func levelOf(severity config.Severity) string {
	switch severity {
	case config.Deny:
		return levelError
	case config.Warn:
		return levelWarning
	default:
		return levelNote
	}
}

// problemOf is the problem severity one severity maps to, which a consumer combines
// with the precision to decide which alerts it shows by default.
func problemOf(severity config.Severity) string {
	switch severity {
	case config.Deny:
		return levelError
	case config.Warn:
		return levelWarning
	default:
		return problemRecommendation
	}
}

// precisionOf is the precision one confidence ceiling maps to.
func precisionOf(ceiling string) string {
	switch kinds.Class(ceiling) {
	case kinds.Certain:
		return precisionVeryHigh
	case kinds.Probable:
		return precisionHigh
	default:
		return precisionMedium
	}
}

// lineHashCache is the line fingerprints of the files one rendering reads, computed
// once per file: a report holds many findings in one file and the procedure walks
// the whole file.
type lineHashCache struct {
	read graph.ReadFile
	held map[string][]string
}

// at is the line fingerprint of one line of one file. A file the rendering cannot
// read, and a line the file does not hold, fail the rendering: a key computed from
// the wrong bytes is worse than no document, because a consumer would open a second
// alert for every finding.
func (c *lineHashCache) at(path string, line int) (string, error) {
	hashes, held := c.held[path]
	if !held {
		content, err := c.read(path)
		if err != nil {
			return "", fmt.Errorf("report: read %s for its line fingerprint: %w", path, err)
		}
		hashes = lineHashes(content)
		c.held[path] = hashes
	}
	if line < 1 || line > len(hashes) {
		return "", fmt.Errorf("report: %s holds %d lines and a finding names line %d",
			path, len(hashes), line)
	}
	return hashes[line-1], nil
}

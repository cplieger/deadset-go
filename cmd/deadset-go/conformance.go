package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	deadsetgo "github.com/cplieger/deadset-go"
	"github.com/cplieger/deadset-go/internal/report"
)

// The two documents this analyzer commits about its own conformance, at the root of
// the repository, named here because a message spells them.
const (
	conformanceGapsFile    = "conformance.json"
	conformanceResultsFile = "conformance-results.json"
)

// corpusVersion is the version of the Conformance Corpus this analyzer answers, which
// is the corpus version the Contract's corpus publishes. It is stated rather than
// read, because no document of the Contract is read at run time; a test pins it equal
// to the published value, the two committed documents are refused where either names
// another version, and the pages that state it are pinned to it.
const corpusVersion = "1.5.0"

// gapsDocument is the shape of the declared-gap document this analyzer reads of its
// own: the corpus version answered and the declined capabilities.
//
// The product object the document carries is not read. It names this analyzer, which
// this command already knows, and a test pins the two equal.
type gapsDocument struct {
	CorpusVersion string           `json:"corpus_version"`
	Gaps          []conformanceGap `json:"gaps"`
}

// conformanceGap is one capability this analyzer declines: the fixture the declension
// applies to, the expectation inside it where the declension is narrower than the
// fixture, the capability itself and the reason a reader of the conformance results
// sees beside it.
type conformanceGap struct {
	Fixture    string `json:"fixture"`
	Symbol     string `json:"symbol,omitempty"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

// resultsAnswer is the two members of the results document a report states: the corpus
// version the run answered and the result over the whole corpus. The rest of the
// document is the per-fixture record, which no report carries.
type resultsAnswer struct {
	CorpusVersion string `json:"corpus_version"`
	Result        string `json:"result"`
}

// conformanceBlock is the conformance object of the Contract's analyzer object, as the
// describe document writes it. A report writes the same three members through the
// report package's own rendering, because a report is that package's document;
// describe's document is this command's own and is written here.
type conformanceBlock struct {
	CorpusVersion string `json:"corpus_version"`
	Result        string `json:"result"`
	Digest        string `json:"digest"`
}

// corpusAnswer is what this analyzer states about itself in every document that names
// it: its result over the Conformance Corpus, the version of the corpus that result is
// against, the digest of the results document its corpus runner wrote, and every
// capability it declines.
//
// The result, the digest and the gaps are read from the two committed documents the
// binary carries, so what a report states and what a reader holding those files can
// recompute are the same bytes. The digest is the SHA-256 of the results document
// exactly as the runner wrote it, which the Contract requires, and a reader recomputes
// it over the committed file.
//
// A document this analyzer cannot decode, and one naming a corpus version this
// analyzer does not answer, are refused rather than patched over: either would make
// every report state a conformance record the analyzer cannot stand behind.
func corpusAnswer() (*corpusRecord, error) {
	var results resultsAnswer
	if err := json.Unmarshal(deadsetgo.Results, &results); err != nil {
		return nil, fmt.Errorf("decode %s: %w", conformanceResultsFile, err)
	}
	var gaps gapsDocument
	if err := json.Unmarshal(deadsetgo.Gaps, &gaps); err != nil {
		return nil, fmt.Errorf("decode %s: %w", conformanceGapsFile, err)
	}
	for document, stated := range map[string]string{
		conformanceResultsFile: results.CorpusVersion,
		conformanceGapsFile:    gaps.CorpusVersion,
	} {
		if stated != corpusVersion {
			return nil, fmt.Errorf("%s names corpus version %s and this analyzer answers %s",
				document, stated, corpusVersion)
		}
	}

	digest := sha256.Sum256(deadsetgo.Results)
	return &corpusRecord{
		conformance: report.Conformance{
			CorpusVersion: corpusVersion,
			Result:        results.Result,
			Digest:        "sha256:" + hex.EncodeToString(digest[:]),
		},
		gaps: reportedGaps(gaps.Gaps),
	}, nil
}

// reportedGaps is the declared gaps as a report names them, in the order the document
// declares them.
func reportedGaps(declared []conformanceGap) []report.DeclaredGap {
	named := make([]report.DeclaredGap, len(declared))
	for i, gap := range declared {
		named[i] = report.DeclaredGap{
			Fixture:    gap.Fixture,
			Symbol:     gap.Symbol,
			Capability: gap.Capability,
			Reason:     gap.Reason,
		}
	}
	return named
}

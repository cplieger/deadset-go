package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	deadsetgo "github.com/cplieger/deadset-go/v2"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// The two documents this analyzer commits about its own conformance, at the root of
// the repository, named here because a message spells them.
const (
	conformanceGapsFile    = "conformance.json"
	conformanceResultsFile = "conformance-results.json"
)

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

// selfOf is what this analyzer states about itself in every document that names it,
// read from the two committed documents the binary carries, so a report states what
// a reader holding those files recomputes: the digest is the SHA-256 of the results
// document as the runner wrote it, as the Contract requires. A document it cannot
// decode, or one naming another corpus version, is refused.
func selfOf() (*run.Self, error) {
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
		if stated != vocab.CorpusVersion {
			return nil, fmt.Errorf("%s names corpus version %s and this analyzer answers %s",
				document, stated, vocab.CorpusVersion)
		}
	}

	digest := sha256.Sum256(deadsetgo.Results)
	declared := make([]report.DeclaredGap, len(gaps.Gaps))
	for i, gap := range gaps.Gaps {
		declared[i] = report.DeclaredGap{Fixture: gap.Fixture, Symbol: gap.Symbol, Capability: gap.Capability, Reason: gap.Reason}
	}
	return &run.Self{
		Analyzer: report.Analyzer{
			Name:                   name,
			Version:                version(),
			Languages:              []vocab.Language{language},
			SchemaVersionsAccepted: vocab.SchemaVersions(),
			Conformance: report.Conformance{
				CorpusVersion: vocab.CorpusVersion,
				Result:        report.Result(results.Result),
				Digest:        "sha256:" + hex.EncodeToString(digest[:]),
			},
		},
		Gaps: declared,
	}, nil
}

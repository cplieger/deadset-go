package main

import (
	"fmt"
	"io"

	"github.com/cplieger/deadset-go/v2/internal/jsondoc"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// describeDocument is what describe writes: the analyzer, the Contract version it
// implements, the report schema versions it reads, the languages it claims and its
// corpus result, which the orchestrator's handshake admits it by.
//
//nolint:govet // fieldalignment: encoding/json writes fields in declaration order, and this is the order the Contract's analyzer object declares these members in
type describeDocument struct {
	Name                   string             `json:"name"`
	Version                string             `json:"version"`
	ContractVersion        string             `json:"contract_version"`
	SchemaVersionsAccepted []string           `json:"schema_versions_accepted"`
	Languages              []vocab.Language   `json:"languages"`
	Conformance            report.Conformance `json:"conformance"`
	FactsVersionWritten    string             `json:"facts_version_written"`
}

// describe writes one JSON object naming this analyzer to stdout and nothing to
// stderr.
func describe(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "deadset-go: describe takes no argument, got %q\n", args[0])
		fmt.Fprintln(stderr, usage)
		return vocab.ExitUsage
	}

	self, err := selfOf()
	if err != nil {
		return fail(stderr, err)
	}
	encoded, err := jsondoc.Encode(describeDocument{
		Name:                   self.Analyzer.Name,
		Version:                self.Analyzer.Version,
		ContractVersion:        vocab.ContractVersion,
		SchemaVersionsAccepted: self.Analyzer.SchemaVersionsAccepted,
		Languages:              self.Analyzer.Languages,
		Conformance:            self.Analyzer.Conformance,
		FactsVersionWritten:    vocab.FactsVersion,
	}, "  ")
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := stdout.Write(encoded); err != nil {
		return unwrittenAnswer(stderr, "describe", err)
	}
	return vocab.ExitClean
}

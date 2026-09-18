package suppress

import (
	"encoding/json"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec"
)

// The published documents that fix the three value forms an entry names.
const (
	referencePage = "contract/grammar/symbol-ref.md"
	referenceFile = "contract/grammar/symbol-ref-corpus.json"
	findingSchema = "contract/finding.schema.json"
)

// referenceCase is one case of the published symbol-reference corpus.
type referenceCase struct {
	Input    string `json:"input"`
	Language string `json:"language"`
	Form     string `json:"form"`
	Reason   string `json:"reason"`
	Accepted bool   `json:"accepted"`
}

func TestPublishedGrammarCarriesEveryReferenceExpression(t *testing.T) {
	page := readPage(t, referencePage)
	for i, re := range referenceExpressions {
		t.Run(referenceForms[i], func(t *testing.T) {
			if !strings.Contains(page, "\n"+re.String()+"\n") {
				t.Errorf("%s carries no line equal to the expression the template %s expands to:\n%s",
					referencePage, referenceForms[i], re.String())
			}
		})
	}
}

func TestReferenceFormClassifiesEveryPublishedCorpusCase(t *testing.T) {
	body, err := spec.Contract.ReadFile(referenceFile)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", referenceFile, err)
	}
	var cases []referenceCase
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatalf("Setup: decode %s: %v", referenceFile, err)
	}
	if len(cases) == 0 {
		t.Fatalf("Setup: %s holds no case", referenceFile)
	}

	for _, c := range cases {
		t.Run(c.Language+" "+c.Form+" "+c.Input, func(t *testing.T) {
			if got := referenceForm(c.Input); got != c.Accepted {
				t.Errorf("referenceForm(%q) = %t, want %t: %s", c.Input, got, c.Accepted, c.Reason)
			}
		})
	}
}

// schemaPatterns are the two value forms the published finding schema fixes,
// read from the schema so a transcription that drifts fails here.
func schemaPatterns(t *testing.T) (code, path string) {
	t.Helper()
	body, err := spec.Contract.ReadFile(findingSchema)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", findingSchema, err)
	}

	var schema struct {
		Defs struct {
			RelativePath struct {
				Pattern string `json:"pattern"`
			} `json:"relative_path"`
		} `json:"$defs"`
		Properties struct {
			Details struct {
				Properties struct {
					Entry struct {
						Properties struct {
							Code struct {
								Pattern string `json:"pattern"`
							} `json:"code"`
						} `json:"properties"`
					} `json:"entry"`
				} `json:"properties"`
			} `json:"details"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("Setup: decode %s: %v", findingSchema, err)
	}
	return schema.Properties.Details.Properties.Entry.Properties.Code.Pattern, schema.Defs.RelativePath.Pattern
}

func TestPublishedSchemaCarriesTheCodeAndPathExpressions(t *testing.T) {
	code, path := schemaPatterns(t)
	if code == "" || path == "" {
		t.Fatalf("Setup: %s carries the code expression %q and the path expression %q, want both", findingSchema, code, path)
	}
	if got := codeForm.String(); got != code {
		t.Errorf("the transcribed code expression is\n%s\nand %s carries\n%s", got, findingSchema, code)
	}
	if got := pathForm.String(); got != path {
		t.Errorf("the transcribed path expression is\n%s\nand %s carries\n%s", got, findingSchema, path)
	}
}

func TestExpandReferenceSubstitutesABlockWrittenFromOthers(t *testing.T) {
	// TS_HEAD is written from two other blocks, so a single pass over the block
	// table would leave their names in the expansion.
	const template = `^ts://TS_PACKAGE/TS_FILE#TS_HEAD$`
	expanded := expandReference(template)
	for _, b := range referenceBlocks {
		if strings.Contains(expanded, b.name) {
			t.Errorf("expandReference(%s) = %s, which still carries the block name %s", template, expanded, b.name)
		}
	}
}

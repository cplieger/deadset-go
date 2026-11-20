package inventory

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	spec "github.com/cplieger/deadset-spec/v8"
)

// segmentCase is one case of the Contract's segment corpus.
type segmentCase struct {
	Set       string `json:"set"`
	Spelled   string `json:"spelled"`
	RawHex    string `json:"raw_hex"`
	Reason    string `json:"reason"`
	Canonical bool   `json:"canonical"`
}

// canonicalSegments is every canonical case of one set of the segment corpus.
func canonicalSegments(t *testing.T, set string) []segmentCase {
	t.Helper()
	const path = "contract/grammar/segment-corpus.json"
	body, err := spec.Contract.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", path, err)
	}
	var cases []segmentCase
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatalf("Setup: decode %s: %v", path, err)
	}
	held := make([]segmentCase, 0, len(cases))
	for _, c := range cases {
		if c.Set == set && c.Canonical {
			held = append(held, c)
		}
	}
	if len(held) == 0 {
		t.Fatalf("Setup: %s holds no canonical %s case", path, set)
	}
	return held
}

func rawOf(t *testing.T, c segmentCase) string {
	t.Helper()
	raw, err := hex.DecodeString(c.RawHex)
	if err != nil {
		t.Fatalf("Setup: the case %q carries raw_hex %q: %v", c.Spelled, c.RawHex, err)
	}
	return string(raw)
}

func TestRefSpellsAnImportPathElementAsThePathSegmentsSectionStates(t *testing.T) {
	t.Parallel()
	for _, c := range canonicalSegments(t, "go-element") {
		element := rawOf(t, c)
		want := "go://example.com/" + c.Spelled + "/sub#Run"
		if got := Ref(KindFunc, "example.com/"+element+"/sub", []string{"Run"}); got != want {
			t.Errorf("Ref(func, %q, Run) = %q, want %q: %s", "example.com/"+element+"/sub", got, want, c.Reason)
		}
		if got := Ref(KindPackage, element, nil); got != "go://"+c.Spelled+"#" {
			t.Errorf("Ref(package, %q) = %q, want %q: %s", element, got, "go://"+c.Spelled+"#", c.Reason)
		}
	}
}

func TestRefSpellsAFileNameAsAFileOperand(t *testing.T) {
	t.Parallel()
	for _, c := range canonicalSegments(t, "file-operand") {
		name := rawOf(t, c)
		want := "go://example.com/c%2B%2B#" + c.Spelled + ":file"
		if got := Ref(KindFile, "example.com/c++", []string{name}); got != want {
			t.Errorf("Ref(file, example.com/c++, %q) = %q, want %q: %s", name, got, want, c.Reason)
		}
	}
}

func TestRefSpellsATypeParameterAndAMemberUnderAnEncodedScope(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind  SymbolKind
		chain []string
		want  string
	}{
		{KindTypeParam, []string{"List", "Map", "T"}, "go://example.com/c%2B%2B#List.Map[T]"},
		{KindMethod, []string{"List", "Push"}, "go://example.com/c%2B%2B#List.Push"},
		{KindPackage, []string{"cpp"}, "go://example.com/c%2B%2B#"},
	} {
		if got := Ref(c.kind, "example.com/c++", c.chain); got != c.want {
			t.Errorf("Ref(%s, example.com/c++, %q) = %q, want %q", c.kind, c.chain, got, c.want)
		}
	}
}

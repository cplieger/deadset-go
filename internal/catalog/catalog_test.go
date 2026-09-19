package catalog

import (
	"encoding/json"
	"slices"
	"testing"

	spec "github.com/cplieger/deadset-spec"
)

// kindsPath is the vocabulary document this package's table is pinned equal to.
const kindsPath = "contract/kinds.json"

// vocabulary is the part of the vocabulary document this package carries: the live
// rows, the retired codes, and the value sets a row's members are drawn from.
type vocabulary struct {
	Languages           []string `json:"languages"`
	Severities          []string `json:"severities"`
	ReachabilityClasses []string `json:"reachability_classes"`
	Fixabilities        []string `json:"fixabilities"`
	Kinds               []struct {
		Code            string   `json:"code"`
		Name            string   `json:"name"`
		Languages       []string `json:"languages"`
		DefaultSeverity string   `json:"default_severity"`
		MaxClass        string   `json:"max_class"`
		Fixability      string   `json:"fixability"`
		DefaultEnabled  bool     `json:"default_enabled"`
		Fixed           bool     `json:"fixed"`
	} `json:"kinds"`
	Retired []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"retired"`
}

// published decodes the vocabulary document from the pinned contract.
func published(t *testing.T) vocabulary {
	t.Helper()

	body, err := spec.Contract.ReadFile(kindsPath)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", kindsPath, err)
	}
	var document vocabulary
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode %s: %v", kindsPath, err)
	}
	if len(document.Kinds) == 0 {
		t.Fatalf("Setup: %s publishes no live kind, so this test pins nothing", kindsPath)
	}
	return document
}

func TestKindsIsTheVocabularysLiveRowsInItsOrder(t *testing.T) {
	document := published(t)
	held := Kinds()

	if len(held) != len(document.Kinds) {
		t.Fatalf("Kinds() returned %d rows, want %d, the live rows of %s",
			len(held), len(document.Kinds), kindsPath)
	}
	for i, row := range document.Kinds {
		got := held[i]
		want := Row{
			Code:            row.Code,
			Name:            row.Name,
			Languages:       row.Languages,
			DefaultSeverity: row.DefaultSeverity,
			MaxClass:        row.MaxClass,
			Fixability:      row.Fixability,
			DefaultEnabled:  row.DefaultEnabled,
			Fixed:           row.Fixed,
		}
		if got.Code != want.Code || got.Name != want.Name ||
			!slices.Equal(got.Languages, want.Languages) ||
			got.DefaultSeverity != want.DefaultSeverity || got.MaxClass != want.MaxClass ||
			got.Fixability != want.Fixability || got.DefaultEnabled != want.DefaultEnabled ||
			got.Fixed != want.Fixed {
			t.Errorf("Kinds()[%d] = %+v, want %+v, the row %s publishes", i, got, want, kindsPath)
		}
	}
}

func TestKindAnswersALiveCodeAndRefusesARetiredOne(t *testing.T) {
	document := published(t)

	for _, row := range document.Kinds {
		got, live := Kind(row.Code)
		if !live {
			t.Errorf("Kind(%q) reported no live kind, want the row %s publishes", row.Code, kindsPath)
			continue
		}
		if got.Name != row.Name || got.MaxClass != row.MaxClass || got.Fixability != row.Fixability {
			t.Errorf("Kind(%q) = name %q, max class %q, fixability %q, want %q, %q, %q",
				row.Code, got.Name, got.MaxClass, got.Fixability, row.Name, row.MaxClass, row.Fixability)
		}
	}
	if len(document.Retired) == 0 {
		t.Fatalf("Setup: %s records no retired code, so the refusal is untested", kindsPath)
	}
	for _, row := range document.Retired {
		if got, live := Kind(row.Code); live {
			t.Errorf("Kind(%q) = %+v, want no live kind: %s retires the code", row.Code, got, kindsPath)
		}
	}
	if got, live := Kind("DS9999"); live {
		t.Errorf("Kind(%q) = %+v, want no live kind: the vocabulary never held the code", "DS9999", got)
	}
}

func TestEveryRowsMembersAreDrawnFromTheVocabularysValueSets(t *testing.T) {
	document := published(t)

	for _, row := range Kinds() {
		for _, language := range row.Languages {
			if !slices.Contains(document.Languages, language) {
				t.Errorf("Kind(%q) names language %q, which %s does not declare",
					row.Code, language, kindsPath)
			}
		}
		if len(row.Languages) == 0 {
			t.Errorf("Kind(%q) names no language, so nothing decides which analyzer reports it", row.Code)
		}
		if !slices.Contains(document.Severities, row.DefaultSeverity) {
			t.Errorf("Kind(%q) defaults to severity %q, which %s does not declare",
				row.Code, row.DefaultSeverity, kindsPath)
		}
		if !slices.Contains(document.ReachabilityClasses, row.MaxClass) {
			t.Errorf("Kind(%q) caps confidence at %q, which %s does not declare",
				row.Code, row.MaxClass, kindsPath)
		}
		if !slices.Contains(document.Fixabilities, row.Fixability) {
			t.Errorf("Kind(%q) is fixability %q, which %s does not declare",
				row.Code, row.Fixability, kindsPath)
		}
	}
}

func TestAReadOfOneRowCannotChangeWhatTheNextReaderSees(t *testing.T) {
	const code = "DS1001"

	first, live := Kind(code)
	if !live {
		t.Fatalf("Setup: Kind(%q) reported no live kind", code)
	}
	if len(first.Languages) == 0 {
		t.Fatalf("Setup: Kind(%q) names no language", code)
	}
	first.Languages[0] = "changed"

	second, _ := Kind(code)
	if second.Languages[0] == "changed" {
		t.Errorf("Kind(%q).Languages[0] = %q after a caller changed its own copy, want the vocabulary's value",
			code, second.Languages[0])
	}
}

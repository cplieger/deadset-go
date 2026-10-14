package report

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/kinds"
)

// A component listed in full is written once, at the report's level, and no finding of
// it carries the members.
func TestTheDocumentListsAComponentsMembersOnce(t *testing.T) {
	members := []kinds.Positioned{
		{Ref: "go://example.com/app#F", Name: "F", Position: kinds.Position{Path: "a.go", Line: 1, Column: 6, EndLine: 1}},
		{Ref: "go://example.com/app#g", Name: "g", Position: kinds.Position{Path: "a.go", Line: 3, Column: 6, EndLine: 3}},
	}
	finding := func(ref, name string, line int, root bool) kinds.Finding {
		return kinds.Finding{
			Code: "DS1001", Kind: "unused-exported", Language: "go",
			Position:  kinds.Position{Path: "a.go", Line: line, Column: 6, EndLine: line},
			Symbol:    kinds.Subject{Ref: ref, Kind: "function", Name: name, SizeLines: 1},
			Message:   "exported function has no reference in the target",
			Component: kinds.Component{ID: "deadset-go/c-1", Root: root, SymbolCount: 2, DeletableLines: 2, Members: members},
		}
	}
	in := minimalInput()
	in.Result.Findings = []kinds.Finding{finding("go://example.com/app#F", "F", 1, true), finding("go://example.com/app#g", "g", 3, false)}
	envelope := built(t, &in)

	var document struct {
		Findings []struct {
			Component map[string]json.RawMessage `json:"component"`
		} `json:"findings"`
		Components []struct {
			ID      string            `json:"id"`
			Members []json.RawMessage `json:"members"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rendered(t, "json", &envelope, Options{}), &document); err != nil {
		t.Fatalf("json.Unmarshal(a report document) = error %v, want its members", err)
	}
	if len(document.Components) != 1 || document.Components[0].ID != "deadset-go/c-1" || len(document.Components[0].Members) != 2 {
		t.Errorf("the document lists components %+v, want deadset-go/c-1 once with its 2 members", document.Components)
	}
	for i, found := range document.Findings {
		if _, held := found.Component["members"]; held {
			t.Errorf("finding %d writes its component's members, want them listed once at the report's level", i)
		}
	}
}

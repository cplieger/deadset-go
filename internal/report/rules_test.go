package report

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	spec "github.com/cplieger/deadset-spec/v5"
)

// publishedRuleTexts is the rule and the precondition of every live kind of the
// vocabulary document, by code.
func publishedRuleTexts(t *testing.T) map[string]ruleText {
	t.Helper()

	body, err := spec.Contract.ReadFile("contract/kinds.json")
	if err != nil {
		t.Fatalf("Setup: read contract/kinds.json: %v", err)
	}
	var document struct {
		Kinds []struct {
			Code         string `json:"code"`
			Rule         string `json:"rule"`
			Precondition string `json:"precondition"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("Setup: decode contract/kinds.json: %v", err)
	}
	if len(document.Kinds) == 0 {
		t.Fatal("Setup: contract/kinds.json publishes no live kind, so this test pins nothing")
	}
	published := make(map[string]ruleText, len(document.Kinds))
	for _, kind := range document.Kinds {
		published[kind.Code] = ruleText{rule: kind.Rule, precondition: kind.Precondition}
	}
	return published
}

func TestTheRuleTextsAreTheVocabularys(t *testing.T) {
	t.Parallel()

	published := publishedRuleTexts(t)
	if got, want := slices.Sorted(maps.Keys(ruleTexts)), slices.Sorted(maps.Keys(published)); !slices.Equal(got, want) {
		t.Fatalf("ruleTexts holds the codes %v, want the live kinds of contract/kinds.json %v", got, want)
	}
	for code, want := range published {
		if got := ruleTexts[code]; got != want {
			t.Errorf("ruleTexts[%s] = %+v, want contract/kinds.json's %+v", code, got, want)
		}
	}
}

func TestTheShortDescriptionIsTheFirstSentence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, rule, want string
	}{
		{name: "two_sentences", rule: "One thing. Another thing.", want: "One thing."},
		{name: "a_stop_inside_a_word", rule: "A go.mod require. Another.", want: "A go.mod require."},
		{name: "one_sentence", rule: "One thing.", want: "One thing."},
		{name: "no_stop", rule: "One thing", want: "One thing"},
		{name: "a_stop_before_a_line_break", rule: "One thing.\nAnother.", want: "One thing.\nAnother."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := (ruleText{rule: tc.rule}).shortDescription(); got != tc.want {
				t.Errorf("shortDescription(%q) = %q, want %q", tc.rule, got, tc.want)
			}
		})
	}
}

func TestTheHelpAppendsThePreconditionAfterABlankLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text ruleText
		want string
	}{
		{name: "no_precondition", text: ruleText{rule: "The rule."}, want: "The rule."},
		{
			name: "a_precondition",
			text: ruleText{rule: "The rule.", precondition: "The condition."},
			want: "The rule.\n\nThe condition.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.text.help(); got != tc.want {
				t.Errorf("%+v.help() = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

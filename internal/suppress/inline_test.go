package suppress

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

// grammarPage is the page that fixes the suppression grammar.
const grammarPage = "contract/grammar/suppression.md"

// expressions are the three expressions of the inline grammar, transcribed from
// that page and named by the step of the decision procedure each one is.
func expressions() map[string]string {
	return map[string]string{
		"candidate":   directiveCandidate.String(),
		"well-formed": directiveWellFormed.String(),
		"no-reason":   directiveNoReason.String(),
	}
}

// outcomes maps one rule of the corpus vocabulary to what the decision procedure
// makes of a case under it: the verdict for an accepted case and the verdict for a
// refused one. A rule whose refused case is well formed all the same, so that the
// binding is what refuses it, wants a directive in both columns.
func outcomes() map[string][2]verdict {
	return map[string][2]verdict{
		"spelling-no-space": {verdictDirective, verdictDirective},
		"spelling-space":    {verdictDirective, verdictDirective},
		"reason-text":       {verdictDirective, verdictDirective},
		"code-list":         {verdictDirective, verdictMalformed},
		"namespace":         {verdictDirective, verdictNone},
		"first-token":       {verdictDirective, verdictNone},
		"line-comment-only": {verdictDirective, verdictNone},
		"directive-name":    {verdictDirective, verdictMalformed},
		"reason-separator":  {verdictDirective, verdictMalformed},
		"reason-required":   {verdictDirective, verdictNoReason},
		"line-above":        {verdictDirective, verdictDirective},
	}
}

// lineAbove is the only line offset the grammar accepts: the comment sits on the
// line immediately above the declaration it targets.
const lineAbove = -1

func TestPublishedGrammarCarriesEveryInlineExpression(t *testing.T) {
	page := readPage(t, grammarPage)
	for step, expression := range expressions() {
		t.Run(step, func(t *testing.T) {
			if !strings.Contains(page, "\n"+expression+"\n") {
				t.Errorf("%s carries no line equal to the expression transcribed for the %s step:\n%s", grammarPage, step, expression)
			}
		})
	}
}

func TestClassifyDecidesEveryPublishedCorpusCase(t *testing.T) {
	for _, c := range readCorpus(t, "inline") {
		var in inlineInput
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("Setup: decode the input of the %s case: %v", c.Rule, err)
		}
		t.Run(c.Rule+" "+in.Comment, func(t *testing.T) {
			outcome, declared := outcomes()[c.Rule]
			if !declared {
				t.Fatalf("the corpus names rule %s and no outcome is declared for it: %s", c.Rule, c.Reason)
			}
			want := outcome[1]
			if c.Accepted {
				want = outcome[0]
			}
			is, codes, reason := classify(in.Comment)
			if is != want {
				t.Errorf("classify(%q) = %v, want %v: %s", in.Comment, is, want, c.Reason)
			}
			if c.Accepted && (len(codes) == 0 || reason == "") {
				t.Errorf("classify(%q) = %v with codes %v and reason %q, want at least one code and a reason: %s",
					in.Comment, is, codes, reason, c.Reason)
			}
			if got := is == verdictDirective && in.LineOffset == lineAbove; got != c.Accepted {
				t.Errorf("the comment %q at line offset %d is a bound suppression = %t, want %t: %s",
					in.Comment, in.LineOffset, got, c.Accepted, c.Reason)
			}
		})
	}
}

func TestClassifyHasAnArmForEveryPublishedRuleAndNoneTheCorpusLacks(t *testing.T) {
	exercised := make(map[string]bool)
	for _, c := range readCorpus(t, "inline") {
		exercised[c.Rule] = true
	}
	if got, want := slices.Sorted(maps.Keys(outcomes())), slices.Sorted(maps.Keys(exercised)); !slices.Equal(got, want) {
		t.Errorf("the declared outcomes cover %v, and the published corpus exercises %v", got, want)
	}
}

func TestClassifyReadsTheTwoCodesOfOneDirectiveAsTwo(t *testing.T) {
	const comment = "//deadset:ignore DS1001,DS1101 -- one reason for both codes."
	is, codes, reason := classify(comment)
	if is != verdictDirective {
		t.Fatalf("classify(%q) = %v, want %v", comment, is, verdictDirective)
	}
	if want := []string{"DS1001", "DS1101"}; !slices.Equal(codes, want) {
		t.Errorf("classify(%q) codes = %v, want %v", comment, codes, want)
	}
	if want := "one reason for both codes."; reason != want {
		t.Errorf("classify(%q) reason = %q, want %q", comment, reason, want)
	}
}

// boundRefs lists, per record reason, the references of the declarations the
// records bound, in the order the read returned them. A reason names one case of
// the fixture, so a table keys on the case rather than on a line number.
func boundRefs(records []Record) map[string][]string {
	bound := make(map[string][]string, len(records))
	for _, r := range records {
		bound[r.Reason] = append(bound[r.Reason], r.Symbol)
	}
	return bound
}

func TestInlineBindsADirectiveToEveryDeclarationOnTheLineBelowIt(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	records, _, err := Inline(result, resolverOf(t, result, root, symbols), symbols)
	if err != nil {
		t.Fatalf("Inline(directives.txtar) error: %v", err)
	}
	bound := boundRefs(records)

	const pkg = "go://example.com/directives#"
	cases := map[string][]string{
		"above the function": {pkg + "Resolve"},
		"at the end of the doc comment, where a formatter puts it": {pkg + "Aliases"},
		"above the field":                                                          {pkg + "Catalog.entries"},
		"above the interface method":                                               {pkg + "Fetcher.Fetch"},
		"above the group's first specification":                                    {pkg + "limit"},
		"above a specification declaring two names":                                {pkg + "first", pkg + "second"},
		"the tab spelling, above the constant":                                     {pkg + "bound"},
		"the space spelling, above the generic function":                           {pkg + "Best", pkg + "Best[T]"},
		"two lines above the function, with a blank line between":                  {""},
		"above the doc comment, which is not a token of the declaration":           {""},
		"above the line that opens the group":                                      {""},
		"on the declaration's own line":                                            {""},
		"on the line directly below a declaration":                                 {""},
		"above a local, which the inventory holds no declaration for":              {""},
		"above the package clause, which no declaration of the language begins at": {""},
	}
	for reason, want := range cases {
		t.Run(reason, func(t *testing.T) {
			if got := bound[reason]; !slices.Equal(got, want) {
				t.Errorf("the directive %q bound %v, want %v", reason, got, want)
			}
		})
	}

	// The method case names two codes, so it is two records with one reason and
	// one binding, which the table above cannot state.
	const method = "two codes above the method"
	if got, want := bound[method], []string{pkg + "Catalog.Reset", pkg + "Catalog.Reset"}; !slices.Equal(got, want) {
		t.Errorf("the directive %q bound %v, want %v", method, got, want)
	}
	if got, want := codesOfReason(records, method), []string{"DS1001", "DS1101"}; !slices.Equal(got, want) {
		t.Errorf("the directive %q suppresses %v, want %v", method, got, want)
	}
}

// codesOfReason lists the codes the records carrying one reason suppress.
func codesOfReason(records []Record, reason string) []string {
	var codes []string
	for _, r := range records {
		if r.Reason == reason {
			codes = append(codes, r.Code)
		}
	}
	return codes
}

func TestInlineReadsACommentInAnotherNamespaceAsNothing(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	records, refusals, err := Inline(result, resolverOf(t, result, root, symbols), symbols)
	if err != nil {
		t.Fatalf("Inline(directives.txtar) error: %v", err)
	}
	for _, r := range records {
		if strings.Contains(r.Reason, "another namespace") {
			t.Errorf("Inline(directives.txtar) produced the record %+v for a comment in another namespace", r)
		}
	}
	for _, r := range refusals {
		if r.Code != "DS1001" {
			t.Errorf("Inline(directives.txtar) refusal = %+v, want only the reasonless DS1001 directive of the fixture", r)
		}
	}
}

func TestInlineRefusesADirectiveThatCarriesNoReason(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	records, refusals, err := Inline(result, resolverOf(t, result, root, symbols), symbols)
	if err != nil {
		t.Fatalf("Inline(directives.txtar) error: %v", err)
	}
	if len(refusals) != 1 {
		t.Fatalf("Inline(directives.txtar) returned %d refusals, want 1: %+v", len(refusals), refusals)
	}

	refusal := refusals[0]
	if refusal.Reported != codeNoReason {
		t.Errorf("Inline(directives.txtar) refusal reported %s, want %s", refusal.Reported, codeNoReason)
	}
	if refusal.Code != "DS1001" || refusal.Reason != "" || refusal.Symbol != "" {
		t.Errorf("Inline(directives.txtar) refusal = %+v, want the code named, no reason and no symbol bound", refusal)
	}
	if refusal.Mechanism != MechanismInline || refusal.Path != "catalog.go" {
		t.Errorf("Inline(directives.txtar) refusal = %+v, want mechanism %s in catalog.go", refusal, MechanismInline)
	}

	// The refused directive binds nothing, so no record names the declaration
	// written under it.
	for _, r := range records {
		if r.Site.Line == refusal.Site.Line {
			t.Errorf("Inline(directives.txtar) produced the record %+v for the refused directive, which binds nothing", r)
		}
	}
}

func TestInlineEndsTheReadOnADirectiveNameTheGrammarDoesNotDefine(t *testing.T) {
	result, root, symbols := loaded(t, "malformed.txtar")
	records, refusals, err := Inline(result, resolverOf(t, result, root, symbols), symbols)

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Inline(malformed.txtar) error = %v, want one satisfying errors.Is(err, ErrMalformed)", err)
	}
	if records != nil || refusals != nil {
		t.Errorf("Inline(malformed.txtar) returned %d records and %d refusals with its error, want none of either", len(records), len(refusals))
	}

	var malformed *MalformedError
	if !errors.As(err, &malformed) {
		t.Fatalf("Inline(malformed.txtar) error = %v, want one errors.As reads as *MalformedError", err)
	}
	if malformed.Site.Filename != "catalog.go" || malformed.Site.Line != 3 {
		t.Errorf("Inline(malformed.txtar) refused at %s, want catalog.go:3", malformed.Site)
	}
	if !strings.Contains(malformed.Error(), directiveForm) {
		t.Errorf("Inline(malformed.txtar) error = %q, want it to name the form %q", malformed.Error(), directiveForm)
	}
}

func TestInlineReadsOneCommentPerSourceSiteAcrossPackageVariants(t *testing.T) {
	result, root, symbols := loaded(t, "same-name.txtar")

	// The premise: the fixture's test file makes the load return variants that
	// type-check catalog.go again, so a walk keyed on anything but the source site
	// would read its comments more than once.
	if len(result.Packages) < 3 {
		t.Fatalf("Setup: the load returned %d packages, want the package, its nested package and at least one test variant", len(result.Packages))
	}

	records, _, err := Inline(result, resolverOf(t, result, root, symbols), symbols)
	if err != nil {
		t.Fatalf("Inline(same-name.txtar) error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Inline(same-name.txtar) returned %d records, want 1: %+v", len(records), records)
	}
	if got, want := records[0].Symbol, "go://example.com/samename#Helper"; got != want {
		t.Errorf("Inline(same-name.txtar) bound %q, want %q", got, want)
	}
}

func TestInlineRefusesAReasonlessDirectiveOncePerCodeItNames(t *testing.T) {
	result, root, symbols := loaded(t, "two-codes.txtar")

	records, refusals, err := Inline(result, resolverOf(t, result, root, symbols), symbols)
	if err != nil {
		t.Fatalf("Inline(two-codes.txtar) error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("Inline(two-codes.txtar) produced %+v, want no record: a refused directive binds nothing", records)
	}
	if len(refusals) != 2 {
		t.Fatalf("Inline(two-codes.txtar) returned %d refusals, want 2, one per code the directive names: %+v",
			len(refusals), refusals)
	}
	got := []string{refusals[0].Code, refusals[1].Code}
	if want := []string{"DS1001", "DS1101"}; !slices.Equal(got, want) {
		t.Errorf("Inline(two-codes.txtar) refused the codes %v, want %v in the order the directive names them", got, want)
	}
	for _, refusal := range refusals {
		if refusal.Reported != codeNoReason || refusal.Reason != "" || refusal.Site.Line != 3 {
			t.Errorf("Inline(two-codes.txtar) refusal = %+v, want %s at catalog.go:3 with no reason",
				refusal, codeNoReason)
		}
	}
}

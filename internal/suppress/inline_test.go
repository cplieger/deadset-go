package suppress

import (
	"errors"
	"slices"
	"strings"
	"testing"

	core "github.com/cplieger/deadset-spec/v8/suppress"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// boundRefs lists, per record reason, the references of the declarations the
// records bound, in the order the read returned them. A reason names one case of
// the fixture, so a table keys on the case rather than on a line number.
func boundRefs(records []core.Record) map[string][]string {
	bound := make(map[string][]string, len(records))
	for _, r := range records {
		bound[r.Reason] = append(bound[r.Reason], r.Symbol)
	}
	return bound
}

func TestInlineBindsADirectiveToEveryDeclarationOnTheLineBelowIt(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	records, _, err := Inline(configured(t, result, root, symbols))
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
		"above a parameter inside a wrapped parameter list":                        {pkg + "Wrapped"},
		"above a result inside a wrapped result list":                              {pkg + "Wrapped"},
		"two lines above the function, with a blank line between":                  {""},
		"above the doc comment, which is not a token of the declaration":           {""},
		"above the line that opens the group":                                      {""},
		"on the declaration's own line":                                            {pkg + "Trailing"},
		"after the first of two declarations on consecutive lines":                 {pkg + "TrailingFirst"},
		"after code on the line above a declaration":                               {""},
		"after a closing brace on the line above a declaration":                    {""},
		"after a parameter on its own line of a wrapped parameter list":            {pkg + "Wrapped"},
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
	if got, want := codesOfReason(records, method), []vocab.Code{"DS1001", "DS1501"}; !slices.Equal(got, want) {
		t.Errorf("the directive %q suppresses %v, want %v", method, got, want)
	}
}

// codesOfReason lists the codes the records carrying one reason suppress.
func codesOfReason(records []core.Record, reason string) []vocab.Code {
	var codes []vocab.Code
	for _, r := range records {
		if r.Reason == reason {
			codes = append(codes, r.Code)
		}
	}
	return codes
}

func TestInlineReadsACommentInAnotherNamespaceAsNothing(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	records, refusals, err := Inline(configured(t, result, root, symbols))
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
	records, refusals, err := Inline(configured(t, result, root, symbols))
	if err != nil {
		t.Fatalf("Inline(directives.txtar) error: %v", err)
	}
	if len(refusals) != 1 {
		t.Fatalf("Inline(directives.txtar) returned %d refusals, want 1: %+v", len(refusals), refusals)
	}

	refusal := refusals[0]
	if refusal.Reported != vocab.SuppressionWithoutReason {
		t.Errorf("Inline(directives.txtar) refusal reported %s, want %s", refusal.Reported, vocab.SuppressionWithoutReason)
	}
	if refusal.Code != "DS1001" || refusal.Reason != "" || refusal.Symbol != "" {
		t.Errorf("Inline(directives.txtar) refusal = %+v, want the code named, no reason and no symbol bound", refusal)
	}
	if refusal.Mechanism != core.Inline || refusal.Path != "catalog.go" {
		t.Errorf("Inline(directives.txtar) refusal = %+v, want mechanism %s in catalog.go", refusal, core.Inline)
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
	records, refusals, err := Inline(configured(t, result, root, symbols))

	if !errors.Is(err, core.ErrMalformed) {
		t.Fatalf("Inline(malformed.txtar) error = %v, want one satisfying errors.Is(err, core.ErrMalformed)", err)
	}
	if records != nil || refusals != nil {
		t.Errorf("Inline(malformed.txtar) returned %d records and %d refusals with its error, want none of either", len(records), len(refusals))
	}

	malformed, ok := errors.AsType[*core.MalformedError](err)
	if !ok {
		t.Fatalf("Inline(malformed.txtar) error = %v, want one errors.As reads as *core.MalformedError", err)
	}
	if malformed.Site.Path != "catalog.go" || malformed.Site.Line != 3 {
		t.Errorf("Inline(malformed.txtar) refused at %+v, want catalog.go:3", malformed.Site)
	}
	if want := "want //deadset:ignore <CODE>"; !strings.Contains(malformed.Error(), want) {
		t.Errorf("Inline(malformed.txtar) error = %q, want it to name the form %q", malformed.Error(), want)
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

	records, _, err := Inline(configured(t, result, root, symbols))
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

	records, refusals, err := Inline(configured(t, result, root, symbols))
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
	got := []vocab.Code{refusals[0].Code, refusals[1].Code}
	if want := []vocab.Code{"DS1001", "DS1501"}; !slices.Equal(got, want) {
		t.Errorf("Inline(two-codes.txtar) refused the codes %v, want %v in the order the directive names them", got, want)
	}
	for _, refusal := range refusals {
		if refusal.Reported != vocab.SuppressionWithoutReason || refusal.Reason != "" || refusal.Site.Line != 3 {
			t.Errorf("Inline(two-codes.txtar) refusal = %+v, want %s at catalog.go:3 with no reason",
				refusal, vocab.SuppressionWithoutReason)
		}
	}
}

func TestInlineReadsADirectiveEveryConfigurationCompilesOnce(t *testing.T) {
	result, root, symbols := loaded(t, "directives.txtar")
	once, refusedOnce, err := Inline(configured(t, result, root, symbols))
	if err != nil {
		t.Fatalf("Inline(directives.txtar) error: %v", err)
	}
	_, first := configured(t, result, root, symbols)
	_, second := configured(t, result, root, symbols)
	twice, refusedTwice, err := Inline(2, func(config int) (*Configuration, error) {
		if config == 0 {
			return first(config)
		}
		return second(config)
	})
	if err != nil {
		t.Fatalf("Inline(directives.txtar, twice) error: %v", err)
	}
	if !slices.Equal(once, twice) || !slices.Equal(refusedOnce, refusedTwice) {
		t.Errorf("Inline over two configurations compiling one tree = %+v, %+v, want the one configuration's %+v, %+v",
			twice, refusedTwice, once, refusedOnce)
	}
}

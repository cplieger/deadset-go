package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec"
)

// corpusCase is one case of the published symbol-reference corpus.
type corpusCase struct {
	Input    string `json:"input"`
	Language string `json:"language"`
	Form     string `json:"form"`
	Reason   string `json:"reason"`
	Accepted bool   `json:"accepted"`
}

// readCorpus returns the published corpus cases for Go.
func readCorpus(t *testing.T) []corpusCase {
	t.Helper()
	const path = "contract/grammar/symbol-ref-corpus.json"
	body, err := spec.Contract.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", path, err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatalf("Setup: decode %s: %v", path, err)
	}
	goCases := make([]corpusCase, 0, len(cases))
	for _, c := range cases {
		if c.Language == "go" {
			goCases = append(goCases, c)
		}
	}
	if len(goCases) == 0 {
		t.Fatalf("Setup: %s holds no Go case", path)
	}
	return goCases
}

// goForms are the expressions the published grammar page fixes for Go, one per
// row of its own table, each anchored and each the whole reference. They are
// transcribed rather than parsed out of the page, and
// TestPublishedGrammarCarriesEveryExpression asserts the transcription is
// byte-identical to what the page carries.
var goForms = map[string]string{
	"package":       `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#$`,
	"package-level": `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*$`,
	"member":        `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*(?:\.(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*)+$`,
	"type-parameter": `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*` +
		`(?:\.(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*)?` +
		`\[(?:[A-Za-z_]|[^\x00-\x7F])(?:[A-Za-z0-9_]|[^\x00-\x7F])*\]$`,
	"file":    `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#[^/\\:#\r\n]+\.go:file$`,
	"require": `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*(?:@[A-Za-z0-9.+-]+)?:require$`,
	"replace": `^go://[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*#[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*(?:@[A-Za-z0-9.+-]+)?:replace$`,
}

// corpusForms maps a corpus form to the expression that accepts it; several
// forms share one expression because they differ in what the symbol is rather
// than in how the reference is spelled.
var corpusForms = map[string]string{
	"package":          "package",
	"package-level":    "package-level",
	"method":           "member",
	"interface-method": "member",
	"field":            "member",
	"type-parameter":   "type-parameter",
	"file":             "file",
	"require":          "require",
	"replace":          "replace",
}

// emitterInput is what Ref is called with to produce one corpus case.
type emitterInput struct {
	kind    SymbolKind
	pkgPath string
	chain   []string
}

// emitted gives, for each accepted Go case whose form this analyzer produces,
// the parts Ref is called with. A case the analyzer does not produce is absent,
// and notEmitted below names the forms that covers.
var emitted = map[string]emitterInput{
	"go://example.com/app#":                                       {KindPackage, "example.com/app", nil},
	"go://example.com/app#Resolve":                                {KindFunc, "example.com/app", []string{"Resolve"}},
	"go://example.com/app#normalize":                              {KindFunc, "example.com/app", []string{"normalize"}},
	"go://example.com/app#Catalog":                                {KindType, "example.com/app", []string{"Catalog"}},
	"go://example.com/app#Exact":                                  {KindConst, "example.com/app", []string{"Exact"}},
	"go://example.com/app#ErrNotFound":                            {KindVar, "example.com/app", []string{"ErrNotFound"}},
	"go://example.com/fixture#Ünused":                             {KindFunc, "example.com/fixture", []string{"Ünused"}},
	"go://example.com/app#TestResolve":                            {KindFunc, "example.com/app", []string{"TestResolve"}},
	"go://example.com/app/httpapi_test#TestSearch":                {KindFunc, "example.com/app/httpapi_test", []string{"TestSearch"}},
	"go://example.com/app/cmd/tool#run":                           {KindFunc, "example.com/app/cmd/tool", []string{"run"}},
	"go://example.com/app#Catalog.ResolveAlias":                   {KindMethod, "example.com/app", []string{"Catalog", "ResolveAlias"}},
	"go://example.com/app/internal/queue#List.Push":               {KindMethod, "example.com/app/internal/queue", []string{"List", "Push"}},
	"go://example.com/app#Fetcher.Fetch":                          {KindInterfaceMethod, "example.com/app", []string{"Fetcher", "Fetch"}},
	"go://example.com/app#Catalog.entries":                        {KindField, "example.com/app", []string{"Catalog", "entries"}},
	"go://example.com/app#Buffered.Buffer":                        {KindField, "example.com/app", []string{"Buffered", "Buffer"}},
	"go://example.com/app#Manifest.Tools.Version":                 {KindField, "example.com/app", []string{"Manifest", "Tools", "Version"}},
	"go://example.com/app#Best[T]":                                {KindTypeParam, "example.com/app", []string{"Best", "T"}},
	"go://example.com/app#Catalog.Decode[T]":                      {KindTypeParam, "example.com/app", []string{"Catalog", "Decode", "T"}},
	"go://example.com/app/internal/queue#List.Map[T]":             {KindTypeParam, "example.com/app/internal/queue", []string{"List", "Map", "T"}},
	"go://example.com/app/internal/legacy#render_windows.go:file": {KindFile, "example.com/app/internal/legacy", []string{"render_windows.go"}},
}

// notEmitted are the forms the published grammar defines for a subject no
// symbol kind of this enumeration reaches: the directives of a module file.
var notEmitted = []string{"require", "replace"}

func TestPublishedGrammarCarriesEveryExpression(t *testing.T) {
	const path = "contract/grammar/symbol-ref.md"
	body, err := spec.Contract.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s from the contract: %v", path, err)
	}
	page := string(body)

	for form, expression := range goForms {
		t.Run(form, func(t *testing.T) {
			if !strings.Contains(page, "\n"+expression+"\n") {
				t.Errorf("%s carries no line equal to the expression transcribed for form %s:\n%s", path, form, expression)
			}
		})
	}
}

func TestRefEmitsEveryAcceptedCorpusCase(t *testing.T) {
	for _, c := range readCorpus(t) {
		if !c.Accepted {
			continue
		}
		t.Run(c.Input, func(t *testing.T) {
			in, declared := emitted[c.Input]
			if slices.Contains(notEmitted, c.Form) {
				if declared {
					t.Fatalf("the emitter table declares %q, whose %s form names a module-file directive no symbol kind of this enumeration reaches", c.Input, c.Form)
				}
				return
			}
			if !declared {
				t.Fatalf("the corpus accepts %s (form %s) and no emitter input is declared for it: %s", c.Input, c.Form, c.Reason)
			}
			if got := Ref(in.kind, in.pkgPath, in.chain); got != c.Input {
				t.Errorf("Ref(%s, %q, %q) = %q, want %q", in.kind, in.pkgPath, in.chain, got, c.Input)
			}
		})
	}
}

func TestRefTableCoversNothingTheCorpusRefuses(t *testing.T) {
	accepted := make(map[string]bool)
	for _, c := range readCorpus(t) {
		if c.Accepted {
			accepted[c.Input] = true
		}
	}
	for input := range emitted {
		if !accepted[input] {
			t.Errorf("the emitter table declares %q, which the published corpus does not accept", input)
		}
	}
}

func TestPublishedGrammarClassifiesEveryCorpusCase(t *testing.T) {
	compiled := make(map[string]*regexp.Regexp, len(goForms))
	for form, expression := range goForms {
		re, err := regexp.Compile(expression)
		if err != nil {
			t.Fatalf("Setup: compile the %s expression: %v", form, err)
		}
		compiled[form] = re
	}

	for _, c := range readCorpus(t) {
		t.Run(c.Input, func(t *testing.T) {
			form, ok := corpusForms[c.Form]
			if !ok {
				t.Fatalf("the corpus names form %s and no expression is declared for it", c.Form)
			}
			if got := compiled[form].MatchString(c.Input); got != c.Accepted {
				t.Errorf("the %s expression matches %q = %t, want %t: %s", form, c.Input, got, c.Accepted, c.Reason)
			}
			if !c.Accepted {
				for other, re := range compiled {
					if re.MatchString(c.Input) {
						t.Errorf("the %s expression matches the refused %q: %s", other, c.Input, c.Reason)
					}
				}
			}
		})
	}
}

func TestSymbolsEmitsOnlyReferencesThePublishedGrammarAccepts(t *testing.T) {
	compiled := make([]*regexp.Regexp, 0, len(goForms))
	for form, expression := range goForms {
		re, err := regexp.Compile(expression)
		if err != nil {
			t.Fatalf("Setup: compile the %s expression: %v", form, err)
		}
		compiled = append(compiled, re)
	}

	for _, archive := range []string{"every-kind.txtar", "columns.txtar", "variants.txtar"} {
		for _, s := range symbolsOf(t, archive) {
			if !slices.ContainsFunc(compiled, func(re *regexp.Regexp) bool { return re.MatchString(s.Ref) }) {
				t.Errorf("Symbols(%s)[%s].Ref = %q, which no published expression accepts", archive, s.ID, s.Ref)
			}
		}
	}
}

func TestRefIsTheSameUnderAnotherBuildConfiguration(t *testing.T) {
	dir := extract(t, "configurations.txtar")

	linux, root := loadDir(t, dir, "linux", "amd64")
	windows, _ := loadDir(t, dir, "windows", "arm64")
	if linux.Fset == windows.Fset {
		t.Fatal("Setup: the two configurations share one file set, so positions were not computed independently")
	}

	first, err := Symbols(linux, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(linux-amd64) error: %v", err)
	}
	second, err := Symbols(windows, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols(windows-arm64) error: %v", err)
	}

	// The premise: each configuration selects one of the two constrained files,
	// so the two loads are genuinely different sets of source.
	onlyLinux := "go://example.com/configured#render_linux.go:file"
	onlyWindows := "go://example.com/configured#render_windows.go:file"
	if !slices.Contains(refs(first), onlyLinux) || slices.Contains(refs(first), onlyWindows) {
		t.Fatalf("Symbols(linux-amd64) holds %v, want %s and not %s", refs(first), onlyLinux, onlyWindows)
	}
	if !slices.Contains(refs(second), onlyWindows) || slices.Contains(refs(second), onlyLinux) {
		t.Fatalf("Symbols(windows-arm64) holds %v, want %s and not %s", refs(second), onlyWindows, onlyLinux)
	}

	// Every declaration both configurations reach carries one reference.
	shared := []string{
		"go://example.com/configured#",
		"go://example.com/configured#catalog.go:file",
		"go://example.com/configured#Resolve",
		"go://example.com/configured#render",
	}
	for _, ref := range shared {
		t.Run(ref, func(t *testing.T) {
			if !slices.Contains(refs(first), ref) {
				t.Errorf("Symbols(linux-amd64) holds no %s", ref)
			}
			if !slices.Contains(refs(second), ref) {
				t.Errorf("Symbols(windows-arm64) holds no %s", ref)
			}
		})
	}
}

func TestRefSurvivesAnEditAboveTheDeclarationAndAFileRename(t *testing.T) {
	dir := extract(t, "every-kind.txtar")
	before, root := loadDir(t, dir, "linux", "amd64")
	original, err := Symbols(before, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols before the edit error: %v", err)
	}

	catalog := filepath.Join(dir, "catalog.go")
	body, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", catalog, err)
	}
	edited := strings.Replace(string(body), "package app\n", "// A line nothing declares.\n\npackage app\n", 1)
	if edited == string(body) {
		t.Fatal("Setup: the fixture no longer starts with its package clause, so no edit above the declarations was made")
	}
	if err := os.WriteFile(filepath.Join(dir, "renamed.go"), []byte(edited), 0o600); err != nil {
		t.Fatalf("Setup: write renamed.go: %v", err)
	}
	if err := os.Remove(catalog); err != nil {
		t.Fatalf("Setup: remove %s: %v", catalog, err)
	}

	after, root := loadDir(t, dir, "linux", "amd64")
	moved, err := Symbols(after, root, os.ReadFile)
	if err != nil {
		t.Fatalf("Symbols after the edit error: %v", err)
	}

	if got, want := declarationRefs(moved), declarationRefs(original); !slices.Equal(got, want) {
		t.Errorf("an edit above the declarations and a file rename changed the references\n--- before\n%s\n+++ after\n%s",
			strings.Join(want, "\n"), strings.Join(got, "\n"))
	}

	positionsMoved := false
	byRef := namedByRef(original)
	for _, s := range moved {
		if s.Kind == KindFile || s.Kind == KindPackage {
			continue
		}
		if was, ok := byRef[s.Ref]; ok && was.Pos.Line != s.Pos.Line {
			positionsMoved = true
		}
	}
	if !positionsMoved {
		t.Error("no declaration moved to another line, so the edit did not exercise what the reference is meant to survive")
	}
}

// refs lists every symbol's reference in the order the enumeration returned.
func refs(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Ref)
	}
	return out
}

// declarationRefs lists the references of everything but the files, whose own
// reference carries the name of the file and so changes when a file is renamed.
func declarationRefs(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		if s.Kind != KindFile {
			out = append(out, s.Ref)
		}
	}
	slices.Sort(out)
	return out
}

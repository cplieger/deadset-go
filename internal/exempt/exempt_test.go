package exempt

import (
	"encoding/json"
	"errors"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/graph"
	spec "github.com/cplieger/deadset-spec"
	"golang.org/x/tools/txtar"
)

// goLanguage is the spelling the vocabulary gives this analyzer's language.
const goLanguage = "go"

// vocabulary is the shape of the vocabulary document this package implements.
type vocabulary struct {
	Exemptions []struct {
		Class     string   `json:"class"`
		Languages []string `json:"languages"`
	} `json:"exemptions"`
}

// publishedClasses reads the classes of the embedded vocabulary that run on Go, in
// the order the document lists them.
func publishedClasses(t *testing.T) []Class {
	t.Helper()

	raw, err := spec.Contract.ReadFile("contract/exemptions.json")
	if err != nil {
		t.Fatalf("Setup: read contract/exemptions.json: %v", err)
	}
	var document vocabulary
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("Setup: decode contract/exemptions.json: %v", err)
	}

	published := make([]Class, 0, len(document.Exemptions))
	for _, e := range document.Exemptions {
		if slices.Contains(e.Languages, goLanguage) {
			published = append(published, Class(e.Class))
		}
	}
	if len(published) == 0 {
		t.Fatalf("Setup: contract/exemptions.json names no %s class", goLanguage)
	}
	return published
}

func TestClassesIsTheVocabularyOfTheContract(t *testing.T) {
	// The vocabulary is closed and its order is the order a run computes the
	// classes in, so the list in this package is the published one or a class is
	// silently absent from every run.
	want := publishedClasses(t)
	if got := Classes(); !slices.Equal(got, want) {
		t.Errorf("Classes() = %v, want %v", got, want)
	}
}

func TestClassesHandsOutNoSliceACallerCanChange(t *testing.T) {
	first := Classes()
	first[0] = "changed"

	// A caller that writes into the answer would change what every later caller
	// reads, and a class list one consumer can rewrite is a vocabulary that is no
	// longer closed.
	if got := Classes(); got[0] == "changed" {
		t.Errorf("Classes()[0] = %q after a caller wrote into an earlier answer, want the vocabulary's own first class", got[0])
	}
}

// at is one rendered evidence site, which is what a class records when its
// evidence is a position in the source rather than a type relation.
func at(file string, line, column int) token.Position {
	return token.Position{Filename: file, Line: line, Column: column}
}

// detector returns a class's detection that reads nothing and retains exactly
// what it was given.
func detector(retained ...graph.Exemption) Detector {
	return func(*Input) ([]graph.Exemption, error) { return retained, nil }
}

// held names every exemption of one union: the symbol, the class and the site.
func held(found []graph.Exemption) []string {
	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, string(e.ID)+" "+e.Class+" "+e.Site.String())
	}
	return names
}

func TestComputeRunsEveryClassTheTableHoldsInVocabularyOrder(t *testing.T) {
	var ran []Class
	detectors := make(map[Class]Detector, len(Classes()))
	for _, class := range Classes() {
		detectors[class] = func(*Input) ([]graph.Exemption, error) {
			ran = append(ran, class)
			return nil, nil
		}
	}
	// One class of the vocabulary is absent from the table, which is what a
	// caller assembling the table from the classes it has produces.
	delete(detectors, EnumGroup)

	if _, err := Compute(&Input{}, detectors); err != nil {
		t.Fatalf("Compute over a table of every class error: %v", err)
	}
	want := slices.DeleteFunc(Classes(), func(class Class) bool { return class == EnumGroup })
	if !slices.Equal(ran, want) {
		t.Errorf("Compute ran %v, want %v", ran, want)
	}
}

func TestComputeRunsNoDisabledClass(t *testing.T) {
	retained := graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile)}
	detectors := map[Class]Detector{
		GeneratedFile:         detector(retained),
		InterfaceSatisfaction: detector(graph.Exemption{ID: "a.go:2:1", Class: string(InterfaceSatisfaction)}),
	}

	in := &Input{Options: Options{Disabled: []Class{InterfaceSatisfaction, InterfaceSatisfaction, "not-a-class"}}}
	found, err := Compute(in, detectors)
	if err != nil {
		t.Fatalf("Compute with one class disabled error: %v", err)
	}

	// A disabled class retains nothing, which is what lets an exemption suspected
	// of hiding a defect be tested; a class named twice is disabled once and a
	// name outside the vocabulary disables nothing.
	if want := held([]graph.Exemption{retained}); !slices.Equal(held(found), want) {
		t.Errorf("Compute with %s disabled retained %v, want %v", InterfaceSatisfaction, held(found), want)
	}
}

func TestComputeOrdersTheUnionBySiteThenClassThenSymbol(t *testing.T) {
	detectors := map[Class]Detector{
		GeneratedFile: detector(
			graph.Exemption{ID: "b.go:9:1", Class: string(GeneratedFile), Site: at("b.go", 9, 1)},
			graph.Exemption{ID: "a.go:4:1", Class: string(GeneratedFile), Site: at("a.go", 4, 7)},
			graph.Exemption{ID: "a.go:2:1", Class: string(GeneratedFile)},
		),
		ReflectiveLookup: detector(
			graph.Exemption{ID: "a.go:3:1", Class: string(ReflectiveLookup), Site: at("a.go", 4, 7)},
			graph.Exemption{ID: "a.go:1:1", Class: string(ReflectiveLookup), Site: at("a.go", 4, 7)},
		),
	}

	found, err := Compute(&Input{}, detectors)
	if err != nil {
		t.Fatalf("Compute over two classes error: %v", err)
	}

	// The union reads in one order whatever order the classes produced it in: by
	// the site the evidence was found at, then by the class, then by the symbol. A
	// class whose evidence is a type relation records no site, so its entries come
	// first.
	want := []string{
		"a.go:2:1 generated-file -",
		"a.go:4:1 generated-file a.go:4:7",
		"a.go:1:1 reflective-lookup a.go:4:7",
		"a.go:3:1 reflective-lookup a.go:4:7",
		"b.go:9:1 generated-file b.go:9:1",
	}
	if got := held(found); !slices.Equal(got, want) {
		t.Errorf("Compute over two classes retained %v, want %v", got, want)
	}
}

func TestComputeKeepsOneEntryPerSymbolClassAndDetail(t *testing.T) {
	const converted = "satisfies io.Writer"
	detectors := map[Class]Detector{
		GeneratedFile: detector(
			// One fact stated at three sites, the last of them the earliest.
			graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile), Site: at("b.go", 2, 1), Detail: converted},
			graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile), Site: at("a.go", 9, 4), Detail: converted},
			graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile), Site: at("a.go", 4, 7), Detail: converted},
			// A second reason on the same symbol and class, and the same reason
			// on a second symbol: each is a fact of its own.
			graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile), Site: at("a.go", 9, 4), Detail: "satisfies io.Closer"},
			graph.Exemption{ID: "a.go:2:1", Class: string(GeneratedFile), Site: at("a.go", 9, 4), Detail: converted},
		),
	}

	found, err := Compute(&Input{}, detectors)
	if err != nil {
		t.Fatalf("Compute over one class error: %v", err)
	}

	// One class holding one symbol for one reason is one exemption however many
	// sites the class found it at, and the site kept is the earliest of them; a
	// second reason and a second symbol are each an exemption of their own.
	want := []string{
		"a.go:1:1 generated-file a.go:4:7",
		"a.go:1:1 generated-file a.go:9:4",
		"a.go:2:1 generated-file a.go:9:4",
	}
	if got := held(found); !slices.Equal(got, want) {
		t.Errorf("Compute over five reports of one class retained %v, want %v", got, want)
	}
	if found[0].Detail != converted {
		t.Errorf("Compute kept the detail %q at the earliest site, want %q", found[0].Detail, converted)
	}
}

// goDetectors is the table a caller assembles from every class this analyzer
// implements, which is the table the composition root builds.
func goDetectors() map[Class]Detector {
	return map[Class]Detector{
		InterfaceSatisfaction: InterfaceSatisfactionDetector,
		EncodingReflection:    EncodingReflectionDetector,
		FormatVerbContract:    FormatVerbContractDetector,
		ErrorsDuckTyping:      ErrorsDuckTypingDetector,
		EnumGroup:             EnumGroupDetector,
		GeneratedFile:         GeneratedFileDetector,
		LinknameCgoAsmPlugin:  LinknameCgoAsmPluginDetector,
		TemplateField:         TemplateFieldDetector,
		ReflectiveLookup:      ReflectiveLookupDetector,
	}
}

// retainedClauses names every exemption of one union as the declaration held
// back, the class, the site and the clause, which is the line print-retained
// carries.
func retainedClauses(in *Input, found []graph.Exemption) []string {
	names := symbolNames(in)
	lines := make([]string, 0, len(found))
	for _, e := range found {
		lines = append(lines, names[e.ID]+" "+e.Class+" "+e.Site.String()+" "+e.Detail)
	}
	return lines
}

func TestComputeKeepsOneRecordForAMethodConvertedAtSeveralSites(t *testing.T) {
	in := inputOf(t, "several-sites.txtar", Options{})

	// The class states the fact once per conversion, which is what gives the
	// framework something to collapse: without this the assertion below would
	// hold over a fixture carrying one conversion.
	perSite, err := InterfaceSatisfactionDetector(in)
	if err != nil {
		t.Fatalf("Setup: InterfaceSatisfactionDetector(several-sites.txtar): %v", err)
	}
	const sites = 5
	if got := writerSites(perSite); got != sites {
		t.Fatalf("Setup: the class recorded %s at %d sites, want %d", writerFact, got, sites)
	}

	found, err := Compute(in, goDetectors())
	if err != nil {
		t.Fatalf("Compute(several-sites.txtar) error: %v", err)
	}

	// Write is converted to io.Writer at five sites over two files and the union
	// states that once, at the earliest site by file and line; the conversion to
	// io.WriteCloser is a second reason on the same symbol and class, so it is a
	// record of its own rather than a site the first one swallowed.
	want := []string{
		"(*Sink).Write interface-satisfaction a.go:20:43 satisfies io.Writer",
		"(*Sink).Write interface-satisfaction a.go:29:44 satisfies io.WriteCloser",
		"(*Sink).Close interface-satisfaction a.go:29:44 satisfies io.WriteCloser",
	}
	if got := retainedClauses(in, found); !slices.Equal(got, want) {
		t.Errorf("Compute(several-sites.txtar) retained\n%v\nwant\n%v", got, want)
	}
}

// writerFact is the clause the fixture's own type states at every one of its
// conversions to a writer.
const writerFact = "satisfies io.Writer"

// writerSites counts the exemptions of one union stating writerFact.
func writerSites(found []graph.Exemption) int {
	sites := 0
	for _, e := range found {
		if e.Detail == writerFact {
			sites++
		}
	}
	return sites
}

func TestEveryClassRunsCleanOverAModuleThatHasATestFile(t *testing.T) {
	in := inputOf(t, "with-tests.txtar", Options{})

	// A test load compiles one file the target does not hold, the test binary the
	// toolchain synthesizes, and the load drops it. So every position a class
	// meets renders, and a class returns what it found rather than a failure: the
	// site of a conversion written in a test file is that file.
	found, err := Compute(in, goDetectors())
	if err != nil {
		t.Fatalf("Compute(with-tests.txtar) error: %v", err)
	}
	want := []string{
		"Sink.Write interface-satisfaction app.go:14:34 satisfies io.Writer",
		"(*recorder).Write interface-satisfaction app_test.go:18:20 satisfies io.Writer",
	}
	if got := retainedClauses(in, found); !slices.Equal(got, want) {
		t.Errorf("Compute(with-tests.txtar) retained\n%v\nwant\n%v", got, want)
	}
}

// Under a production run an exemption whose evidence a test file carries holds
// nothing: a test that marshals a value makes no member of it live for production,
// exactly as a test's reference is no reference there. The same fact found in a
// source file holds under both modes, which is what tells the rule from a run that
// retained less for another reason.
func TestComputeDropsAnExemptionATestFileIsTheEvidenceFor(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{
			name: "the plain mode",
			want: []string{
				"Shipped.Name encoding-reflection evidence.go:18:63 passed to encoding/json.Marshal",
				"Shipped.Extra encoding-reflection evidence.go:18:63 passed to encoding/json.Marshal",
				"Fixture.Name encoding-reflection evidence_test.go:9:28 passed to encoding/json.Marshal",
				"Fixture.Extra encoding-reflection evidence_test.go:9:28 passed to encoding/json.Marshal",
			},
		},
		{
			name: "a production run",
			want: []string{
				"Shipped.Name encoding-reflection evidence.go:18:63 passed to encoding/json.Marshal",
				"Shipped.Extra encoding-reflection evidence.go:18:63 passed to encoding/json.Marshal",
			},
		},
	}
	for _, tc := range tests {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			in := inputOf(t, "production-evidence.txtar", Options{Production: tc.name == "a production run"})

			found, err := Compute(in, goDetectors())
			if err != nil {
				t.Fatalf("Compute(production-evidence.txtar, %s) error: %v", tc.name, err)
			}
			if got := retainedClauses(in, found); !slices.Equal(got, tc.want) {
				t.Errorf("Compute(production-evidence.txtar, %s) retained\n%v\nwant\n%v", tc.name, got, tc.want)
			}
		})
	}
}

// A production run keeps the source file's site of a fact both a source file and a
// test file carry, rather than dropping the fact with the test file's site: the
// framework keeps the first site by rendered order, and a test file sorts before the
// source file here.
func TestComputeKeepsTheSourceSiteOfAFactATestFileAlsoCarries(t *testing.T) {
	in := inputOf(t, "with-tests.txtar", Options{Production: true})

	found, err := Compute(in, goDetectors())
	if err != nil {
		t.Fatalf("Compute(with-tests.txtar, a production run) error: %v", err)
	}
	want := []string{"Sink.Write interface-satisfaction app.go:14:34 satisfies io.Writer"}
	if got := retainedClauses(in, found); !slices.Equal(got, want) {
		t.Errorf("Compute(with-tests.txtar, a production run) retained\n%v\nwant\n%v", got, want)
	}
}

// relationWords is the closed set of relations a clause may open with: every
// class states one clause of the form relation-then-thing, and the words are what
// a maintainer reads the nine classes by.
var relationWords = []string{
	"satisfies", "passed", "scanned", "converted", "decoded", "formatted",
	"implements", "declares", "declared", "named", "exported", "looked",
}

// fixtureArchives names every txtar archive of the package's testdata, which is
// every loaded configuration the package's own tests measure a class over.
func fixtureArchives(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("testdata", "*.txtar"))
	if err != nil {
		t.Fatalf("Setup: list testdata/*.txtar: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("Setup: testdata holds no txtar archive")
	}
	archives := make([]string, 0, len(found))
	for _, path := range found {
		archives = append(archives, filepath.Base(path))
	}
	return archives
}

// fixtureOptions configures the one class that scans files rather than type
// information, for the archives that carry a directory for it to scan.
func fixtureOptions(t *testing.T, archive string) Options {
	t.Helper()
	parsed, err := txtar.ParseFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatalf("Setup: parse testdata/%s: %v", archive, err)
	}
	for _, f := range parsed.Files {
		if strings.HasPrefix(f.Name, templateFixtureDir+"/") {
			return Options{TemplateDirs: []string{templateFixtureDir}}
		}
	}
	return Options{}
}

// templateFixtureDir is the directory an archive carries its templates in.
const templateFixtureDir = "templates"

func TestEveryClauseIsOneRelationThenTheThingItRelatesTo(t *testing.T) {
	seen := make(map[Class]bool, len(Classes()))
	for _, archive := range fixtureArchives(t) {
		t.Run(archive, func(t *testing.T) {
			in := inputOf(t, archive, fixtureOptions(t, archive))
			found, err := Compute(in, goDetectors())
			if err != nil {
				t.Fatalf("Compute(%s) error: %v", archive, err)
			}
			for _, e := range found {
				seen[Class(e.Class)] = true
				checkClause(t, archive, e)
			}
		})
	}

	// The grammar above is only worth pinning over a corpus that reaches every
	// class, so the fixtures are also what proves each of the nine states one.
	for _, class := range Classes() {
		if !seen[class] {
			t.Errorf("no fixture of testdata made %s state a clause, want each class covered", class)
		}
	}
}

// checkClause reports every way one exemption's clause departs from the grammar:
// one non-empty line opening with a lower-case relation from the closed set, and
// no closing full stop.
func checkClause(t *testing.T, archive string, e graph.Exemption) {
	t.Helper()

	stated := e.ID + " " + graph.SymbolID(e.Class)
	if e.Detail == "" {
		t.Errorf("Compute(%s) recorded %s with no clause, want one relation and the thing it relates to", archive, stated)
		return
	}
	if strings.ContainsAny(e.Detail, "\n\t") {
		t.Errorf("Compute(%s) recorded %s as %q, want one line with no tab", archive, stated, e.Detail)
	}
	if strings.HasSuffix(e.Detail, ".") {
		t.Errorf("Compute(%s) recorded %s as %q, want a clause with no closing full stop", archive, stated, e.Detail)
	}
	relation, _, _ := strings.Cut(e.Detail, " ")
	if !slices.Contains(relationWords, relation) {
		t.Errorf("Compute(%s) recorded %s as %q, whose relation %q is outside %v",
			archive, stated, e.Detail, relation, relationWords)
	}
}

// errEvidence is what a class returns when the evidence it needs is unreadable.
var errEvidence = errors.New("the template directory cannot be read")

func TestComputeNamesTheClassThatFailed(t *testing.T) {
	detectors := map[Class]Detector{
		GeneratedFile: detector(graph.Exemption{ID: "a.go:1:1", Class: string(GeneratedFile)}),
		TemplateField: func(*Input) ([]graph.Exemption, error) { return nil, errEvidence },
	}

	found, err := Compute(&Input{}, detectors)
	if !errors.Is(err, errEvidence) {
		t.Errorf("Compute over a class that failed error = %v, want one wrapping %v", err, errEvidence)
	}
	// A run whose exemption set is incomplete would report a symbol something
	// retains, so the union is withheld rather than returned short.
	if found != nil {
		t.Errorf("Compute over a class that failed retained %v, want nothing", held(found))
	}
	if got := err.Error(); !slices.Contains([]string{
		"exempt: " + string(TemplateField) + ": " + errEvidence.Error(),
	}, got) {
		t.Errorf("Compute over a class that failed error = %q, want it to name %s", got, TemplateField)
	}
}

func TestComputeHandsEveryClassTheLoadedConfigurationItReads(t *testing.T) {
	const method = "(*Catalog).Resolve"

	opts := Options{TemplateDirs: []string{"views"}, IncludeGenerated: true}
	in := inputOf(t, "framework.txtar", opts)

	// The framework hands a class the loaded configuration, the inventory, the
	// resolver over it, the target root, the reader and the configured half, and
	// every class of this wave is written against that one value.
	var seen *Input
	detectors := map[Class]Detector{
		GeneratedFile: func(in *Input) ([]graph.Exemption, error) {
			seen = in
			return nil, nil
		},
	}
	if _, err := Compute(in, detectors); err != nil {
		t.Fatalf("Compute over the loaded configuration error: %v", err)
	}
	if seen != in {
		t.Fatalf("Compute handed the class %p, want the input it was given, %p", seen, in)
	}

	var resolved []string
	for _, p := range seen.Result.Packages {
		for _, obj := range p.TypesInfo.Defs {
			if obj == nil {
				continue
			}
			if id, ok := seen.Resolve.Object(obj); ok {
				resolved = append(resolved, string(id))
			}
		}
	}
	if len(resolved) == 0 {
		t.Errorf("the input's resolver named no declaration of framework.txtar, want at least one")
	}

	names := make([]string, 0, len(seen.Symbols))
	for _, s := range seen.Symbols {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, method) {
		t.Errorf("the input's inventory holds %v, want it to hold %s", names, method)
	}
	if _, err := seen.Read(seen.Root + "/catalog.go"); err != nil {
		t.Errorf("the input's reader could not read the fixture's own source: %v", err)
	}
	if !slices.Equal(seen.Options.TemplateDirs, opts.TemplateDirs) || !seen.Options.IncludeGenerated {
		t.Errorf("the input's options are %+v, want %+v", seen.Options, opts)
	}
}

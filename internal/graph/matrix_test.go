package graph

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/load"
)

// configured returns what the three passes would have returned for each of
// configs configurations of the hand-built graph, given which configurations hold
// each declaration.
//
// A reference belongs to every configuration holding both the declaration that
// makes it and the declaration it names, and a root to every configuration
// holding the symbol it names, which is what a load produces: a file another
// platform does not compile makes no reference and declares no root there.
func (b *graphBuilder) configured(configs int, in func(config int, name string) bool) []Configured {
	per := make([]Configured, configs)
	for config := range configs {
		holds := func(id SymbolID) bool { return in(config, b.named[id]) }
		for _, s := range b.sorted() {
			if holds(s.ID) {
				per[config].Symbols = append(per[config].Symbols, s)
			}
		}
		for _, r := range b.refs {
			if holds(r.From) && holds(r.To) {
				per[config].References = append(per[config].References, r)
			}
		}
		for _, r := range b.roots {
			if holds(r.ID) {
				per[config].Roots = append(per[config].Roots, r)
			}
		}
	}
	return per
}

// The prefix every reference of the matrix fixture's one package carries.
const matrixPackage = "go://example.com/matrix#"

// linuxAmd64 and windowsAmd64 are the two configurations the matrix fixtures are
// loaded under: two platforms whose constrained files are both in the tree, which
// is what makes the two loads different sets of source.
func linuxAmd64() load.Configuration {
	return load.Configuration{ID: "linux-amd64", OS: "linux", Arch: "amd64"}
}

func windowsAmd64() load.Configuration {
	return load.Configuration{ID: "windows-amd64", OS: "windows", Arch: "amd64"}
}

// configuredOf runs over one configuration of one directory the three passes the
// sweep reads, so a matrix is assembled from the production pipeline rather than
// from an inventory written by hand.
func configuredOf(t *testing.T, dir string, c load.Configuration, opts RootOptions) Configured {
	t.Helper()

	result, target := loadDirUnder(t.Context(), t, dir, c)
	symbols, err := Symbols(result, target, os.ReadFile)
	if err != nil {
		t.Fatalf("Setup: Symbols(%s): %v", c.ID, err)
	}
	refs, _, err := References(result, target, os.ReadFile, symbols)
	if err != nil {
		t.Fatalf("Setup: References(%s): %v", c.ID, err)
	}
	roots, _, err := Roots(result, target, os.ReadFile, symbols, opts)
	if err != nil {
		t.Fatalf("Setup: Roots(%s): %v", c.ID, err)
	}
	return Configured{Symbols: symbols, References: refs, Roots: roots}
}

// sweptMatrix is one archive loaded under several configurations and merged, with
// the names a failure message reads by.
type sweptMatrix struct {
	matrix *Matrix
	byID   map[SymbolID]Symbol
	byRef  map[string]SymbolID
	merged Merged
	ids    []string // per configuration, its identifier
}

// matrixOf extracts one archive, runs the three passes over every configuration
// named and merges them in that order.
func matrixOf(t *testing.T, archive string, opts RootOptions, configs ...load.Configuration) *sweptMatrix {
	t.Helper()

	dir := extract(t, archive)
	per := make([]Configured, 0, len(configs))
	x := &sweptMatrix{
		byID:  make(map[SymbolID]Symbol),
		byRef: make(map[string]SymbolID),
		ids:   make([]string, 0, len(configs)),
	}
	for _, c := range configs {
		per = append(per, configuredOf(t, dir, c, opts))
		x.ids = append(x.ids, c.ID)
	}

	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge(%s) = _, %v, want no error", archive, err)
	}
	x.merged = merged
	x.matrix = NewMatrix(&merged)
	for _, s := range merged.Symbols {
		x.byID[s.ID] = s
		// A blank declaration carries its container's reference rather than one
		// of its own, so no reference names it.
		if !s.Blank {
			x.byRef[s.Ref] = s.ID
		}
	}
	return x
}

// name is how a failure message and a table name one symbol: its reference
// without the part every symbol of the package shares.
func (x *sweptMatrix) name(id SymbolID, prefix string) string {
	return strings.TrimPrefix(x.byID[id].Ref, prefix)
}

// names names a set of symbols in the order given.
func (x *sweptMatrix) names(ids []SymbolID, prefix string) []string {
	found := make([]string, 0, len(ids))
	for _, id := range ids {
		found = append(found, x.name(id, prefix))
	}
	return found
}

// configurations names the configurations one set holds, in matrix order.
func (x *sweptMatrix) configurations(set ConfigSet) string {
	found := make([]string, 0, len(x.ids))
	for _, at := range set.Indexes() {
		found = append(found, x.ids[at])
	}
	return strings.Join(found, " ")
}

// candidatesUnder names every candidate whose reference carries prefix, each with
// the relation that found it, the configurations it is dead in and the references
// the matrix counted to it.
func (x *sweptMatrix) candidatesUnder(prefix string, r Result) []string {
	var found []string
	for _, c := range r.Candidates {
		if !strings.HasPrefix(x.byID[c.ID].Ref, prefix) {
			continue
		}
		found = append(found, fmt.Sprintf("%s %s [%s] refs=%d/%d",
			x.name(c.ID, prefix), c.Relation, x.configurations(c.Configs), c.ProductionRefs, c.TestRefs))
	}
	return found
}

// groupsUnder names every component holding a symbol whose reference carries
// prefix.
func (x *sweptMatrix) groupsUnder(prefix string, r Result) []grouped {
	var found []grouped
	for _, c := range r.Components {
		if !strings.HasPrefix(x.byID[c.Members[0]].Ref, prefix) {
			continue
		}
		found = append(found, grouped{
			members: strings.Join(x.names(c.Members, prefix), " "),
			roots:   strings.Join(x.names(c.Roots, prefix), " "),
			falls:   strings.Join(x.names(c.Falls, prefix), " "),
			lines:   c.DeletableLines,
		})
	}
	return found
}

// existsIn names the configurations one declaration of the merged inventory
// exists in.
func (x *sweptMatrix) existsIn(t *testing.T, ref string) string {
	t.Helper()
	id, held := x.byRef[ref]
	if !held {
		t.Fatalf("the merged inventory holds no symbol whose reference is %s", ref)
	}
	return x.configurations(x.byID[id].Configs)
}

func TestMergeNamesTheConfigurationsEachDeclarationExistsIn(t *testing.T) {
	x := matrixOf(t, "matrix.txtar", RootOptions{PublishedAPI: true}, linuxAmd64(), windowsAmd64())

	cases := map[string]string{
		"a declaration every configuration compiles":         "linux-amd64 windows-amd64",
		"a declaration the first configuration alone holds":  "linux-amd64",
		"a declaration the second configuration alone holds": "windows-amd64",
	}
	refs := map[string]string{
		"a declaration every configuration compiles":         matrixPackage + "usedOnWindowsOnly",
		"a declaration the first configuration alone holds":  matrixPackage + "render_linux.go:file",
		"a declaration the second configuration alone holds": matrixPackage + "windowsOnlyDeadCaller",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if got := x.existsIn(t, refs[name]); got != want {
				t.Errorf("Merge(matrix.txtar)[%s].Configs names %q, want %q", refs[name], got, want)
			}
		})
	}
}

func TestMatrixSweepReportsOnlyWhatIsDeadUnderEveryConfigurationItExistsIn(t *testing.T) {
	x := matrixOf(t, "matrix.txtar", RootOptions{PublishedAPI: true}, linuxAmd64(), windowsAmd64())
	r := x.matrix.Sweep(SweepInput{})

	// usedOnWindowsOnly is the case the matrix exists for: dead under linux,
	// live under windows, and so reported under neither. windowsOnlyDeadCaller
	// and shut exist under windows alone and are judged there alone.
	// referencedByDeadCodeOnWindows and box are dead under both configurations
	// and are reported under the weaker claim, because the stronger one needs
	// every configuration to agree and windows holds a reference to each.
	// countedOnce carries one reference, not the two the two configurations saw.
	want := []string{
		"windowsOnlyDeadCaller reference-counting [windows-amd64] refs=0/0",
		"box.shut reference-counting [windows-amd64] refs=0/0",
		"Resolve reference-counting [linux-amd64 windows-amd64] refs=0/0",
		"deadEverywhere reference-counting [linux-amd64 windows-amd64] refs=0/0",
		"referencedByDeadCodeOnWindows reachability [linux-amd64 windows-amd64] refs=1/0",
		"deadCallerEverywhere reference-counting [linux-amd64 windows-amd64] refs=0/0",
		"countedOnce reachability [linux-amd64 windows-amd64] refs=1/0",
		"box reachability [linux-amd64 windows-amd64] refs=1/0",
	}
	if got := x.candidatesUnder(matrixPackage, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(matrix.txtar) over two configurations reported\n%s\nwant\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMatrixSweepGroupsOneComponentOverEveryConfigurationsEdges(t *testing.T) {
	x := matrixOf(t, "matrix.txtar", RootOptions{PublishedAPI: true}, linuxAmd64(), windowsAmd64())
	r := x.matrix.Sweep(SweepInput{})

	// The type and the method are one component although only one configuration
	// declares the method, because a report lists a dead component once and an
	// edge that holds under one configuration is an edge. The pair the windows
	// configuration alone holds an edge between is one component reaching
	// another, for the same reason.
	want := []grouped{
		{members: "windowsOnlyDeadCaller", roots: "windowsOnlyDeadCaller", falls: "windowsOnlyDeadCaller referencedByDeadCodeOnWindows", lines: 2},
		{members: "box.shut box", roots: "box", falls: "box.shut box", lines: 2},
		{members: "Resolve", roots: "Resolve", falls: "Resolve", lines: 1},
		{members: "deadEverywhere", roots: "deadEverywhere", falls: "deadEverywhere", lines: 1},
		{members: "deadCallerEverywhere", roots: "deadCallerEverywhere", falls: "deadCallerEverywhere countedOnce", lines: 2},
		{members: "referencedByDeadCodeOnWindows", falls: "referencedByDeadCodeOnWindows", lines: 1},
		{members: "countedOnce", falls: "countedOnce", lines: 1},
	}
	if got := x.groupsUnder(matrixPackage, r); !slices.Equal(got, want) {
		t.Errorf("Sweep(matrix.txtar) over two configurations returned components\n%+v\nwant\n%+v", got, want)
	}
}

func TestMatrixSweepHoldsASymbolLiveUnderTheRelationsOfEveryConfiguration(t *testing.T) {
	x := matrixOf(t, "matrix.txtar", RootOptions{PublishedAPI: true}, linuxAmd64(), windowsAmd64())
	r := x.matrix.Sweep(SweepInput{})

	// One configuration holds the declaration live under both relations and the
	// other holds it live under neither, so the record of why it is live is the
	// union of the two answers.
	id := x.byRef[matrixPackage+"usedOnWindowsOnly"]
	set := r.LiveUnder[id]
	if !set.Has(ReferenceCounting) || !set.Has(Reachability) {
		t.Errorf("Sweep(matrix.txtar).LiveUnder[usedOnWindowsOnly] holds reference-counting=%t reachability=%t, want both",
			set.Has(ReferenceCounting), set.Has(Reachability))
	}
}

func TestMatrixOverOneConfigurationAnswersWhatTheGraphOfThatConfigurationAnswers(t *testing.T) {
	dir := extract(t, "sweep.txtar")
	one := configuredOf(t, dir, linuxAmd64(), RootOptions{PublishedAPI: true})
	direct := New(one.Symbols, one.References, one.Roots).Sweep(SweepInput{})

	merged, err := Merge([]Configured{one})
	if err != nil {
		t.Fatalf("Merge(sweep.txtar) = _, %v, want no error", err)
	}
	got := NewMatrix(&merged).Sweep(SweepInput{})

	// A matrix of one configuration is the graph of that configuration, so every
	// answer agrees with the one a single load produces; the candidate's set of
	// configurations is the one thing a single graph is not keyed to answer.
	if !slices.EqualFunc(got.Candidates, direct.Candidates, sameApartFromConfigs) {
		t.Errorf("Sweep(sweep.txtar) over a matrix of one configuration reported %+v, want %+v",
			got.Candidates, direct.Candidates)
	}
	if !reflect.DeepEqual(got.Components, direct.Components) {
		t.Errorf("Sweep(sweep.txtar) over a matrix of one configuration returned components %+v, want %+v",
			got.Components, direct.Components)
	}
	if !maps.Equal(got.LiveUnder, direct.LiveUnder) {
		t.Errorf("Sweep(sweep.txtar) over a matrix of one configuration held %d symbols live, want %d",
			len(got.LiveUnder), len(direct.LiveUnder))
	}
	only := ConfigSet(0).with(0)
	for _, c := range got.Candidates {
		if c.Configs != only {
			t.Errorf("Sweep(sweep.txtar) over a matrix of one configuration reported %s under configurations %v, want %v",
				c.ID, c.Configs.Indexes(), only.Indexes())
		}
	}
}

// sameApartFromConfigs reports whether two candidates agree on everything but the
// configurations they are dead in.
func sameApartFromConfigs(a, b Candidate) bool {
	a.Configs, b.Configs = 0, 0
	return a == b
}

func TestMatrixSweepUnionsTheExemptionsEveryConfigurationRetained(t *testing.T) {
	// Two configurations, each holding one declaration of its own and one they
	// share, with an exemption on each of the three and a duplicate of the shared
	// one, which is what the union of two configurations' classes produces.
	b := newGraphBuilder(t).add("shared", "onlyFirst", "onlySecond")
	per := b.configured(2, func(config int, name string) bool {
		switch name {
		case "onlyFirst":
			return config == 0
		case "onlySecond":
			return config == 1
		default:
			return true
		}
	})
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}
	exempt := []Exemption{
		b.exemption("shared", "alpha-class"),
		b.exemption("shared", "alpha-class"),
		b.exemption("onlyFirst", "beta-class"),
		b.exemption("onlySecond", "beta-class"),
	}
	r := NewMatrix(&merged).Sweep(SweepInput{Exempt: exempt})

	// Every exemption held back a declaration under the configuration that holds
	// it, so every one is retained; the duplicate is one record, and a
	// declaration one configuration alone holds is retained under that one.
	want := []string{"shared alpha-class", "onlyFirst beta-class", "onlySecond beta-class"}
	if got := b.retainedBy(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over a matrix of two configurations retained %v, want %v", got, want)
	}
	if len(r.Candidates) != 0 {
		t.Errorf("Sweep over a matrix whose every declaration is exempt reported %d candidates, want 0", len(r.Candidates))
	}
}

func TestMatrixSweepUnionsTheMarksEveryConfigurationSuppressed(t *testing.T) {
	// Two configurations, each holding a caller the other does not, so each of
	// the two declarations those callers reference is dead under one
	// configuration and live under the other. A third declaration an entry point
	// reaches under both is live everywhere.
	b := newGraphBuilder(t).add("entry", "liveEverywhere",
		"heldBackOnTheFirst", "callerOnTheSecond", "heldBackOnTheSecond", "callerOnTheFirst")
	b.root("entry", RootMain)
	b.root("callerOnTheFirst", RootMain)
	b.root("callerOnTheSecond", RootMain)
	b.ref("entry", "liveEverywhere")
	b.ref("callerOnTheSecond", "heldBackOnTheFirst")
	b.ref("callerOnTheFirst", "heldBackOnTheSecond")
	per := b.configured(2, func(config int, name string) bool {
		switch name {
		case "callerOnTheFirst":
			return config == 0
		case "callerOnTheSecond":
			return config == 1
		default:
			return true
		}
	})
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}
	r := NewMatrix(&merged).Sweep(SweepInput{Marked: []SymbolID{
		b.id("heldBackOnTheSecond"),
		b.id("liveEverywhere"),
		b.id("heldBackOnTheFirst"),
	}})

	// A mark that held a declaration back under one configuration is in effect,
	// because withdrawing it would report the declaration there, so the record is
	// the union and not the intersection; the mark on the declaration every
	// configuration holds live is in effect for nothing. The record reads in the
	// order the merged inventory holds the symbols rather than the order the
	// marks arrived in.
	want := []string{"heldBackOnTheFirst", "heldBackOnTheSecond"}
	if got := b.suppressedBy(r); !slices.Equal(got, want) {
		t.Errorf("Sweep over a matrix of two configurations suppressed %v, want %v", got, want)
	}
	if len(r.Candidates) != 0 {
		t.Errorf("Sweep over a matrix whose every declaration is live somewhere reported %d candidates, want 0",
			len(r.Candidates))
	}
}

func TestMergeRefusesAMatrixItCannotKeyAConfigurationOf(t *testing.T) {
	cases := map[string]int{
		"no configuration":                 0,
		"one configuration past the bound": maxConfigurations + 1,
	}
	for name, count := range cases {
		t.Run(name, func(t *testing.T) {
			per := make([]Configured, count)
			if _, err := Merge(per); !errors.Is(err, ErrMatrix) {
				t.Errorf("Merge over %d configurations = _, %v, want %v", count, err, ErrMatrix)
			}
		})
	}
}

func TestMergeKeepsOneSymbolPerPositionWithTheFirstConfigurationsFields(t *testing.T) {
	b := newGraphBuilder(t).add("first", "second")
	per := b.configured(2, func(int, string) bool { return true })

	// The second configuration's copy of one declaration carries fields of its
	// own, which one source position cannot produce and the merge therefore
	// decides by the lowest configuration index.
	per[1].Symbols[0].EndLine = 99
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}

	if len(merged.Symbols) != len(per[0].Symbols) {
		t.Fatalf("Merge over two configurations of one inventory returned %d symbols, want %d",
			len(merged.Symbols), len(per[0].Symbols))
	}
	if got := merged.Symbols[0].EndLine; got != per[0].Symbols[0].EndLine {
		t.Errorf("Merge kept EndLine %d for %s, want the first configuration's %d",
			got, merged.Symbols[0].ID, per[0].Symbols[0].EndLine)
	}
	if got := merged.Symbols[0].Configs.Indexes(); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("Merge named configurations %v for %s, want [0 1]", got, merged.Symbols[0].ID)
	}
}

func TestMergeCarriesEveryConfigurationsReferencesAndRoots(t *testing.T) {
	b := newGraphBuilder(t).add("caller", "target")
	b.ref("caller", "target")
	b.root("caller", RootMain)
	per := b.configured(2, func(int, string) bool { return true })

	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}

	// Each configuration's reference and root come through with the
	// configuration they were seen in, because a per-configuration sweep reads
	// its own set and the merge is what keys them.
	if got := configsOfRefs(merged.References); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("Merge over two configurations returned references of configurations %v, want [0 1]", got)
	}
	if got := configsOfRoots(merged.Roots); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("Merge over two configurations returned roots of configurations %v, want [0 1]", got)
	}
}

// configsOfRefs lists the configuration of every reference, in the order given.
func configsOfRefs(refs []Reference) []int {
	found := make([]int, 0, len(refs))
	for _, r := range refs {
		found = append(found, r.Config)
	}
	return found
}

// configsOfRoots lists the configuration of every root, in the order given.
func configsOfRoots(roots []Root) []int {
	found := make([]int, 0, len(roots))
	for _, r := range roots {
		found = append(found, r.Config)
	}
	return found
}

func TestMatrixUnmatchedEverywhereIntersectsWhatEachConfigurationMatchedNothingWith(t *testing.T) {
	cases := map[string]struct {
		per  [][]string
		want []string
	}{
		"one configuration answers its own set": {
			per:  [][]string{{"alone", "second"}},
			want: []string{"alone", "second"},
		},
		"a string every configuration matched nothing with": {
			per:  [][]string{{"nowhere"}, {"nowhere"}},
			want: []string{"nowhere"},
		},
		"a string one configuration matched": {
			per:  [][]string{{"here"}, {}},
			want: []string{},
		},
		"the order of the first configuration that reported each": {
			per:  [][]string{{"second", "first"}, {"first", "second"}},
			want: []string{"second", "first"},
		},
		"a string reported twice in one configuration": {
			per:  [][]string{{"twice", "twice"}, {"twice"}},
			want: []string{},
		},
		"nothing unmatched at all": {
			per:  [][]string{{}, {}},
			want: []string{},
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			b := newGraphBuilder(t).add("only")
			per := b.configured(len(test.per), func(int, string) bool { return true })
			for config, sources := range test.per {
				for _, source := range sources {
					per[config].Unmatched = append(per[config].Unmatched, Unmatched{Source: source})
				}
			}
			merged, err := Merge(per)
			if err != nil {
				t.Fatalf("Merge = _, %v, want no error", err)
			}

			got := make([]string, 0, len(test.want))
			for _, unmatched := range NewMatrix(&merged).UnmatchedEverywhere() {
				got = append(got, unmatched.Source)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("UnmatchedEverywhere over %v = %v, want %v", test.per, got, test.want)
			}
		})
	}
}

func TestMergeCarriesEveryConfigurationsUnmatchedStrings(t *testing.T) {
	b := newGraphBuilder(t).add("only")
	per := b.configured(2, func(int, string) bool { return true })
	per[0].Unmatched = []Unmatched{{Source: "first"}}
	per[1].Unmatched = []Unmatched{{Source: "second"}}

	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}

	want := []Unmatched{{Source: "first", Config: 0}, {Source: "second", Config: 1}}
	if !reflect.DeepEqual(merged.Unmatched, want) {
		t.Errorf("Merge over two configurations returned unmatched %+v, want %+v", merged.Unmatched, want)
	}
}

func TestDistinctCountsOneReferenceOncePerMatrixAndTwiceInOneConfiguration(t *testing.T) {
	b := newGraphBuilder(t).add("caller", "target")
	b.ref("caller", "target")
	b.ref("caller", "target")
	per := b.configured(2, func(int, string) bool { return true })
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}

	// Four references arrive, being two in each of two configurations at one
	// position. The two the second configuration saw are the two the first saw,
	// so the matrix counts two.
	if got := len(distinct(merged.References)); got != 2 {
		t.Errorf("distinct over two configurations holding two references at one position returned %d, want 2", got)
	}
}

func TestDistinctTellsTwoConsumersReferencesAtOnePositionApart(t *testing.T) {
	b := newGraphBuilder(t).add("called")
	b.refFromConsumer(handConsumer, "called", false)
	b.refFromConsumer(handOtherConsumer, "called", false)
	// One position per consumer, which is what two modules holding a file of one
	// name produce: the module is part of what makes a reference one reference.
	b.refs[1].Pos = b.refs[0].Pos
	per := b.configured(2, func(int, string) bool { return true })
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}

	if got := len(distinct(merged.References)); got != 2 {
		t.Errorf("distinct over two consumers referencing one symbol at one position returned %d, want 2", got)
	}
}

func TestNewMatrixAnalyzesNoDeclarationOfNoConfigurationOfTheMatrix(t *testing.T) {
	b := newGraphBuilder(t).add("outside")
	per := b.configured(1, func(int, string) bool { return true })
	merged, err := Merge(per)
	if err != nil {
		t.Fatalf("Merge = _, %v, want no error", err)
	}
	merged.Symbols[0].Configs = ConfigSet(0).with(1)

	// A declaration that exists in no configuration of the matrix is not
	// analyzed. Reporting it would be the vacuous truth that it is dead in every
	// configuration it exists in, over no configuration at all.
	if r := NewMatrix(&merged).Sweep(SweepInput{}); len(r.Candidates) != 0 {
		t.Errorf("Sweep over a matrix whose one declaration names no configuration of it reported %d candidates, want 0",
			len(r.Candidates))
	}
}

func TestConfigSetHoldsEveryConfigurationInTheMatrixAndNoneOutsideIt(t *testing.T) {
	cases := map[string]struct {
		set  ConfigSet
		want []int
	}{
		"no configuration":               {set: 0, want: []int{}},
		"the first":                      {set: ConfigSet(0).with(0), want: []int{0}},
		"the first and the last":         {set: ConfigSet(0).with(0).with(maxConfigurations - 1), want: []int{0, maxConfigurations - 1}},
		"one configuration past the set": {set: ConfigSet(0).with(maxConfigurations), want: []int{}},
		"a negative configuration":       {set: ConfigSet(0).with(-1), want: []int{}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := test.set.Indexes(); !slices.Equal(got, test.want) {
				t.Errorf("ConfigSet(%d).Indexes() = %v, want %v", test.set, got, test.want)
			}
			for _, config := range []int{-1, maxConfigurations, maxConfigurations + 1} {
				if test.set.Has(config) {
					t.Errorf("ConfigSet(%d).Has(%d) = true, want false", test.set, config)
				}
			}
		})
	}
}

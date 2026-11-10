package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
	"pgregory.net/rapid"
)

// The kinds of expression a drawn constant specification states. The last two
// can fail to compile after a deletion lowers iota, so only a block's last
// specification states them.
const (
	exIota = iota
	exIotaPlus
	exIotaMinus
	exShift
	exTimesTwo
	exLiteral
	exDivision
	exShiftBy
)

// egExpr is one expression of a drawn constant specification.
type egExpr struct {
	kind int
	k    int
}

// source is the expression as the program writes it.
func (e egExpr) source() string {
	switch e.kind {
	case exIota:
		return "iota"
	case exIotaPlus:
		return "iota + " + strconv.Itoa(e.k)
	case exIotaMinus:
		return "iota - " + strconv.Itoa(e.k)
	case exShift:
		return "1 << iota"
	case exTimesTwo:
		return "iota * 2"
	case exDivision:
		return "1 / (iota - " + strconv.Itoa(e.k) + ")"
	case exShiftBy:
		return "1 << (iota - " + strconv.Itoa(e.k) + ")"
	default:
		return strconv.Itoa(e.k)
	}
}

// value is the expression's value at one iota, and false where the compiler
// rejects it: a division by zero or a negative shift count.
func (e egExpr) value(iota int) (int, bool) {
	switch e.kind {
	case exIota:
		return iota, true
	case exIotaPlus:
		return iota + e.k, true
	case exIotaMinus:
		return iota - e.k, true
	case exShift:
		return 1 << iota, true
	case exTimesTwo:
		return iota * 2, true
	case exDivision:
		if iota == e.k {
			return 0, false
		}
		return 1 / (iota - e.k), true
	case exShiftBy:
		if iota < e.k {
			return 0, false
		}
		return 1 << (iota - e.k), true
	default:
		return e.k, true
	}
}

// egSpec is one specification of a drawn constant block: its names and the list
// it states, nil where it repeats the list before it. A blank specification is an
// untyped `_ = expr`, always the block's last.
type egSpec struct {
	names []string
	exprs []egExpr
	blank bool
}

// egType is one drawn enumerated type: its blocks of members, which members the
// program names, how values of it arise and how the program uses them.
type egType struct {
	name   string
	base   string
	blocks [][]egSpec
	named  []string

	// The productions: each named member passed by name, through a struct field and
	// through a ranged slice, the zero value, constant conversions, constants a
	// function body declares, a named member of another type converted to this one
	// in a constant expression, a constant whose value depends on the target
	// platform, a constant of a build-constrained file (with the legacy line form or
	// the current one), a conversion of the input and an increment.
	byName, byField, byRange []string
	zero                     bool
	literals, locals         []int
	crossFrom                *egType
	crossMember              string
	wide, pinned, legacy     bool
	converted, incremented   bool

	// convertedOut is set where a named member of the type is converted to another
	// type, a use the rule does not list.
	convertedOut bool

	// The uses inside the observing function: comparison against every named
	// member is always written; a switch, an order comparison, a map lookup, and
	// two forms that print the number.
	switched, ordered, looked bool
	printsInt, printsValue    bool
}

// unlisted reports whether a value of the type arises or is used in a form the
// enum-group rule does not list, or arises with a value the program does not fix.
func (e *egType) unlisted() bool {
	return e.wide || e.pinned || e.converted || e.incremented || e.printsInt || e.printsValue || e.convertedOut
}

// members is every non-blank member name of the type, in declaration order.
func (e *egType) members() []string {
	var all []string
	for _, block := range e.blocks {
		for _, spec := range block {
			if !spec.blank {
				all = append(all, spec.names...)
			}
		}
	}
	return all
}

// values evaluates every member of the type with the members in deleted removed,
// the way the compiler does: a specification keeps its place while one name stays,
// a specification that repeats takes the list last stated before it, and the
// counts must match. It reports false where the program would not compile.
func (e *egType) values(deleted map[string]bool) (map[string]int, bool) {
	low, high := -1<<63, 1<<63-1
	if e.base == "int8" {
		low, high = -128, 127
	}
	found := make(map[string]int)
	for _, block := range e.blocks {
		var last []egExpr
		iota := 0
		for _, spec := range block {
			names, exprs := spec.names, spec.exprs
			kept, keptExprs := []string{}, []egExpr{}
			for i, name := range names {
				if deleted[name] {
					continue
				}
				kept = append(kept, name)
				if exprs != nil {
					keptExprs = append(keptExprs, exprs[i])
				}
			}
			if len(kept) == 0 {
				// The whole specification is gone, and with it its place.
				continue
			}
			if exprs != nil {
				last = keptExprs
			}
			if len(last) != len(kept) {
				return nil, false
			}
			for i, name := range kept {
				v, ok := last[i].value(iota)
				if !ok || !spec.blank && (v < low || v > high) {
					return nil, false
				}
				if !spec.blank {
					found[name] = v
				}
			}
			iota++
		}
	}
	return found, true
}

// tails is every unnamed member whose removal moves nothing whatever else is
// deleted: one of the last specification of its block, when that specification
// states its own list or has a single name (rule D retains a name of a
// specification of several that repeats a list).
func (e *egType) tails() []string {
	var found []string
	for _, block := range e.blocks {
		last := block[len(block)-1]
		if last.blank || last.exprs == nil && len(last.names) > 1 {
			continue
		}
		for _, name := range last.names {
			if !slices.Contains(e.named, name) {
				found = append(found, name)
			}
		}
	}
	return found
}

// moves reports whether deleting the members in deleted changes the value of a
// named member or leaves a program that does not compile.
func (e *egType) moves(deleted map[string]bool) bool {
	before, _ := e.values(nil)
	after, ok := e.values(deleted)
	if !ok {
		return true
	}
	return slices.ContainsFunc(e.named, func(n string) bool { return before[n] != after[n] })
}

// observedAlike reports whether every value arising in the program relates to every
// named member as it did before the members in deleted were removed: the zero
// value, each constant conversion, each local constant and the member of another
// type converted to this one by its number, and each member the program passes by
// its own name.
func (e *egType) observedAlike(deleted map[string]bool) bool {
	before, _ := e.values(nil)
	after, ok := e.values(deleted)
	if !ok {
		return false
	}
	type arising struct {
		number int
		member string
	}
	var values []arising
	if e.zero {
		values = append(values, arising{})
	}
	for _, k := range slices.Concat(e.literals, e.locals) {
		values = append(values, arising{number: k})
	}
	if e.crossFrom != nil {
		values = append(values, arising{number: e.crossValue()})
	}
	for _, name := range slices.Concat(e.byName, e.byField, e.byRange) {
		values = append(values, arising{member: name})
	}
	at := func(a arising, of map[string]int) int {
		if a.member != "" {
			return of[a.member]
		}
		return a.number
	}
	for _, a := range values {
		for _, n := range e.named {
			if (at(a, before) == before[n]) != (at(a, after) == after[n]) {
				return false
			}
			if e.ordered && n == e.named[0] && (at(a, before) < before[n]) != (at(a, after) < after[n]) {
				return false
			}
		}
	}
	return true
}

// crossValue is the value of the member of another type converted to this one.
func (e *egType) crossValue() int {
	values, _ := e.crossFrom.values(nil)
	return values[e.crossMember]
}

// enumProgram is one drawn program: its types and whether its package holds a
// source in another language than Go.
type enumProgram struct {
	types []*egType
	nonGo bool
}

// drawEnumProgram draws one program from the enum-group case grammar of the
// design's section 7.2.
func drawEnumProgram(t *rapid.T) *enumProgram {
	p := &enumProgram{nonGo: rapid.IntRange(0, 9).Draw(t, "a non-Go source beside the package") == 0}
	for ti := range rapid.IntRange(1, 3).Draw(t, "types") {
		p.types = append(p.types, drawEnumType(t, ti))
	}
	for ti := 1; ti < len(p.types); ti++ {
		from, e := p.types[ti-1], p.types[ti]
		if len(from.named) == 0 || rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("type %d: a member of type %d converted to it", ti, ti-1)) != 0 {
			continue
		}
		e.crossFrom, e.crossMember = from, rapid.SampledFrom(from.named).Draw(t, fmt.Sprintf("type %d: the converted member", ti))
		from.convertedOut = true
	}
	return p
}

// drawEnumType draws one enumerated type, its blocks and its uses.
func drawEnumType(t *rapid.T, ti int) *egType {
	label := func(s string) string { return fmt.Sprintf("type %d: %s", ti, s) }
	e := &egType{name: "T" + strconv.Itoa(ti), base: rapid.SampledFrom([]string{"int", "int8"}).Draw(t, label("base"))}
	width := 1
	if rapid.IntRange(0, 3).Draw(t, label("two names per specification")) == 0 {
		width = 2
	}
	drawn := func(s string, last bool) egExpr {
		top := exLiteral
		if last {
			top = exShiftBy
		}
		kind := rapid.IntRange(exIota, top).Draw(t, s+": kind")
		return egExpr{kind: kind, k: rapid.IntRange(0, 3).Draw(t, s+": constant")}
	}
	count := 0
	for bi := range rapid.IntRange(1, 2).Draw(t, label("blocks")) {
		var block []egSpec
		specs := rapid.IntRange(1, 4).Draw(t, label(fmt.Sprintf("block %d specifications", bi)))
		for si := range specs {
			s := label(fmt.Sprintf("block %d spec %d", bi, si))
			spec := egSpec{}
			for range width {
				spec.names = append(spec.names, fmt.Sprintf("t%dm%d", ti, count))
				count++
			}
			if si == 0 || rapid.Bool().Draw(t, s+": restates") {
				for wi := range width {
					spec.exprs = append(spec.exprs, drawn(fmt.Sprintf("%s expr %d", s, wi), si == specs-1))
				}
			}
			block = append(block, spec)
		}
		if rapid.IntRange(0, 3).Draw(t, label(fmt.Sprintf("block %d blank last", bi))) == 0 {
			k := rapid.IntRange(0, len(block)).Draw(t, label("blank constant"))
			kind := rapid.SampledFrom([]int{exDivision, exShiftBy}).Draw(t, label("blank kind"))
			block = append(block, egSpec{names: []string{"_"}, exprs: []egExpr{{kind: kind, k: k}}, blank: true})
		}
		e.blocks = append(e.blocks, block)
	}
	for _, m := range e.members() {
		if rapid.Bool().Draw(t, label(m+" named")) {
			e.named = append(e.named, m)
		}
	}
	for _, m := range e.named {
		if rapid.Bool().Draw(t, label(m+" passed by name")) {
			e.byName = append(e.byName, m)
		}
		if rapid.IntRange(0, 4).Draw(t, label(m+" through a field")) == 0 {
			e.byField = append(e.byField, m)
		}
		if rapid.IntRange(0, 4).Draw(t, label(m+" through a range")) == 0 {
			e.byRange = append(e.byRange, m)
		}
	}
	rare := func(s string) bool { return rapid.IntRange(0, 5).Draw(t, label(s)) == 0 }
	e.zero = rapid.Bool().Draw(t, label("zero value"))
	for range rapid.IntRange(0, 2).Draw(t, label("constant conversions")) {
		e.literals = append(e.literals, rapid.IntRange(0, 6).Draw(t, label("converted constant")))
	}
	for range rapid.IntRange(0, 1).Draw(t, label("local constants")) {
		e.locals = append(e.locals, rapid.IntRange(0, 6).Draw(t, label("local constant")))
	}
	e.wide, e.pinned = rare("platform constant"), rare("constrained constant")
	e.legacy = e.pinned && rapid.Bool().Draw(t, label("legacy constraint line"))
	e.converted, e.incremented = rare("input conversion"), rare("increment") && len(e.named) > 0
	e.switched, e.ordered, e.looked = rapid.Bool().Draw(t, label("switch")), rapid.Bool().Draw(t, label("order")), rapid.Bool().Draw(t, label("map"))
	e.printsInt, e.printsValue = rare("prints the number"), rare("prints the value")
	if len(e.byName)+len(e.byField)+len(e.byRange)+len(e.literals)+len(e.locals) == 0 && !e.wide && !e.pinned && !e.converted && !e.incremented {
		// An observer no production calls is dead code, and the members only it
		// names are then not named.
		e.zero = true
	}
	return e
}

// valid reports whether the drawn program compiles as drawn: every member's value
// fits, a member converted to another type fits it, and, where a switch or a map
// literal lists the named members, they differ.
func (p *enumProgram) valid() bool {
	for _, e := range p.types {
		values, ok := e.values(nil)
		if !ok {
			return false
		}
		if e.crossFrom != nil {
			if _, ok := e.crossFrom.values(nil); !ok {
				return false
			}
			if v := e.crossValue(); e.base == "int8" && (v < -128 || v > 127) {
				return false
			}
		}
		if e.switched || e.looked {
			seen := make(map[int]bool)
			for _, n := range e.named {
				if seen[values[n]] {
					return false
				}
				seen[values[n]] = true
			}
		}
	}
	return true
}

// source is the program with the members in deleted removed, as its files.
func (p *enumProgram) source(deleted map[string]bool) map[string]string {
	var b strings.Builder
	b.WriteString("// Command app is a generated enum-group program.\npackage main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"strconv\"\n")
	if slices.ContainsFunc(p.types, func(e *egType) bool { return e.wide }) {
		b.WriteString("\t\"math/bits\"\n")
	}
	b.WriteString(")\n")
	files := map[string]string{"go.mod": "module example.com/app\n\ngo 1.27.0\n"}
	for _, e := range p.types {
		writeEnumType(&b, e, deleted)
		if e.pinned {
			line := "//go:build !plan9"
			if e.legacy {
				line = "// +build !plan9"
			}
			files["pin_"+strings.ToLower(e.name)+".go"] = fmt.Sprintf("%s\n\npackage main\n\nconst pinned%s %s = 1\n", line, e.name, e.name)
		}
	}
	b.WriteString("\nfunc main() {\n\tn, _ := strconv.Atoi(os.Args[1])\n\tfmt.Println(\"input\", n)\n")
	for _, e := range p.types {
		writeProductions(&b, e)
	}
	b.WriteString("}\n")
	files["main.go"] = b.String()
	if p.nonGo {
		files["_note.s"] = "// An assembly file go build ignores for its leading underscore.\n"
	}
	return files
}

// writeEnumType writes one type, its blocks, its holder types and its observing
// function.
func writeEnumType(b *strings.Builder, e *egType, deleted map[string]bool) {
	fmt.Fprintf(b, "\ntype %s %s\n", e.name, e.base)
	for _, block := range e.blocks {
		var specs []string
		for _, spec := range block {
			var names, exprs []string
			for i, name := range spec.names {
				if deleted[name] {
					continue
				}
				names = append(names, name)
				if spec.exprs != nil {
					exprs = append(exprs, spec.exprs[i].source())
				}
			}
			switch {
			case len(names) == 0:
			case spec.blank:
				specs = append(specs, "\t_ = "+exprs[0])
			case spec.exprs == nil:
				specs = append(specs, "\t"+strings.Join(names, ", "))
			default:
				specs = append(specs, fmt.Sprintf("\t%s %s = %s", strings.Join(names, ", "), e.name, strings.Join(exprs, ", ")))
			}
		}
		if len(specs) > 0 {
			fmt.Fprintf(b, "\nconst (\n%s\n)\n", strings.Join(specs, "\n"))
		}
	}
	if e.wide {
		fmt.Fprintf(b, "\nconst wide%s = %s(bits.UintSize / 32)\n", e.name, e.name)
	}
	fmt.Fprintf(b, "\ntype box%s struct{ v %s }\n", e.name, e.name)
	if e.looked && len(e.named) > 0 {
		var entries []string
		for _, n := range e.named {
			entries = append(entries, fmt.Sprintf("%s: %q", n, n))
		}
		fmt.Fprintf(b, "\nvar names%s = map[%s]string{%s}\n", e.name, e.name, strings.Join(entries, ", "))
	}
	fmt.Fprintf(b, "\nfunc observe%s(tag string, v %s) {\n", e.name, e.name)
	compared := slices.Clone(e.named)
	if e.wide {
		compared = append(compared, "wide"+e.name)
	}
	if e.pinned {
		compared = append(compared, "pinned"+e.name)
	}
	for _, n := range compared {
		fmt.Fprintf(b, "\tfmt.Println(tag, %q, v == %s)\n", n, n)
	}
	if e.switched && len(e.named) > 0 {
		b.WriteString("\tswitch v {\n")
		for _, n := range e.named {
			fmt.Fprintf(b, "\tcase %s:\n\t\tfmt.Println(tag, \"case %s\")\n", n, n)
		}
		b.WriteString("\tdefault:\n\t\tfmt.Println(tag, \"default\")\n\t}\n")
	}
	if e.ordered && len(e.named) > 0 {
		fmt.Fprintf(b, "\tfmt.Println(tag, v < %s)\n", e.named[0])
	}
	if e.looked && len(e.named) > 0 {
		fmt.Fprintf(b, "\tfmt.Println(tag, names%s[v])\n", e.name)
	}
	if e.printsInt {
		b.WriteString("\tfmt.Println(tag, int(v))\n")
	}
	if e.printsValue {
		b.WriteString("\tfmt.Println(tag, v)\n")
	}
	b.WriteString("}\n")
}

// writeProductions writes the statements of main that make values of one type
// and hand them to its observing function.
func writeProductions(b *strings.Builder, e *egType) {
	call := func(tag, value string) { fmt.Fprintf(b, "\tobserve%s(%q, %s)\n", e.name, tag, value) }
	for _, n := range e.byName {
		call("name "+n, n)
	}
	for _, n := range e.byField {
		call("field "+n, fmt.Sprintf("box%s{v: %s}.v", e.name, n))
	}
	if len(e.byRange) > 0 {
		fmt.Fprintf(b, "\tfor _, x := range []%s{%s} {\n\t\tobserve%s(\"range\", x)\n\t}\n", e.name, strings.Join(e.byRange, ", "), e.name)
	}
	if e.zero {
		fmt.Fprintf(b, "\t{\n\t\tvar z %s\n\t\tobserve%s(\"zero\", z)\n\t}\n", e.name, e.name)
	}
	for i, k := range e.literals {
		call("literal "+strconv.Itoa(i), fmt.Sprintf("%s(%d)", e.name, k))
	}
	for i, k := range e.locals {
		fmt.Fprintf(b, "\t{\n\t\tconst c %s = %d\n\t\tobserve%s(%q, c)\n\t}\n", e.name, k, e.name, "local "+strconv.Itoa(i))
	}
	if e.crossFrom != nil {
		call("cross", fmt.Sprintf("%s(%s)", e.name, e.crossMember))
	}
	if e.wide {
		call("wide", "wide"+e.name)
	}
	if e.pinned {
		call("pinned", "pinned"+e.name)
	}
	if e.converted {
		call("input", e.name+"(n)")
	}
	if e.incremented {
		fmt.Fprintf(b, "\t{\n\t\tv := %s\n\t\tv++\n\t\tobserve%s(\"increment\", v)\n\t}\n", e.named[0], e.name)
	}
}

// enumInputs are the arguments every build of a drawn program runs on.
var enumInputs = []string{"0", "1", "2", "3"}

// TestEnumGroupReportsOnlyAMemberWhoseDeletionNoProgramObserves is enum-group's
// generated property over programs drawn from the case grammar (drawEnumProgram).
// Each draw is analysed once and held to the rule's oracle (checkRuleOracle),
// computed from the grammar alone, and to the differential oracle
// (checkDifferential): the program with the reported members deleted builds and
// prints the same on every input. Cost: one analysis and one go build of up to a
// dozen small main packages per draw, about 90 s for rapid's default 100 draws on
// four cores; DEADSET_ENUM_PROPERTY_ROUNDS repeats the run under fresh seeds.
func TestEnumGroupReportsOnlyAMemberWhoseDeletionNoProgramObserves(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the differential oracle builds each program with the go command, which is not on PATH")
	}
	rounds := 1
	if raised, err := strconv.Atoi(os.Getenv("DEADSET_ENUM_PROPERTY_ROUNDS")); err == nil && raised > 0 {
		rounds = raised
	}
	scratch := t.TempDir()
	ctx := t.Context()
	draw := 0
	for range rounds {
		// Each round is one rapid run under a seed of its own.
		rapid.Check(t, func(rt *rapid.T) {
			draw++
			p := drawEnumProgram(rt)
			if !p.valid() {
				rt.Skip("the drawn program does not compile as drawn")
			}
			checkEnumProgram(ctx, rt, p, filepath.Join(scratch, strconv.Itoa(draw)))
		})
		if t.Failed() {
			return
		}
	}
}

// checkEnumProgram analyses one drawn program and applies both oracles.
func checkEnumProgram(ctx context.Context, t *rapid.T, p *enumProgram, dir string) {
	files := p.source(nil)
	files[repositoryDocument] = `{"target": {"kind": "application"}}`
	set := cachedFindings(ctx, t, files)
	reported := make(map[string]bool)
	for _, found := range slices.Concat(set.Result.Findings, set.Result.Withheld) {
		if found.Code != vocab.UnusedEnumMember && found.Code != vocab.UnusedUnexported {
			continue
		}
		_, name, _ := strings.Cut(found.Symbol.Ref, "#")
		reported[name] = true
	}
	program := files["main.go"]

	variants := map[string]map[string]bool{}
	for _, e := range p.types {
		var held []string
		for _, m := range e.members() {
			if reported[m] {
				held = append(held, m)
			}
		}
		checkRuleOracle(t, p, e, held, program)
		if len(held) == 0 {
			continue
		}
		variants[e.name+"all"] = setOf(held...)
		if len(held) > 1 {
			for _, m := range held {
				variants[e.name+m] = setOf(m)
			}
		}
	}
	if len(variants) > 0 {
		checkDifferential(ctx, t, p, variants, dir, program)
	}
}

// checkRuleOracle applies the rule's oracle to the members of one type the run
// reported. A report claims that the reported members can be deleted together, and
// any subset of them, so every subset is checked.
func checkRuleOracle(t *rapid.T, p *enumProgram, e *egType, held []string, program string) {
	if p.nonGo && len(held) > 0 {
		t.Fatalf("a package holding a non-Go source reported %v of %s:\n%s", held, e.name, program)
	}
	for _, m := range held {
		if slices.Contains(e.named, m) {
			t.Fatalf("the named member %s was reported:\n%s", m, program)
		}
	}
	for subset := 1; subset < 1<<len(held); subset++ {
		deleted := map[string]bool{}
		for i, m := range held {
			if subset&(1<<i) != 0 {
				deleted[m] = true
			}
		}
		listed := slices.Sorted(maps.Keys(deleted))
		if _, compiles := e.values(deleted); !compiles {
			t.Fatalf("deleting %v of the reported %v of %s leaves a block that does not compile:\n%s", listed, held, e.name, program)
		}
		if !e.moves(deleted) {
			continue
		}
		if e.unlisted() {
			t.Fatalf("deleting %v of the reported %v moves a named member of %s, whose values arise or are used in an unlisted form or have no fixed value:\n%s",
				listed, held, e.name, program)
		}
		if !e.observedAlike(deleted) {
			t.Fatalf("deleting %v of the reported %v changes how a value of %s compares with a named member:\n%s", listed, held, e.name, program)
		}
	}
	if p.nonGo {
		return
	}
	for _, m := range e.tails() {
		if !slices.Contains(held, m) {
			t.Fatalf("%s of %s is named nowhere and its removal moves nothing, yet it was not reported:\n%s", m, e.name, program)
		}
	}
}

// checkDifferential builds the original program and every variant in one module,
// runs each on every input and compares what it prints with the original.
func checkDifferential(ctx context.Context, t *rapid.T, p *enumProgram, variants map[string]map[string]bool, dir, program string) {
	write := func(sub string, files map[string]string) {
		for name, src := range files {
			if name == "go.mod" {
				continue
			}
			path := filepath.Join(dir, sub, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("Setup: %v", err)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/variants\n\ngo 1.27.0\n"), 0o644); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	write("original", p.source(nil))
	names := slices.Sorted(maps.Keys(variants))
	for _, name := range names {
		write(name, p.source(variants[name]))
	}
	bin := filepath.Join(dir, "bin")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin+string(filepath.Separator), "./...")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("deleting the reported members leaves a program that does not build: %v\n%s\noriginal:\n%s", err, out, program)
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	runOne := func(name, input string) string {
		cmd := exec.CommandContext(ctx, filepath.Join(bin, name+exe), input)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return fmt.Sprintf("%s\nexit: %v", out.String(), err)
	}
	for _, input := range enumInputs {
		want := runOne("original", input)
		for _, name := range names {
			if got := runOne(name, input); got != want {
				t.Fatalf("deleting %v changes the output on input %s:\n--- original\n%s\n--- deleted\n%s\nprogram:\n%s",
					slices.Sorted(maps.Keys(variants[name])), input, want, got, program)
			}
		}
	}
}

// setOf is the set of the names given.
func setOf(names ...string) map[string]bool {
	held := make(map[string]bool, len(names))
	for _, n := range names {
		held[n] = true
	}
	return held
}

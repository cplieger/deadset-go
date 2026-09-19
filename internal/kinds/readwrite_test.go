package kinds

import (
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/deadset-go/internal/exempt"
	"github.com/cplieger/deadset-go/internal/graph"
)

func TestWriteOnlySymbolNamesEveryWritePositionOfAVariableNothingReads(t *testing.T) {
	in := inputOf(t, "readwrite-writeonly.txtar", applicationConfig(), Consumers{})
	in.Mode.Production = false

	found := computed(t, in, map[string]Emitter{writeOnlyCode: WriteOnlySymbol}).Findings
	if names := namesUnder(found, writeOnlyCode); !slices.Equal(names, []string{"tally"}) {
		t.Fatalf("WriteOnlySymbol with Production=false over readwrite-writeonly.txtar reports %v, want [tally]", names)
	}

	tally := findingOf(t, found, writeOnlyCode, "tally")
	if tally.Symbol.Kind != "variable" || tally.Relation != graph.ReferenceCounting {
		t.Errorf("WriteOnlySymbol with Production=false reports tally as subject %q under %v, want variable under %v",
			tally.Symbol.Kind, tally.Relation, graph.ReferenceCounting)
	}
	if want := "variable tally is written at 2 positions and never read"; tally.Message != want {
		t.Errorf("WriteOnlySymbol with Production=false reports tally with message %q, want %q", tally.Message, want)
	}
	want := []string{"main.go:31:2", "main.go:32:2"}
	if got := writePositions(tally); !slices.Equal(got, want) {
		t.Errorf("WriteOnlySymbol with Production=false names write positions %v for tally, want %v", got, want)
	}
	for _, p := range tally.Details.WritePositions {
		if p.EndLine != p.Line {
			t.Errorf("WriteOnlySymbol with Production=false names write position %s:%d:%d ending on line %d, want line %d",
				p.Path, p.Line, p.Column, p.EndLine, p.Line)
		}
	}
}

func TestWriteOnlySymbolCountsATestReadAsNoReadUnderProductionModeAlone(t *testing.T) {
	for _, production := range []bool{false, true} {
		t.Run("production="+strconv.FormatBool(production), func(t *testing.T) {
			in := inputOf(t, "readwrite-writeonly.txtar", applicationConfig(), Consumers{})
			in.Mode.Production = production
			found := computed(t, in, map[string]Emitter{writeOnlyCode: WriteOnlySymbol}).Findings

			want := []string{"tally"}
			if production {
				want = []string{"counter.hits", "tally"}
			}
			names := namesUnder(found, writeOnlyCode)
			slices.Sort(names)
			if !slices.Equal(names, want) {
				t.Fatalf("WriteOnlySymbol with Production=%t over readwrite-writeonly.txtar reports %v, want %v",
					production, names, want)
			}
			if !production {
				return
			}
			hits := findingOf(t, found, writeOnlyCode, "counter.hits")
			if want := "field counter.hits is written once and never read"; hits.Message != want {
				t.Errorf("WriteOnlySymbol with Production=true reports counter.hits with message %q, want %q", hits.Message, want)
			}
			if got := writePositions(hits); !slices.Equal(got, []string{"main.go:21:4"}) {
				t.Errorf("WriteOnlySymbol with Production=true names write positions %v for counter.hits, want [main.go:21:4]", got)
			}
		})
	}
}

func TestWriteOnlySymbolReportsNoDeclarationAnExemptionHoldsLive(t *testing.T) {
	in := inputOf(t, "readwrite-retained.txtar", applicationConfig(), Consumers{})

	staged := false
	for _, exemption := range in.Exempt {
		if symbol := in.symbol(exemption.ID); symbol != nil && symbol.Name == "answer.Status" {
			staged = true
		}
	}
	if !staged {
		t.Fatalf("Setup: readwrite-retained.txtar exempts no field of answer, so the fixture stages nothing")
	}

	found := computed(t, in, map[string]Emitter{writeOnlyCode: WriteOnlySymbol}).Findings
	if names := namesUnder(found, writeOnlyCode); len(names) != 0 {
		t.Errorf("WriteOnlySymbol under a production mode over readwrite-retained.txtar reports %v, want nothing", names)
	}
}

func TestUnusedEnumMemberReportsTheGroupNoConversionRetainsAtTheDerivedClass(t *testing.T) {
	in := inputOf(t, "readwrite-enum.txtar", libraryConfig(), Consumers{})

	found := computed(t, in, map[string]Emitter{enumMemberCode: UnusedEnumMember}).Findings
	if names := namesUnder(found, enumMemberCode); !slices.Equal(names, []string{"modeWrite"}) {
		t.Fatalf("UnusedEnumMember over readwrite-enum.txtar reports %v, want [modeWrite]", names)
	}

	member := findingOf(t, found, enumMemberCode, "modeWrite")
	if member.Symbol.Kind != enumMemberSubject {
		t.Errorf("UnusedEnumMember reports modeWrite as subject %q, want %q",
			member.Symbol.Kind, enumMemberSubject)
	}
	if member.Class != Certain || member.Confidence != Certain {
		t.Errorf("UnusedEnumMember reports modeWrite, whose type is unexported, at class %q and confidence %q, want certain and certain",
			member.Class, member.Confidence)
	}
	if want := "enumerated member modeWrite of mode is named nowhere"; member.Message != want {
		t.Errorf("UnusedEnumMember reports modeWrite with message %q, want %q", member.Message, want)
	}
	if member.Fixability != "deletable" {
		t.Errorf("UnusedEnumMember reports modeWrite with fixability %q, want deletable", member.Fixability)
	}
}

func TestUnusedEnumMemberReportsNoMemberTheEnumGroupClassRetains(t *testing.T) {
	in := inputOf(t, "readwrite-enum.txtar", libraryConfig(), Consumers{})

	retained := make(map[string]bool)
	for _, exemption := range in.Sweep.Retained {
		if exemption.Class != string(exempt.EnumGroup) {
			continue
		}
		if symbol := in.symbol(exemption.ID); symbol != nil {
			retained[symbol.Name] = true
		}
	}
	for _, name := range []string{"levelHigh", "stateIdle", "stateBusy"} {
		if !retained[name] {
			t.Errorf("the sweep over readwrite-enum.txtar does not retain %s under enum-group, want it held back", name)
		}
	}
	if retained["modeWrite"] {
		t.Error("the sweep over readwrite-enum.txtar retains modeWrite under enum-group, want it reported instead")
	}
}

func TestEnumGroupMembersReadsAnIotaGroupTheWayTheExemptionDoes(t *testing.T) {
	in := inputOf(t, "readwrite-enum.txtar", libraryConfig(), Consumers{})

	var named []string
	for id, owner := range EnumGroupMembers(in) {
		symbol := in.symbol(id)
		if symbol == nil {
			t.Fatalf("EnumGroupMembers names %s, which the inventory does not hold", id)
		}
		named = append(named, symbol.Name+" of "+owner)
	}
	slices.Sort(named)
	want := []string{
		"levelHigh of level", "levelLow of level",
		"modeRead of mode", "modeWrite of mode",
		"stateBusy of state", "stateIdle of state",
	}
	if !slices.Equal(named, want) {
		t.Errorf("EnumGroupMembers over readwrite-enum.txtar = %v, want %v", named, want)
	}
}

func TestUnusedTypeParameterReportsAFunctionAndAMethodAtCertain(t *testing.T) {
	in := inputOf(t, "readwrite-typeparam.txtar", applicationConfig(), Consumers{})

	found := computed(t, in, map[string]Emitter{typeParameterCode: UnusedTypeParameter}).Findings
	names := namesUnder(found, typeParameterCode)
	slices.Sort(names)
	if want := []string{"convert[T]", "holder.take[T]"}; !slices.Equal(names, want) {
		t.Fatalf("UnusedTypeParameter over readwrite-typeparam.txtar reports %v, want %v", names, want)
	}
	for _, name := range names {
		parameter := findingOf(t, found, typeParameterCode, name)
		if parameter.Symbol.Kind != "type-parameter" {
			t.Errorf("UnusedTypeParameter reports %s as subject %q, want type-parameter", name, parameter.Symbol.Kind)
		}
		if parameter.Class != Certain || parameter.Confidence != Certain || parameter.Fixability != "deletable" {
			t.Errorf("UnusedTypeParameter reports %s at class %q, confidence %q and fixability %q, want certain, certain and deletable",
				name, parameter.Class, parameter.Confidence, parameter.Fixability)
		}
		if want := "type parameter " + name + " is named in neither its signature nor its body"; parameter.Message != want {
			t.Errorf("UnusedTypeParameter reports %s with message %q, want %q", name, parameter.Message, want)
		}
	}
}

func TestUnusedTypeParameterTakesNoSubjectFromATypeDeclaration(t *testing.T) {
	for name, owner := range map[string]*graph.Symbol{
		"none":     nil,
		"function": {Kind: graph.KindFunc},
		"method":   {Kind: graph.KindMethod},
		"type":     {Kind: graph.KindType},
		"variable": {Kind: graph.KindVar},
	} {
		t.Run(name, func(t *testing.T) {
			want := name == "function" || name == "method"
			if got := declaresTypeParameters(owner); got != want {
				t.Errorf("declaresTypeParameters(a %s container) = %t, want %t", name, got, want)
			}
		})
	}

	in := inputOf(t, "readwrite-typeparam.txtar", applicationConfig(), Consumers{})
	for i := range in.Merged.Symbols {
		symbol := &in.Merged.Symbols[i]
		if symbol.Kind != graph.KindTypeParam {
			continue
		}
		if !declaresTypeParameters(in.symbol(symbol.Parent)) {
			t.Errorf("the inventory holds %s, a type parameter of a container whose parameters nothing reports",
				symbol.Name)
		}
	}
}

func TestConfidenceEqualsTheDerivedClassForBothCertainReadAndWriteKinds(t *testing.T) {
	in := inputOf(t, "readwrite-confidence.txtar", applicationConfig(), Consumers{})

	found := computed(t, in, map[string]Emitter{
		enumMemberCode:    UnusedEnumMember,
		typeParameterCode: UnusedTypeParameter,
	}).Findings
	if codes := codesOf(found); !slices.Equal(codes, []string{enumMemberCode, typeParameterCode}) {
		t.Fatalf("the read-and-write kinds over readwrite-confidence.txtar report %v, want %v in site order",
			summary(found), []string{enumMemberCode, typeParameterCode})
	}
	for _, finding := range found {
		if finding.Confidence != finding.Class || finding.Class != Certain {
			t.Errorf("%s reports %s at class %q and confidence %q, want certain equal to the class",
				finding.Code, finding.Symbol.Name, finding.Class, finding.Confidence)
		}
	}
}

// writePositions renders the write positions one finding names, as a path, a line
// and a column.
func writePositions(found Finding) []string {
	rendered := make([]string, 0, len(found.Details.WritePositions))
	for _, p := range found.Details.WritePositions {
		rendered = append(rendered, p.Path+":"+strconv.Itoa(p.Line)+":"+strconv.Itoa(p.Column))
	}
	return rendered
}

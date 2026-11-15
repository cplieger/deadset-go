package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// blankedArchive is an application whose constant blocks hold a specification of
// one name stating a list the next specification repeats, one per code a constant
// can be reported under, first or in the middle of its block, beside two blocks
// whose first specification the next one does not repeat.
func blankedArchive() map[string]string {
	return map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
		"level/level.go":   "// Package level names levels.\npackage level\n\nconst (\n\tLow = iota\n\tHigh\n)\n",
		"main.go": `package main

import (
	"os"

	"example.com/app/level"
)

const (
	Quiet = iota
	loud
)

const (
	probeA = iota
	probeB
)

const (
	// Deprecated: colorRed replaces it.
	colorOld = iota
	colorRed
)

type state int

const (
	stateUnknown state = iota
	stateIdle
)

type mode int

const (
	modeOff mode = iota
	modeOn
)

const (
	spareA = 1
	spareB = 2
)

const (
	midA = iota
	midB = iota * 10
	midC
)

const (
	Base = 1
	Step = iota * 10
	Next
)

func modes(n int) bool { return mode(n) == mode(0) }

func locals() int {
	const (
		localFirst = iota
		localSecond
	)
	const (
		localA = 1
		localB = 2
	)
	return localSecond + localB
}

func main() {
	println(level.High, loud, probeB, colorRed, stateIdle, modes(len(os.Args)), locals(), spareB, midA, midC, Base, Step, Next)
}
`,
		"main_test.go": "package main\n\nimport \"testing\"\n\n" +
			"func TestProbe(t *testing.T) {\n\tif probeA+probeB != 1 {\n\t\tt.Fatal(\"probe values moved\")\n\t}\n}\n",
	}
}

// Deleting the only name of a specification stating a list the next specification
// repeats replaces the name with _, which the message of a finding that deletes it
// says whatever its code and place, a function body's block included, and that of
// a narrowing finding, which deletes nothing, does not. It carries manual
// fixability where a constant repeating the list is live. A specification the
// next one does not repeat deletes as any other.
func TestAConstantWhoseDeletionLeavesABlankNameSaysSoInItsMessage(t *testing.T) {
	t.Parallel()
	const hint = "replace the name with _"
	envelope := cachedEnvelope(t.Context(), t, blankedArchive())
	want := []string{
		"Low DS1001 manual true",
		"Quiet DS1101 manual true",
		"Step DS1501 narrowable false",
		"colorOld DS1104 manual true",
		"localA DS1002 deletable false",
		"localFirst DS1002 manual true",
		"midB DS1002 manual true",
		"modeOff DS1005 deletable true",
		"modeOn DS1005 deletable false",
		"probeA DS1102 manual true",
		"spareA DS1002 deletable false",
		"stateUnknown DS1005 manual true",
	}
	subjects := make(map[string]bool, len(want))
	for _, row := range want {
		subjects[strings.Fields(row)[0]] = true
	}
	var got []string
	for i := range envelope.Findings {
		f := &envelope.Findings[i]
		if subjects[f.Symbol.Name] {
			got = append(got, f.Symbol.Name+" "+string(f.Code)+" "+string(f.Fixability)+" "+strconv.FormatBool(strings.Contains(f.Message, hint)))
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("analyze reports (name, code, fixability, message says %q)\n%q\nwant\n%q", hint, got, want)
	}
}

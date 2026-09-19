package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/internal/kinds"
	"pgregory.net/rapid"
)

// libraryPackage is the directory of the generated library, which is the tree every
// finding the property compares is about.
const libraryPackage = "lib"

// entryPointFile is the executable entry point the property adds and removes.
const entryPointFile = "cmd/app/main.go"

// drawLibrary draws one library tree: one package holding a few exported and
// unexported declarations, each referenced by the package's own exported entry
// declaration or by nothing.
//
// The first declaration is always exported and referenced by nothing, so the finding
// set is never empty under one declared kind and equal to it under the other: the
// property's second half asserts that the declared kind moves the answer, and a tree
// with nothing to report would satisfy that vacuously.
func drawLibrary(t *rapid.T, document string) map[string]string {
	count := rapid.IntRange(1, 4).Draw(t, "the number of declarations")
	names := make([]string, count)
	referenced := make([]bool, count)
	for i := range count {
		label := "declaration " + strconv.Itoa(i)
		names[i] = "d" + strconv.Itoa(i)
		if i == 0 || rapid.Bool().Draw(t, label+": exported") {
			names[i] = "D" + strconv.Itoa(i)
		}
		referenced[i] = i != 0 && rapid.Bool().Draw(t, label+": referenced by the entry declaration")
	}

	var held strings.Builder
	held.WriteString("// Package lib is a generated library.\npackage lib\n\n")
	held.WriteString("// Entry is the declaration a consumer of this library calls.\nfunc Entry() {\n")
	for i := range names {
		if referenced[i] {
			fmt.Fprintf(&held, "\t%s()\n", names[i])
		}
	}
	held.WriteString("}\n")
	for i := range names {
		fmt.Fprintf(&held, "\n// %s is a generated declaration.\nfunc %s() {}\n", names[i], names[i])
	}

	return map[string]string{
		"go.mod":                   "module example.com/app\n\ngo 1.27.1\n",
		libraryPackage + "/lib.go": held.String(),
		repositoryDocument:         document,
	}
}

// TestTheDeclaredTargetKindIsTheOnlyThingThatChangesTheTargetsTreatment is property
// dead-code-suite/P14: the declared target kind decides how the target is treated,
// and the presence of an executable entry point decides nothing.
//
// Three trees are analyzed per iteration: the library, the library with an executable
// entry point added, and the library declared an application. The entry point
// references nothing of the library, so it adds declarations and no reference: the
// findings about the library's own package must therefore be identical across the
// first two, and adding or removing it is what Requirement 27.9 says must change
// nothing. The third tree is the same source under the other declared kind, and its
// finding set must differ, because the kind is what the treatment turns on.
//
// One iteration analyzes three modules, each of which carries no test file and imports
// nothing, so each analysis is the cheap one. An archive a later iteration draws again
// is analyzed once.
func TestTheDeclaredTargetKindIsTheOnlyThingThatChangesTheTargetsTreatment(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		const library = `{"target": {"kind": "library"}}`
		files := drawLibrary(t, library)

		withEntry := make(map[string]string, len(files)+1)
		maps.Copy(withEntry, files)
		withEntry[entryPointFile] = "// Command app is an executable entry point that references nothing of the\n" +
			"// library beside it.\npackage main\n\nfunc main() {}\n"

		asApplication := make(map[string]string, len(files))
		maps.Copy(asApplication, files)
		asApplication[repositoryDocument] = `{"target": {"kind": "application"}}`

		plain := libraryFindings(t, files)
		entried := libraryFindings(t, withEntry)
		if !slices.Equal(plain, entried) {
			t.Fatalf("adding an executable entry point moved the findings about the library:\n%s\n%s",
				strings.Join(plain, "\n"), strings.Join(entried, "\n"))
		}

		application := libraryFindings(t, asApplication)
		if slices.Equal(plain, application) {
			t.Fatalf("the declared target kind moved no finding about the library:\n%s",
				strings.Join(plain, "\n"))
		}
	})
}

// libraryFindings is every finding of one run about the library package, rendered as
// the fields a reader of the report compares: the position, the code, the reference,
// the reachability class, the confidence and the severity.
//
// The findings are restricted to the library's own package, because a tree carrying an
// entry point declares declarations the other does not and a finding about one of
// those is a new declaration rather than a changed treatment.
func libraryFindings(t *rapid.T, files map[string]string) []string {
	set := cachedFindings(t.Context(), t, files)
	var held []string
	for i := range set.result.Findings {
		found := &set.result.Findings[i]
		if !strings.HasPrefix(found.Position.Path, libraryPackage+"/") {
			continue
		}
		held = append(held, renderedFinding(found))
	}
	return held
}

// renderedFinding is one finding as the property compares it.
func renderedFinding(found *kinds.Finding) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
		positionKey(&found.Position), found.Code, found.Symbol.Ref,
		found.Class, found.Confidence, found.Severity)
}

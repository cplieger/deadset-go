package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// toolsModule is the one module outside the standard library this analyzer's
// program is built on, and the whole of its runtime budget: the type information
// the analysis reads has no reimplementation, and everything else the program does
// is the standard library's.
const toolsModule = "golang.org/x/tools"

// testOnlyModules are the requirements no program links: the property-testing
// instrument and the Contract, each read by a test alone.
var testOnlyModules = []string{"github.com/cplieger/deadset-spec", "pgregory.net/rapid"}

// TestTheBuiltBinaryLinksNothingOutsideTheBudget reads the built program and
// refuses a module the budget does not admit.
//
// The subject is the binary rather than the module file, because what a module file
// requires and what a program links are different sets: a test-only requirement is
// declared and never linked, and a module reaches the program through what it
// imports rather than through what is written down. So the assertion is made where
// the answer is, which is the build information the toolchain records in the binary
// itself.
//
// The admissible set is derived and not written down: golang.org/x/tools, and every
// module the main module needs only through a package of it. A module that arrives
// because x/tools started requiring it is therefore admitted without an edit here,
// and one that arrives any other way is reported whatever its name.
func TestTheBuiltBinaryLinksNothingOutsideTheBudget(t *testing.T) {
	binary := analyzerBinary(t)

	linked := linkedModules(t, binary)
	if !slices.Contains(linked, toolsModule) {
		t.Fatalf("the built binary links %v, want %s among them: the analysis reads type information through it",
			linked, toolsModule)
	}
	throughTools := tracedThroughTools(t, linked)
	for _, module := range linked {
		if module == toolsModule || throughTools[module] {
			continue
		}
		t.Errorf("the built binary links %s, which the budget does not admit: the budget is the standard library, %s, and the modules the main module needs only through %s",
			module, toolsModule, toolsModule)
	}
}

// TestTheModuleFileDeclaresNoDirectRequirementOutsideTheBudget reads the module
// file and refuses a direct requirement the budget does not admit.
//
// It is the assertion the one above cannot make: a requirement only a test links is
// inside the budget and absent from every binary, so nothing about the program says
// whether the module file declares a fourth one. A new direct requirement is a
// decision about the budget, and this is where that decision is refused.
func TestTheModuleFileDeclaresNoDirectRequirementOutsideTheBudget(t *testing.T) {
	want := append([]string{toolsModule}, testOnlyModules...)
	slices.Sort(want)

	got := directRequirements(t, moduleFilePath(t))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the module file requires %v directly, want %v: %s is the runtime budget and the others are read by a test alone",
			got, want, toolsModule)
	}
}

// linkedModules is every module the built program links, in the order the build
// information records them.
func linkedModules(t *testing.T, binary string) []string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", "version", "-m", binary)
	command.Env = append(os.Environ(), "GOWORK=off")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Setup: go version -m %s: %v\n%s", binary, err, out)
	}

	var linked []string
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "dep" {
			continue
		}
		linked = append(linked, fields[1])
	}
	return linked
}

// tracedThroughTools names the modules of one set the main module needs through a
// package of golang.org/x/tools, which are the modules x/tools itself links.
//
// The toolchain answers why each module is needed as a chain of packages from the
// main module to the package that imports it, and a chain holding a package of
// x/tools is a module that reached the program through x/tools. A module the main
// module imports itself has a chain that names no such package, so a direct import
// of something x/tools also happens to require is not admitted by this answer.
func tracedThroughTools(t *testing.T, modules []string) map[string]bool {
	t.Helper()

	if len(modules) == 0 {
		return nil
	}
	command := exec.CommandContext(t.Context(), "go", append([]string{"mod", "why", "-m"}, modules...)...)
	command.Env = append(os.Environ(), "GOWORK=off")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Setup: go mod why -m %v: %v\n%s", modules, err, out)
	}

	traced := make(map[string]bool, len(modules))
	var asked string
	for line := range strings.Lines(string(out)) {
		text := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(text, "# "):
			asked = strings.TrimPrefix(text, "# ")
		case asked == "":
			continue
		case text == toolsModule, strings.HasPrefix(text, toolsModule+"/"):
			traced[asked] = true
		}
	}
	return traced
}

// moduleFilePath is the module file of the module this command belongs to, as the
// toolchain resolves it, so the assertion reads the file the build reads rather
// than a path this test spells relative to its own directory.
func moduleFilePath(t *testing.T) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", "env", "GOMOD")
	command.Env = append(os.Environ(), "GOWORK=off")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Setup: go env GOMOD: %v\n%s", err, out)
	}
	path := strings.TrimSpace(string(out))
	if path == "" || path == os.DevNull {
		t.Fatalf("go env GOMOD answered %q, want the module file of this command", path)
	}
	return path
}

// directRequirements is every module one module file requires directly: the module
// path of each requirement no indirect comment marks.
//
// The file is read and parsed here rather than through a module-file library,
// because linking such a library would add to the budget this file measures.
func directRequirements(t *testing.T, path string) []string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read the module file %s: %v", path, err)
	}

	var direct []string
	block := false
	for line := range strings.Lines(string(body)) {
		text := strings.TrimSpace(line)
		var required string
		switch {
		case block && text == ")":
			block = false
		case text == "require (":
			block = true
		case strings.HasPrefix(text, "require "):
			required = requiredModule(strings.TrimPrefix(text, "require "))
		case block:
			required = requiredModule(text)
		}
		if required != "" {
			direct = append(direct, required)
		}
	}
	return direct
}

// requiredModule is the module path one line of a require directive names, and the
// empty string where the line names no direct requirement: a blank line, a comment
// of its own, or a requirement an indirect comment marks.
func requiredModule(text string) string {
	if strings.HasPrefix(text, "//") || strings.Contains(text, "// indirect") {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return fields[0]
}

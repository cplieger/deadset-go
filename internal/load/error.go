package load

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Diagnostic is one error a load reported, at the position the toolchain named.
type Diagnostic struct {
	Package  string // the package the error was reported against, empty when none
	Position string // file:line:col as the toolchain rendered it, empty when none
	Message  string
}

// Error reports every diagnostic one configuration's load produced. A load that
// returns one produced no analyzable package set, so no finding list follows it.
type Error struct {
	Configuration string

	// Module names the consumer the diagnostics came from, as the scope named it,
	// and is empty for the target. A run loads the target and every declared
	// consumer, so which module did not type-check is what a reader needs first.
	Module string

	Diagnostics []Diagnostic
}

// Error renders the configuration, the consumer where one is named, the number of
// diagnostics, and every diagnostic on its own line, so a caller that prints the
// error prints the whole list.
func (e *Error) Error() string {
	var b strings.Builder
	noun := "errors"
	if len(e.Diagnostics) == 1 {
		noun = "error"
	}
	fmt.Fprintf(&b, "load %s: ", e.Configuration)
	if e.Module != "" {
		fmt.Fprintf(&b, "consumer %s: ", e.Module)
	}
	fmt.Fprintf(&b, "%d %s", len(e.Diagnostics), noun)
	for _, d := range e.Diagnostics {
		b.WriteString("\n  ")
		if d.Position != "" {
			b.WriteString(d.Position)
			b.WriteString(": ")
		}
		b.WriteString(d.Message)
	}
	return b.String()
}

// compareDiagnostics is the total order the load reports diagnostics in, so a
// repeated run prints the same bytes. Position leads, because that is the order a
// reader walks the list in, and it puts every diagnostic of one source site
// together whichever package variant reported it.
func compareDiagnostics(a, b Diagnostic) int {
	return cmp.Or(
		cmp.Compare(a.Position, b.Position),
		cmp.Compare(a.Message, b.Message),
		cmp.Compare(a.Package, b.Package),
	)
}

// sameDiagnostic reports whether two adjacent diagnostics of the sorted list name
// one problem. A package and its test variant type-check the same files
// independently and report the same error twice, so a positioned diagnostic is
// keyed on its source site; one with no position is keyed on its package, which
// is all that distinguishes it.
func sameDiagnostic(a, b Diagnostic) bool {
	if a.Position != b.Position || a.Message != b.Message {
		return false
	}
	return a.Position != "" || a.Package == b.Package
}

// SetupClass names one class of setup failure: a file or a component the analysis
// needs and the project does not provide.
type SetupClass string

// The setup-failure classes a load answers, and the one a caller answers from the
// derived matrix.
const (
	MissingModule       SetupClass = "missing-module"
	IncompleteModuleSum SetupClass = "incomplete-module-sum"
	TestBuildTag        SetupClass = "test-build-tag"
	MissingConsumer     SetupClass = "missing-consumer"
)

// SetupFailure is one setup failure: its class and what is missing, with the fix.
type SetupFailure struct {
	Class  SetupClass
	Detail string
}

// Line is the failure as one line of standard error: the setup-failure prefix, the
// class and what is missing with the fix.
func (f SetupFailure) Line() string {
	return "setup failure: " + string(f.Class) + ": " + f.Detail
}

// SetupError reports every setup failure one run met. A run that returns one
// analyzed nothing, because every missing piece hides real uses.
type SetupError struct {
	Failures []SetupFailure
}

// Error renders one line per failure.
func (e *SetupError) Error() string {
	lines := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		lines[i] = f.Line()
	}
	return strings.Join(lines, "\n")
}

// The toolchain's messages that name a package nothing provides and a module sum
// file missing a checksum. The toolchain reports neither as a typed error, so its
// message is the only record of which one a listing met.
const (
	noModuleProvides = "no required module provides package "
	missingSumEntry  = "missing go.sum entry for module providing package "
	lookupDisabled   = "module lookup disabled by GOPROXY=off"
)

// setupFailures classifies a listing's diagnostics, and returns nil where none of
// them is a setup failure. A package nothing provides is a missing module only
// within a module the project expects, one of expected; an import of any other
// module is a compile error the project's own build reports too.
func setupFailures(diagnostics []Diagnostic, expected []string) *SetupError {
	var failures []SetupFailure
	for _, d := range diagnostics {
		at := d.Position
		if at == "" {
			at = d.Package
		}
		switch {
		case strings.Contains(d.Message, missingSumEntry):
			module, _, _ := strings.Cut(strings.SplitN(d.Message, missingSumEntry, 2)[1], " ")
			failures = append(failures, SetupFailure{Class: IncompleteModuleSum, Detail: fmt.Sprintf(
				"%s: the module sum file holds no checksum for the module providing %s; run go mod tidy in the module that requires it",
				at, module,
			)})
		case strings.Contains(d.Message, noModuleProvides) && within(packageNamed(d.Message), expected):
			pkg := packageNamed(d.Message)
			failures = append(failures, SetupFailure{Class: MissingModule, Detail: fmt.Sprintf(
				"%s: the import %s names a package nothing provides; run the generator, the build or the install that writes it",
				at, pkg,
			)})
		case strings.Contains(d.Message, lookupDisabled):
			failures = append(failures, SetupFailure{Class: MissingModule, Detail: fmt.Sprintf(
				"%s: %s; the required module is not installed, so run go mod download in the module that requires it",
				at, strings.TrimSuffix(d.Message, ": "+lookupDisabled),
			)})
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &SetupError{Failures: failures}
}

// packageNamed is the package a "no required module provides" message names.
func packageNamed(message string) string {
	_, rest, _ := strings.Cut(message, noModuleProvides)
	pkg, _, _ := strings.Cut(rest, ";")
	return pkg
}

// within reports whether pkg is one of modules or a package below one.
func within(pkg string, modules []string) bool {
	return slices.ContainsFunc(modules, func(module string) bool {
		return pkg == module || strings.HasPrefix(pkg, module+"/")
	})
}

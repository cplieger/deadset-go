// Package failure is a run that ends with exit code 3, each cause of one class of
// the Contract's failure_classes, and the one rendering of it on standard error:
// the first failure after its group's prefix as the run's one class line, every
// further one on a line of its own without a prefix.
package failure

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// product is the name a product fault names.
const product = "deadset-go"

// Failure is one cause that ends a run: its class and what fails, the fix
// included.
type Failure struct {
	Class  string
	Detail string
}

// Error is a run that ends with exit code 3 for its failures, in the order they
// were found.
type Error struct {
	err      error
	Failures []Failure
}

// Error is the failures' lines as [Write] prints them, without the last line feed.
func (e *Error) Error() string {
	var out strings.Builder
	write(&out, e.Failures)
	return strings.TrimSuffix(out.String(), "\n")
}

func (e *Error) Unwrap() error { return e.err }

// Setup reports whether err holds an [*Error] whose first failure is of the setup
// group: a cause in the project, which a derived configuration is dropped for.
func Setup(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && isSetup(e.Failures[0].Class)
}

// fixes is the fix each class states after what fails, for the classes whose
// detail is an error this package is handed.
var fixes = map[string]string{
	vocab.SetupUnreadableDocument: "make it a regular file inside the target root that the run can read, or remove it",
	vocab.RunUnwrittenOutput:      "name a path the run can write, or correct the template that renders it",
	vocab.RunUnreportablePath:     "run from a directory that holds the target, or analyze a target that holds the configuration",
	vocab.RunInterruptedRun:       "run it again without interrupting it",
	vocab.RunProductFault:         "reinstall " + product + ", and report the fault to its maintainers if a reinstalled " + product + " fails the same way",
}

// New is a failure of class whose detail names what fails and the fix.
func New(class, detail string) *Error {
	return &Error{Failures: []Failure{{Class: class, Detail: detail}}}
}

// Wrap is err as a failure of class, followed by the class's fix.
func Wrap(class string, err error) *Error {
	e := New(class, err.Error()+"; "+fixes[class])
	e.err = err
	return e
}

// Of is the failures err holds: an [*Error]'s, or a product fault naming an error
// no class names.
func Of(err error) []Failure {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Failures
	}
	return New(vocab.RunProductFault, fmt.Sprintf("%s failed: %v; %s", product, err, fixes[vocab.RunProductFault])).Failures
}

// Write prints failures on w, the first as the class line, each on one line.
func Write(w io.Writer, failures []Failure) error {
	var out strings.Builder
	write(&out, failures)
	_, err := io.WriteString(w, out.String())
	return err
}

func write(out *strings.Builder, failures []Failure) {
	for i, f := range failures {
		if i == 0 {
			out.WriteString(prefix(f.Class))
		}
		fmt.Fprintf(out, "%s: %s\n", f.Class, strings.Join(strings.FieldsFunc(f.Detail, func(r rune) bool { return r == '\n' || r == '\r' }), " "))
	}
}

func isSetup(class string) bool {
	return slices.ContainsFunc(vocab.SetupClasses(), func(c vocab.SetupClass) bool { return c.Name == class })
}

func prefix(class string) string {
	if isSetup(class) {
		return vocab.SetupFailurePrefix
	}
	return vocab.RunFailurePrefix
}

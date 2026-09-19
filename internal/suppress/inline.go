package suppress

import (
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/cplieger/deadset-go/internal/graph"
	"github.com/cplieger/deadset-go/internal/load"
)

// The three expressions the grammar fixes, applied to the text of each line
// comment in this order: the first recognizes a comment in the namespace, the
// second a well-formed directive, the third a directive that lacks only its
// reason. A comment matching none of the three is not a directive and nothing
// happens; one in the namespace that matches neither of the last two is
// malformed. They are transcribed from the published grammar and a test asserts
// each is byte-identical to the line that grammar carries.
var (
	directiveCandidate = regexp.MustCompile(`^//[ \t]*deadset:`)

	directiveWellFormed = regexp.MustCompile(`^//[ \t]*deadset:ignore[ \t]+(?<codes>DS[0-9]{4}(?:,DS[0-9]{4})*)[ \t]+--[ \t]+(?<reason>[^ \t\r\n][^\r\n]*?)[ \t]*$`)

	directiveNoReason = regexp.MustCompile(`^//[ \t]*deadset:ignore[ \t]+(?<codes>DS[0-9]{4}(?:,DS[0-9]{4})*)(?:[ \t]+--)?[ \t]*$`)
)

// directiveForm is the form a malformed directive is told to take.
const directiveForm = "//deadset:ignore DS0000[,DS0000...] -- <reason>"

// codeSeparator joins the codes of one directive, with no whitespace inside the
// list.
const codeSeparator = ","

// verdict is what the grammar's decision procedure says one comment is.
type verdict uint8

const (
	verdictNone      verdict = iota // not a directive: nothing happens, whatever else it says
	verdictDirective                // a directive: one record per code, each carrying the reason
	verdictNoReason                 // a directive with no reason: refused, and it binds nothing
	verdictMalformed                // in the namespace and malformed: the run ends
)

// classify applies the three expressions to the text of one comment, in the
// grammar's order, and returns what the comment is with the codes and the reason
// it names.
func classify(text string) (is verdict, codes []string, reason string) {
	if !directiveCandidate.MatchString(text) {
		return verdictNone, nil, ""
	}
	if match := directiveWellFormed.FindStringSubmatch(text); match != nil {
		return verdictDirective,
			codesOf(directiveWellFormed, match),
			match[directiveWellFormed.SubexpIndex("reason")]
	}
	if match := directiveNoReason.FindStringSubmatch(text); match != nil {
		return verdictNoReason, codesOf(directiveNoReason, match), ""
	}
	return verdictMalformed, nil, ""
}

// codesOf splits the code list one expression captured.
func codesOf(expression *regexp.Regexp, match []string) []string {
	return strings.Split(match[expression.SubexpIndex("codes")], codeSeparator)
}

// directive is one comment in the namespace: what the decision procedure made of
// it, what it named, and where it is written.
type directive struct {
	text   string
	reason string
	codes  []string
	at     token.Position
	is     verdict
}

// Inline reads every inline directive of one loaded configuration and binds each
// to the declarations written on the line below it.
//
// A directive names one or more codes and carries a reason, and it binds to every
// declaration whose own first line is the line below the directive's. Nothing
// else binds it: not a trailing directive on the declaration's own line, not a
// line two above with a blank line or a doc-comment line between, not a line
// below, so a directive anywhere else is a record bound to nothing. A directive
// that lacks only its reason is a [Refusal]; a comment in the namespace that is
// neither is a [MalformedError], and no record or refusal comes back with it.
//
// Every position is rendered the way a report carries it, so a file the target
// root does not hold ends the read rather than being passed over. The resolver is
// the run's own, over the same load and the same inventory, so a directive and the
// declaration below it are placed by one owner and Inline reaches the filesystem
// not at all.
func Inline(r *load.Result, resolve *graph.Resolver, symbols []graph.Symbol) ([]Record, []Refusal, error) {
	directives, err := namespaced(r, resolve)
	if err != nil {
		return nil, nil, err
	}

	below := declarationLines(symbols)
	var records []Record
	var refusals []Refusal
	for i := range directives {
		d := &directives[i]
		switch d.is {
		case verdictDirective:
			records = append(records, d.bind(below[site{file: d.at.Filename, line: d.at.Line + 1}])...)
		case verdictNoReason:
			refusals = append(refusals, d.refuse()...)
		case verdictNone, verdictMalformed:
			return nil, nil, &MalformedError{
				Text:      d.text,
				Want:      directiveForm,
				Site:      d.at,
				Mechanism: MechanismInline,
			}
		}
	}
	return records, refusals, nil
}

// bind returns one record per code the directive names and per declaration it
// bound, and one record per code bound to nothing where it bound none, which is
// the record a stale-suppression finding reports.
func (d *directive) bind(bound []graph.Symbol) []Record {
	records := make([]Record, 0, len(d.codes)*max(len(bound), 1))
	for _, code := range d.codes {
		record := Record{
			Code:      code,
			Path:      d.at.Filename,
			Reason:    d.reason,
			Site:      d.at,
			Mechanism: MechanismInline,
		}
		if len(bound) == 0 {
			records = append(records, record)
			continue
		}
		for i := range bound {
			record.Symbol, record.Bound = bound[i].Ref, bound[i].ID
			records = append(records, record)
		}
	}
	return records
}

// refuse returns one refusal per code a directive carrying no reason names,
// because the finding each becomes reports one code.
func (d *directive) refuse() []Refusal {
	refusals := make([]Refusal, 0, len(d.codes))
	for _, code := range d.codes {
		refusals = append(refusals, Refusal{
			Reported:  codeNoReason,
			Code:      code,
			Path:      d.at.Filename,
			Site:      d.at,
			Mechanism: MechanismInline,
		})
	}
	return refusals
}

// namespaced returns every line comment of the configuration that is in the
// namespace, in file order.
//
// One comment is read once whatever the number of package variants that
// type-check the file holding it, because the load parses a file once and every
// variant carries the same syntax tree. The order the load reported the packages
// in does not reach the result: the comments are ordered by rendered position, so
// the first malformed directive a read refuses is the first one written.
func namespaced(r *load.Result, resolve *graph.Resolver) ([]directive, error) {
	read := &comments{resolve: resolve, seen: make(map[token.Pos]bool)}
	for _, p := range r.Packages {
		for _, f := range p.Syntax {
			if err := read.file(f); err != nil {
				return nil, err
			}
		}
	}
	found := read.found
	slices.SortFunc(found, func(a, b directive) int { return graph.ByPosition(a.at, b.at) })
	return found, nil
}

// comments accumulates the line comments of one configuration that are in the
// namespace, one per source site.
type comments struct {
	resolve *graph.Resolver
	seen    map[token.Pos]bool
	found   []directive
}

// file keeps every comment of one syntax tree that is in the namespace. A
// position that does not render ends the read, because a comment the analysis
// cannot place is a directive it cannot bind.
func (c *comments) file(f *ast.File) error {
	for _, group := range f.Comments {
		for _, comment := range group.List {
			text := lineText(comment)
			is, codes, reason := classify(text)
			if is == verdictNone || c.seen[comment.Slash] {
				continue
			}
			c.seen[comment.Slash] = true
			at, err := c.resolve.Render(comment.Slash)
			if err != nil {
				return err
			}
			c.found = append(c.found, directive{text: text, reason: reason, codes: codes, at: at, is: is})
		}
	}
	return nil
}

// lineText is the text of one comment from its first solidus to the end of its
// line. A file whose lines end in a carriage return and a line feed carries the
// carriage return in neither, because it terminates the line rather than
// belonging to it.
func lineText(c *ast.Comment) string {
	return strings.TrimSuffix(c.Text, "\r")
}

// declarationLines indexes the inventory by the line each declaration begins on,
// which is the line of the keyword of a declaration, of the name of a
// specification inside a grouped declaration, of a field name inside a struct, or
// of a method name inside an interface. A line that declares several symbols
// holds each of them, and a line that opens a group declares none.
//
// A file and a package are left out. Neither is a declaration of the language, so
// neither has a first token for a directive to sit above, and a directive
// covering a whole file is not admitted: a suppression names one site so that its
// staleness can be decided at that site.
func declarationLines(symbols []graph.Symbol) map[site][]graph.Symbol {
	below := make(map[site][]graph.Symbol, len(symbols))
	for i := range symbols {
		s := &symbols[i]
		if s.Kind == graph.KindFile || s.Kind == graph.KindPackage {
			continue
		}
		at := site{file: s.Pos.Filename, line: s.Pos.Line}
		below[at] = append(below[at], *s)
	}
	return below
}

// site names one line of one file, which is what binds a directive to the
// declarations written on the line below it.
type site struct {
	file string
	line int
}

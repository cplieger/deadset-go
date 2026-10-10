package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

// templateName is the name an error gives the user's template.
const templateName = "report"

// subsetNames are the functions the template subset admits. The ones whose
// text/template builtin already behaves as the subset states are absent from
// templateFuncs.
var subsetNames = []string{
	"and", "or", "not", "len", "index", "eq", "ne", "lt", "le", "gt", "ge", "print", "printf", "println",
}

// rangeGuard is the function a parsed range calls on its value. No template can
// name it, because the parser admits subsetNames alone.
const rangeGuard = "deadsetRange"

// templateFuncs is every function of the subset whose meaning differs from
// text/template's builtin of that name, and the function a parsed range calls.
var templateFuncs = template.FuncMap{
	"len":      length,
	"index":    index,
	"eq":       equal,
	"ne":       notEqual,
	"lt":       ordered(func(c int) bool { return c < 0 }),
	"le":       ordered(func(c int) bool { return c <= 0 }),
	"gt":       ordered(func(c int) bool { return c > 0 }),
	"ge":       ordered(func(c int) bool { return c >= 0 }),
	"printf":   printf,
	rangeGuard: rangeable,
}

// integerConstant is the one number constant the subset admits.
var integerConstant = regexp.MustCompile(`^[+-]?(0|[1-9]\d*)$`)

// ParsedTemplate is a user template in the template subset, parsed, which [Options]
// carries to [Template].
type ParsedTemplate struct {
	parsed *template.Template
}

// ParseTemplate parses text in the template subset: text/template's action grammar
// with its fixed delimiters, the subset's functions, and no template definition,
// character constant, octal escape or number other than a decimal integer. A refusal
// names the line. A caller parses before it analyzes anything, because a refusal is
// an error of the invocation.
func ParseTemplate(text string) (*ParsedTemplate, error) {
	funcs := make(map[string]any, len(subsetNames))
	for _, name := range subsetNames {
		funcs[name] = struct{}{}
	}
	tree, trees := parse.New(templateName), map[string]*parse.Tree{}
	if _, err := tree.Parse(text, "", "", trees, funcs); err != nil {
		return nil, fmt.Errorf("report: parse the template: %w", err)
	}
	for name, defined := range trees {
		if defined != tree {
			return nil, fmt.Errorf("report: parse the template: %w",
				outside(defined, defined.Root, "the template definition "+strconv.Quote(name)))
		}
	}
	if err := subset(tree, tree.Root); err != nil {
		return nil, fmt.Errorf("report: parse the template: %w", err)
	}
	parsed, err := template.New(templateName).Option("missingkey=error").Funcs(templateFuncs).AddParseTree(templateName, tree)
	if err != nil {
		return nil, fmt.Errorf("report: parse the template: %w", err)
	}
	return &ParsedTemplate{parsed: parsed}, nil
}

// subset refuses a node outside the template subset, and makes a range evaluate its
// value through rangeGuard, which text/template's own range does not refuse as the
// subset does.
func subset(tree *parse.Tree, node parse.Node) error {
	switch n := node.(type) {
	case *parse.ListNode:
		return subsetAll(tree, n.Nodes...)
	case *parse.ActionNode:
		return subsetPipe(tree, n.Pipe)
	case *parse.IfNode:
		return subsetBranch(tree, &n.BranchNode)
	case *parse.WithNode:
		return subsetBranch(tree, &n.BranchNode)
	case *parse.RangeNode:
		n.Pipe.Cmds = append(n.Pipe.Cmds, call(tree, n.Pipe.Position(), rangeGuard))
		return subsetBranch(tree, &n.BranchNode)
	case *parse.TemplateNode:
		return outside(tree, n, "a template invocation")
	case *parse.PipeNode:
		return subsetPipe(tree, n)
	case *parse.ChainNode:
		return subset(tree, n.Node)
	case *parse.NumberNode:
		if !integerConstant.MatchString(n.Text) || !n.IsInt {
			return outside(tree, n, "the number "+n.Text)
		}
	case *parse.StringNode:
		if octalEscape(n.Quoted) {
			return outside(tree, n, "an octal escape")
		}
	}
	return nil
}

// subsetAll checks every node of a list in order.
func subsetAll(tree *parse.Tree, nodes ...parse.Node) error {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if err := subset(tree, node); err != nil {
			return err
		}
	}
	return nil
}

// subsetBranch checks an if, a with or a range: its pipeline and both lists.
func subsetBranch(tree *parse.Tree, n *parse.BranchNode) error {
	if err := subsetPipe(tree, n.Pipe); err != nil {
		return err
	}
	if n.ElseList == nil {
		return subset(tree, n.List)
	}
	return subsetAll(tree, n.List, n.ElseList)
}

// subsetPipe checks every argument of every command of a pipeline.
func subsetPipe(tree *parse.Tree, pipe *parse.PipeNode) error {
	for _, cmd := range pipe.Cmds {
		if err := subsetAll(tree, cmd.Args...); err != nil {
			return err
		}
	}
	return nil
}

// call is a command calling the function name with no argument of its own.
func call(tree *parse.Tree, at parse.Pos, name string) *parse.CommandNode {
	identifier := parse.NewIdentifier(name).SetTree(tree).SetPos(at)
	return &parse.CommandNode{NodeType: parse.NodeCommand, Pos: at, Args: []parse.Node{identifier}}
}

// outside is the refusal of a form the subset leaves out, naming its line.
func outside(tree *parse.Tree, node parse.Node, form string) error {
	location, _ := tree.ErrorContext(node)
	return fmt.Errorf("%s: %s is outside the template subset", location, form)
}

// octalEscape reports whether an interpreted string constant, as written, holds an
// octal escape.
func octalEscape(quoted string) bool {
	if !strings.HasPrefix(quoted, `"`) {
		return false
	}
	for i := 0; i < len(quoted)-1; i++ {
		if quoted[i] != '\\' {
			continue
		}
		if next := quoted[i+1]; next >= '0' && next <= '7' {
			return true
		}
		i++
	}
	return false
}

// Template renders the envelope through a template the user supplied, executed over
// the report document the envelope encodes to, by its JSON member names.
//
// A template that names something the document does not carry fails and names it,
// rather than rendering that place as nothing. The rendering is built whole before
// anything is written, so a template that fails writes no partial output.
func Template(w io.Writer, e *Envelope, opts Options) error {
	if opts.Template == nil {
		return fmt.Errorf("%w: a template rendering needs a template", errOptions)
	}
	document, err := encoded(e, "")
	if err != nil {
		return fmt.Errorf("report: encode the report document: %w", err)
	}
	return opts.Template.execute(w, document)
}

// execute writes the rendering of the template over one JSON document.
func (t *ParsedTemplate) execute(w io.Writer, document []byte) error {
	data, err := jsonValue(document)
	if err != nil {
		return fmt.Errorf("report: render the template: %w", err)
	}
	var rendered bytes.Buffer
	if err := t.parsed.Execute(&rendered, data); err != nil {
		return fmt.Errorf("%w: render the template: %w", errOptions, err)
	}
	if _, err := w.Write(rendered.Bytes()); err != nil {
		return fmt.Errorf("report: write the template rendering: %w", err)
	}
	return nil
}

// jsonValue is one JSON document as the value a template reads: an object is a
// map[string]any, an array a []any, a number an int64, and a string, a boolean and
// null are themselves.
func jsonValue(document []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("read the report document: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("read the report document: want one JSON value and nothing after it")
	}
	return integers(value)
}

// integers replaces every number of a decoded value by its int64.
func integers(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("read the report document: the number %s is not an integer", v)
		}
		return n, nil
	case []any:
		for i := range v {
			var err error
			if v[i], err = integers(v[i]); err != nil {
				return nil, err
			}
		}
	case map[string]any:
		for name, member := range v {
			replaced, err := integers(member)
			if err != nil {
				return nil, err
			}
			v[name] = replaced
		}
	}
	return value, nil
}

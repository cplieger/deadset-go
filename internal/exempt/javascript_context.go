package exempt

import (
	"errors"
	"html/template"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"
)

// escapeEntry names the template [escapeSet] executes, which no call names.
const escapeEntry = "\x00"

// escapeEnd names the template [guarded] calls last in each copy, which no call names.
const escapeEnd = "\x00end"

// maxEscapeRounds bounds the rounds of [encodesWhole]; a set needing more is read
// as encoding whole.
const maxEscapeRounds = 16

// jsEscapers are the escapers html/template adds to an action in a JavaScript value,
// string, template literal or regular expression. Each writes a struct with every
// field, as JSON or through fmt (escapeAction and js.go in go1.27.3
// src/html/template).
var jsEscapers = map[string]bool{
	"_html_template_jsvalescaper": true, "_html_template_jsstrescaper": true,
	"_html_template_jstmpllitescaper": true, "_html_template_jsregexpescaper": true,
}

var errEndContext = errors.New("a template ends in a context other than the one it starts in")

// encodesWhole reports whether html/template may write a value of a template of set
// with every field, which it does for an action it escapes in a JavaScript context
// (https://pkg.go.dev/html/template#hdr-Contexts), or escapes set in a way this
// cannot read: it rejects a template, a template ends in a context other than the
// one it starts in, or, where guessed says a name is the analysis's guess, a
// template is called in a context other than text.
func encodesWhole(set map[string]*parse.Tree, guessed bool) bool {
	if len(set) == 0 {
		return false
	}
	derived := make(map[string]string)
	for range maxEscapeRounds {
		roots, calls, err := escapeSet(set, derived)
		if err != nil || slices.ContainsFunc(roots, escapesJavaScript) {
			return true
		}
		// html/template renames a call it escapes outside text to a copy for that
		// context and reuses a template of that name the set holds (escapeTree in
		// go1.27.3 src/html/template/escape.go), so the next round adds that copy.
		added := 0
		for call, callee := range calls {
			if _, held := derived[call.Name]; call.Name != callee && !held {
				derived[call.Name] = callee
				added++
			}
		}
		if added == 0 || guessed {
			return added > 0
		}
	}
	return true
}

// escapeSet has html/template escape, in name order, a copy of every template of
// set as an entry and, under each name of derived, a copy of the template it maps
// to. It returns the copies' roots and the name each call of a copy had before the
// escape, or an error where the engine rejects a copy or a copy ends in a context
// other than the one it starts in.
func escapeSet(set map[string]*parse.Tree, derived map[string]string) ([]*parse.ListNode, map[*parse.TemplateNode]string, error) {
	names := slices.Sorted(maps.Keys(set))
	t, err := escapeTemplate(names)
	if err != nil {
		return nil, nil, err
	}
	roots := make([]*parse.ListNode, 0, len(set)+len(derived))
	ends := make(map[*parse.TemplateNode]string)
	for _, name := range append(names, slices.Sorted(maps.Keys(derived))...) {
		callee, isCopy := derived[name]
		if !isCopy {
			callee = name
		}
		tree, last := guarded(name, set[callee].Root)
		ends[last] = escapeEnd + strings.TrimPrefix(name, callee)
		if _, err := t.AddParseTree(name, tree); err != nil {
			return nil, nil, err
		}
		roots = append(roots, tree.Root)
	}
	calls := namedCalls(roots, ends)
	if err := t.Execute(io.Discard, nil); err != nil {
		return nil, nil, err
	}
	for last, want := range ends {
		if last.Name != want {
			return nil, nil, errEndContext
		}
	}
	return roots, calls, nil
}

// escapeTemplate is the entry calling each of names, in a set holding [escapeEnd].
func escapeTemplate(names []string) (*template.Template, error) {
	var entry strings.Builder
	for _, name := range names {
		entry.WriteString("{{template " + strconv.Quote(name) + " .}}")
	}
	t, err := template.New(escapeEntry).Parse(entry.String())
	if err != nil {
		return nil, err
	}
	end, _ := parse.Parse(escapeEnd, "{{if false}}{{end}}", "", "")
	_, err = t.AddParseTree(escapeEnd, end[escapeEnd])
	return t, err
}

// namedCalls is the name of each call under roots, the calls of ends left out.
func namedCalls(roots []*parse.ListNode, ends map[*parse.TemplateNode]string) map[*parse.TemplateNode]string {
	calls := make(map[*parse.TemplateNode]string)
	for _, root := range roots {
		eachNode(root, func(n parse.Node) {
			if call, isCall := n.(*parse.TemplateNode); isCall && ends[call] == "" {
				calls[call] = call.Name
			}
		})
	}
	return calls
}

// guarded is a copy of root as the body of {{if false}}, so executing it runs no
// action, ending in a call of [escapeEnd], which html/template renames by the
// body's end context. The engine caches a called template's start context as its
// end context (escapeTemplateBody in go1.27.3 src/html/template/escape.go), so a
// template ending in another context escapes differently by the order templates
// run in.
func guarded(name string, root *parse.ListNode) (*parse.Tree, *parse.TemplateNode) {
	trees, _ := parse.Parse(name, "{{if false}}{{template "+strconv.Quote(escapeEnd)+" .}}{{end}}", "", "")
	branch, _ := trees[name].Root.Nodes[0].(*parse.IfNode)
	last, _ := branch.List.Nodes[0].(*parse.TemplateNode)
	branch.List = root.CopyList()
	branch.List.Nodes = append(branch.List.Nodes, last)
	return trees[name], last
}

// escapesJavaScript reports whether html/template escaped an action under root with
// one of [jsEscapers].
func escapesJavaScript(root *parse.ListNode) bool {
	found := false
	eachNode(root, func(n parse.Node) {
		if action, isAction := n.(*parse.ActionNode); isAction {
			found = found || slices.ContainsFunc(action.Pipe.Cmds, func(cmd *parse.CommandNode) bool {
				id, isIdent := cmd.Args[0].(*parse.IdentifierNode)
				return isIdent && jsEscapers[id.Ident]
			})
		}
	})
	return found
}

func eachNode(n parse.Node, visit func(parse.Node)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n != nil {
			for _, child := range n.Nodes {
				eachNode(child, visit)
			}
		}
	case *parse.IfNode:
		eachNode(n.List, visit)
		eachNode(n.ElseList, visit)
	case *parse.RangeNode:
		eachNode(n.List, visit)
		eachNode(n.ElseList, visit)
	case *parse.WithNode:
		eachNode(n.List, visit)
		eachNode(n.ElseList, visit)
	default:
		visit(n)
	}
}

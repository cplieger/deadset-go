package exempt

import (
	"go/ast"
	"go/types"
	"maps"
	"strings"
	"text/template/parse"

	"github.com/cplieger/deadset-go/v2/internal/facts"
	"github.com/cplieger/deadset-go/v2/internal/resolve"
)

// typedExecution is one call executing a template set the analysis reads whole
// ([templateOrigins.receiverSet]), kept until the class solves: the trees of the
// set, the template the call starts with, the static type of the value it passes,
// and where the call is written.
type typedExecution struct {
	data  types.Type
	trees map[string]*parse.Tree
	call  *ast.CallExpr
	start string
}

// noteExecution keeps one execution of a template set for the typed walk, where
// the call executes a template by a name the analysis reads, passing a value whose
// static type is known. Any other execution reads only what the class reads of a
// destination ([destination.named]).
func (f *encodingFlow) noteExecution(info *types.Info, dir string, fn *types.Func, call *ast.CallExpr, callee facts.Callee) {
	if callee.Shift != 0 || !destinations[fn.Pkg().Path()].executions[fn.Name()] {
		return
	}
	inv := f.templates.receiverSet(info, dir, call)
	if inv == nil || inv.split {
		return
	}
	start, data := inv.receiver, 1
	if fn.Name() == executeTemplate {
		if len(call.Args) < 3 {
			return
		}
		names, ok := inv.flow.constants(call.Args[1], flowAt{info: info, dir: dir})
		if !ok || len(names) != 1 {
			return
		}
		start, data = names[0], 2
	}
	if start == "" || len(call.Args) <= data {
		return
	}
	f.executions = append(f.executions, typedExecution{trees: inv.typed, start: start, data: info.TypeOf(call.Args[data]), call: call})
}

// typeExecutions records what the typed walk of every kept execution reads.
func (f *encodingFlow) typeExecutions() error {
	for i := range f.executions {
		e := &f.executions[i]
		site, err := f.kept.site(e.call.Pos())
		if err != nil {
			return err
		}
		w := &templateWalk{kept: f.kept, trees: e.trees, site: site, visited: make(map[string]bool)}
		w.tree(e.start, e.data)
	}
	return nil
}

// templateWalk types the actions of the templates one execution runs as the engine
// binds dot and variables, and records on those static types what the engine
// reads: each field an action names, written or not, since an action naming a
// field the type lacks fails, and what fmt reads of a value an action prints
// ([recordPrinted]). A value it cannot type records nothing, nor does anything
// beneath it.
type templateWalk struct {
	kept    *retention
	trees   map[string]*parse.Tree
	visited map[string]bool // each template by name and the type of its dot
	site    evidence
}

// templateScope is what the actions of one place of a template read: the type of
// dot, nil where the walk cannot type it, and of each variable in scope, and the
// variables the template assigns after declaring them.
type templateScope struct {
	dot      types.Type
	vars     map[string]types.Type
	assigned map[string]bool
}

// child is a scope for the body of a control structure, whose variables end with it.
func (s *templateScope) child(dot types.Type) *templateScope {
	vars := make(map[string]types.Type, len(s.vars))
	maps.Copy(vars, s.vars)
	return &templateScope{dot: dot, vars: vars, assigned: s.assigned}
}

// variable is the type of one variable, nil where the template assigns it.
func (s *templateScope) variable(name string) types.Type {
	if s.assigned[name] {
		return nil
	}
	return s.vars[name]
}

// tree walks the template of one name with dot and $ of type dot.
func (w *templateWalk) tree(name string, dot types.Type) {
	tree := w.trees[name]
	if tree == nil || dot == nil {
		return
	}
	key := name + "\x00" + resolve.Spell(dot, nil)
	if w.visited[key] {
		return
	}
	w.visited[key] = true
	assigned := make(map[string]bool)
	assignedVariables(tree.Root, assigned)
	w.list(tree.Root, &templateScope{dot: dot, vars: map[string]types.Type{"$": dot}, assigned: assigned})
}

// list walks each node of one list in order, its declarations reaching the nodes
// after them.
func (w *templateWalk) list(list *parse.ListNode, s *templateScope) {
	if list == nil {
		return
	}
	for _, node := range list.Nodes {
		w.node(node, s)
	}
}

// node walks one node of a template.
func (w *templateWalk) node(node parse.Node, s *templateScope) {
	switch n := node.(type) {
	case *parse.ActionNode:
		t := w.pipe(n.Pipe, s, true)
		if len(n.Pipe.Decl) == 0 {
			w.print(t, n.String(), true, 'v', false)
		}
	case *parse.IfNode:
		inner := s.child(s.dot)
		w.pipe(n.Pipe, inner, true)
		w.list(n.List, inner)
		w.list(n.ElseList, inner)
	case *parse.WithNode:
		inner := s.child(s.dot)
		t := w.pipe(n.Pipe, inner, true)
		w.list(n.List, inner.child(t))
		w.list(n.ElseList, inner)
	case *parse.RangeNode:
		inner := s.child(s.dot)
		key, elem := rangeBinds(w.pipe(n.Pipe, inner, false))
		switch len(n.Pipe.Decl) {
		case 1:
			inner.vars[n.Pipe.Decl[0].Ident[0]] = elem
		case 2:
			inner.vars[n.Pipe.Decl[0].Ident[0]] = key
			inner.vars[n.Pipe.Decl[1].Ident[0]] = elem
		}
		w.list(n.List, inner.child(elem))
		w.list(n.ElseList, inner)
	case *parse.TemplateNode:
		var dot types.Type
		if n.Pipe != nil {
			dot = w.pipe(n.Pipe, s, true)
		}
		w.tree(n.Name, dot)
	case *parse.ListNode:
		w.list(n, s)
	}
}

// rangeBinds is the type of the key or index and of the element a range over a
// value of type t binds: an array's, a slice's or a map's, a channel's element, or
// an integer's values. Either is nil where the walk cannot type it.
func rangeBinds(t types.Type) (key, elem types.Type) {
	t = indirectType(t)
	if t == nil {
		return nil, nil
	}
	switch u := t.Underlying().(type) {
	case *types.Array:
		return types.Typ[types.Int], u.Elem()
	case *types.Slice:
		return types.Typ[types.Int], u.Elem()
	case *types.Map:
		return u.Key(), u.Elem()
	case *types.Chan:
		return nil, u.Elem()
	case *types.Basic:
		if u.Info()&types.IsInteger != 0 {
			return t, t
		}
	}
	return nil, nil
}

// pipe walks one pipeline and returns the type of its value, binding the variables
// it declares where bind says the pipeline's value is what they hold.
func (w *templateWalk) pipe(pipe *parse.PipeNode, s *templateScope, bind bool) types.Type {
	if pipe == nil {
		return nil
	}
	var value types.Type
	for i, cmd := range pipe.Cmds {
		value = w.command(cmd, s, value, i > 0)
	}
	if bind {
		for _, v := range pipe.Decl {
			s.vars[v.Ident[0]] = value
		}
	}
	return value
}

// command walks one command of a pipeline, given the value the previous command
// passes it as its final argument where piped says there is one, and returns the
// type of its value.
func (w *templateWalk) command(cmd *parse.CommandNode, s *templateScope, piped types.Type, isPiped bool) types.Type {
	if len(cmd.Args) == 0 {
		return nil
	}
	args := make([]types.Type, 0, len(cmd.Args))
	for _, arg := range cmd.Args[1:] {
		args = append(args, w.argument(arg, s))
	}
	if isPiped {
		args = append(args, piped)
	}
	switch first := cmd.Args[0].(type) {
	case *parse.IdentifierNode:
		return w.function(first.Ident, cmd, args)
	case *parse.FieldNode:
		return w.fields(s.dot, first.Ident, first, len(args) > 0)
	case *parse.ChainNode:
		return w.fields(w.argument(first.Node, s), first.Field, first, len(args) > 0)
	case *parse.VariableNode:
		return w.fields(s.variable(first.Ident[0]), first.Ident[1:], first, len(args) > 0)
	default:
		return w.argument(first, s)
	}
}

// argument walks one argument of a command and returns the type of its value.
func (w *templateWalk) argument(node parse.Node, s *templateScope) types.Type {
	switch n := node.(type) {
	case *parse.DotNode:
		return s.dot
	case *parse.FieldNode:
		return w.fields(s.dot, n.Ident, n, false)
	case *parse.ChainNode:
		return w.fields(w.argument(n.Node, s), n.Field, n, false)
	case *parse.VariableNode:
		return w.fields(s.variable(n.Ident[0]), n.Ident[1:], n, false)
	case *parse.PipeNode:
		return w.pipe(n, s, false)
	default:
		return nil
	}
}

// function is the type of the value of a call of one template function, recording
// what fmt reads of each argument it prints: print, println, html, js and urlquery
// print every argument with no format (evalArgs in go1.27.3
// src/text/template/funcs.go), and printf with its first. Their value is a string;
// any other function's is a value the walk does not type.
func (w *templateWalk) function(name string, cmd *parse.CommandNode, args []types.Type) types.Type {
	text := cmd.String()
	switch name {
	case "print", "println", "html", "js", "urlquery":
		for _, t := range args {
			w.print(t, text, true, 'v', false)
		}
	case "printf":
		w.printf(cmd, text, args)
	default:
		return nil
	}
	return types.Typ[types.String]
}

// printf records what fmt reads of each operand one printf call prints, through
// its format where the format is a constant string.
func (w *templateWalk) printf(cmd *parse.CommandNode, text string, args []types.Type) {
	if len(args) == 0 {
		return
	}
	operands := args[1:]
	var format *parse.StringNode
	if len(cmd.Args) > 1 {
		format, _ = cmd.Args[1].(*parse.StringNode)
	}
	if format == nil {
		for _, t := range operands {
			w.print(t, text, true, anyVerb, true)
		}
		return
	}
	bound := make(map[int]bool)
	for _, op := range stringOperands(format.Text, len(operands), stringVerbs).bound {
		bound[op.at] = true
	}
	for _, op := range printedOperands(format.Text, len(operands)) {
		w.print(operands[op.at], text, bound[op.at], op.verb, op.sharpV)
	}
}

// fields walks one chain of names on a value of type t and returns the type at its
// end, recording each field it names. The engine resolves a name to a method of the
// value first and to a field second, and to a map's element where the value is a
// map with string keys (evalField in go1.27.3 src/text/template/exec.go); a method's
// result, and a name a command passes arguments to, ends the walk.
func (w *templateWalk) fields(t types.Type, names []string, node parse.Node, called bool) types.Type {
	for i, name := range names {
		t = indirectType(t)
		if t == nil || i == len(names)-1 && called {
			return nil
		}
		if t = w.field(t, name, node); t == nil {
			return nil
		}
	}
	return t
}

// field is one step of [templateWalk.fields]: the type a name evaluates to on a
// value of type t, or nil where the name is a method or the walk cannot type it.
func (w *templateWalk) field(t types.Type, name string, node parse.Node) types.Type {
	if sel := types.NewMethodSet(types.NewPointer(t)).Lookup(nil, name); sel != nil {
		return nil
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		obj, index, _ := types.LookupFieldOrMethod(t, false, nil, name)
		field, isField := obj.(*types.Var)
		if !isField {
			return nil
		}
		w.named(t, index, node)
		return field.Type()
	case *types.Map:
		key, isBasic := u.Key().Underlying().(*types.Basic)
		if !isBasic || key.Info()&types.IsString == 0 {
			return nil
		}
		return u.Elem()
	default:
		return nil
	}
}

// named records each field one selection path of a struct type passes through, the
// embedded ones a promoted field is reached by included.
func (w *templateWalk) named(t types.Type, index []int, node parse.Node) {
	detail := "named by " + actionText(node.String())
	for _, i := range index {
		t = indirectType(t)
		st, isStruct := t.Underlying().(*types.Struct)
		if !isStruct {
			return
		}
		field := st.Field(i)
		w.kept.record(field, &w.site, detail)
		t = field.Type()
	}
}

// print records what fmt reads of one value an action prints under one verb, where
// the walk types it.
func (w *templateWalk) print(t types.Type, text string, str bool, verb rune, sharpV bool) {
	if t == nil {
		return
	}
	op := operand{verb: verb, sharpV: sharpV}
	recordPrinted(w.kept, t, &w.site, "formatted by "+actionText(text), str, op)
}

// indirectType is the type the engine evaluates a value of type t as: t through
// every pointer, nil where t is nil, an interface, a type parameter's type or a
// reflect.Value, whose dynamic value the static type does not give.
func indirectType(t types.Type) types.Type {
	for t != nil {
		t = types.Unalias(t)
		if isReflectHandle(t) {
			return nil
		}
		switch u := t.Underlying().(type) {
		case *types.Pointer:
			t = u.Elem()
		case *types.Interface:
			return nil
		default:
			return t
		}
	}
	return nil
}

// actionText is one node of a template as a clause spells it: in the action's
// braces, on one line.
func actionText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if strings.HasPrefix(text, "{{") {
		return text
	}
	return "{{" + text + "}}"
}

// assignedVariables adds to held every variable a node of a template assigns
// after declaring it, which the walk does not type.
func assignedVariables(node parse.Node, held map[string]bool) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, inner := range n.Nodes {
			assignedVariables(inner, held)
		}
	case *parse.ActionNode:
		assignedPipe(n.Pipe, held)
	case *parse.IfNode:
		assignedBranch(&n.BranchNode, held)
	case *parse.WithNode:
		assignedBranch(&n.BranchNode, held)
	case *parse.RangeNode:
		assignedBranch(&n.BranchNode, held)
	case *parse.TemplateNode:
		assignedPipe(n.Pipe, held)
	}
}

// assignedBranch is [assignedVariables] for a control structure.
func assignedBranch(n *parse.BranchNode, held map[string]bool) {
	assignedPipe(n.Pipe, held)
	assignedVariables(n.List, held)
	if n.ElseList != nil {
		assignedVariables(n.ElseList, held)
	}
}

// assignedPipe is [assignedVariables] for a pipeline and the pipelines its
// commands hold in parentheses.
func assignedPipe(pipe *parse.PipeNode, held map[string]bool) {
	if pipe == nil {
		return
	}
	if pipe.IsAssign {
		for _, v := range pipe.Decl {
			held[v.Ident[0]] = true
		}
	}
	for _, cmd := range pipe.Cmds {
		for _, arg := range cmd.Args {
			switch a := arg.(type) {
			case *parse.PipeNode:
				assignedPipe(a, held)
			case *parse.ChainNode:
				if inner, isPipe := a.Node.(*parse.PipeNode); isPipe {
					assignedPipe(inner, held)
				}
			}
		}
	}
}

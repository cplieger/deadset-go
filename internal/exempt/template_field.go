package exempt

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template/parse"
	"unicode/utf16"

	"github.com/cplieger/deadset-go/internal/graph"
)

// ErrTemplateDir reports a configured template directory the scan cannot read:
// one the target does not hold, one named outside the target root, or one whose
// walk fails. The class then scans nothing and would retain nothing, so a
// directory whose name no longer matches the tree refuses the run rather than
// silently disabling the exemption it was configured to enable.
var ErrTemplateDir = errors.New("exempt: template directory")

// TemplateFieldDetector retains every exported field and method whose name a
// template under the configured template directories names as a field or a
// method reference, on any type, and records the template file and line that
// named it.
//
// The evidence is a name written in a template rather than a type relation, so
// the class applies only where the project configured template directories: with
// none configured it scans nothing and retains nothing.
//
// Every file under a configured directory is read, whatever its name, and parsed
// with the action grammar text/template and html/template share, at the default
// delimiters. A field reference names the identifiers of its own chain, so
// {{ .Page.Title }} names both Page and Title, and a variable's chain names every
// identifier after the variable. A file the grammar cannot parse names nothing
// and is skipped: an unparsable file is not a template the project renders, and a
// run that refused it would fail on a fixture or a partial that is not the
// analyzer's business.
func TemplateFieldDetector(in *Input) ([]graph.Exemption, error) {
	dirs := templateDirs(in.Options.TemplateDirs)
	if len(dirs) == 0 {
		return nil, nil
	}

	var refs []reference
	for _, dir := range dirs {
		found, err := scanTemplateDir(in.Root, dir)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	index := newNameIndex(in)
	var out []graph.Exemption
	for _, r := range refs {
		for _, m := range index[r.name] {
			if !m.exported || !templateReaches(m.kind) {
				continue
			}
			out = append(out, graph.Exemption{
				ID:     m.id,
				Class:  string(TemplateField),
				Site:   r.site,
				Detail: "named by " + r.action,
			})
		}
	}
	slices.SortFunc(out, byEvidence)
	return out, nil
}

// templateReaches reports whether a template action can name a declaration of
// this kind. A template reaches the fields and the methods of the value it
// renders, and a method it reaches may be declared by a concrete type or by the
// interface the value carries.
func templateReaches(k graph.SymbolKind) bool {
	return k == graph.KindField || k == graph.KindMethod || k == graph.KindInterfaceMethod
}

// templateDirs cleans the configured directories into target-relative paths with
// forward slashes, dropping repetitions so one directory is scanned once.
func templateDirs(configured []string) []string {
	dirs := make([]string, 0, len(configured))
	for _, dir := range configured {
		clean := filepath.ToSlash(filepath.Clean(dir))
		if clean == "" || slices.Contains(dirs, clean) {
			continue
		}
		dirs = append(dirs, clean)
	}
	return dirs
}

// reference is one identifier a template action names, and where it is written.
type reference struct {
	name   string
	action string
	site   token.Position
}

// scanTemplateDir returns every identifier the templates under one
// target-relative directory name. The directory is opened as a root, so a
// symbolic link is not followed out of it and a path in the configuration cannot
// name a file the target does not hold.
func scanTemplateDir(root, dir string) ([]reference, error) {
	if !filepath.IsLocal(filepath.FromSlash(dir)) {
		return nil, fmt.Errorf("%w %s: outside the target root", ErrTemplateDir, dir)
	}
	opened, err := os.OpenRoot(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrTemplateDir, dir, err)
	}
	defer func() { _ = opened.Close() }()

	files := opened.FS()
	var refs []reference
	walk := func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		src, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		refs = append(refs, templateFileRefs(path.Join(dir, name), src)...)
		return nil
	}
	if err := fs.WalkDir(files, ".", walk); err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrTemplateDir, dir, err)
	}
	return refs, nil
}

// templateFileRefs returns every identifier the actions of one template file
// name, in the order the file writes them. The parse carries no function map and
// skips the function check, because the project's own functions are unknown here
// and whether a name is a function decides nothing the scan reads.
func templateFileRefs(name string, src []byte) []reference {
	tree := parse.New(name)
	tree.Mode = parse.SkipFuncCheck
	set := make(map[string]*parse.Tree)
	if _, err := tree.Parse(string(src), "", "", set); err != nil {
		return nil
	}

	// A file defines the template it is plus one tree per define action, and an
	// action inside a definition names what one inside the file body names.
	defined := make([]string, 0, len(set))
	for name := range set {
		defined = append(defined, name)
	}
	slices.Sort(defined)

	var refs []reference
	for _, d := range defined {
		if t := set[d]; t != nil {
			walkTemplate(t.Root, "", name, src, &refs)
		}
	}
	slices.SortFunc(refs, func(a, b reference) int {
		if c := a.site.Offset - b.site.Offset; c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	return refs
}

// walkTemplate collects the identifiers the nodes under n name. action is the
// text of the pipeline the walk is inside, which is what a maintainer reads at
// the site: a reference inside a branch or a template action carries its own
// pipeline's text rather than the whole clause.
func walkTemplate(n parse.Node, action, file string, src []byte, refs *[]reference) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n != nil {
			walkNodes(n.Nodes, action, file, src, refs)
		}
	case *parse.PipeNode:
		walkPipe(n, file, src, refs)
	case *parse.CommandNode:
		walkNodes(n.Args, action, file, src, refs)
	case *parse.ActionNode:
		walkTemplate(n.Pipe, action, file, src, refs)
	case *parse.TemplateNode:
		walkTemplate(n.Pipe, action, file, src, refs)
	case *parse.IfNode:
		walkBranch(&n.BranchNode, action, file, src, refs)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, action, file, src, refs)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, action, file, src, refs)
	case *parse.FieldNode:
		addRefs(n.Ident, int(n.Position()), action, file, src, refs)
	case *parse.VariableNode:
		// The first identifier is the variable's own name and the rest is the
		// field chain read from it, so a variable with no chain names nothing.
		addRefs(n.Ident[1:], int(n.Position()), action, file, src, refs)
	}
}

// walkNodes collects the identifiers a sequence of nodes names.
func walkNodes(nodes []parse.Node, action, file string, src []byte, refs *[]reference) {
	for _, n := range nodes {
		walkTemplate(n, action, file, src, refs)
	}
}

// walkPipe collects the identifiers one pipeline names. The pipeline's own text
// is the action text every site under it records, and a declaration names the
// variable it introduces rather than a field, so only the commands are walked.
func walkPipe(p *parse.PipeNode, file string, src []byte, refs *[]reference) {
	if p == nil {
		return
	}
	action := "{{" + p.String() + "}}"
	for _, c := range p.Cmds {
		walkTemplate(c, action, file, src, refs)
	}
}

// walkBranch collects the identifiers one branch action names, in its pipeline
// and in both its bodies.
func walkBranch(b *parse.BranchNode, action, file string, src []byte, refs *[]reference) {
	walkTemplate(b.Pipe, action, file, src, refs)
	walkTemplate(b.List, action, file, src, refs)
	walkTemplate(b.ElseList, action, file, src, refs)
}

// addRefs records one reference per identifier of a chain, each at the position
// the chain starts, which is the position the grammar gives a field chain
// whatever its length: the site is where the reference is written, and a chain
// carries one.
func addRefs(idents []string, at int, action, file string, src []byte, refs *[]reference) {
	site := templatePosition(file, src, at)
	for _, name := range idents {
		*refs = append(*refs, reference{name: name, action: action, site: site})
	}
}

// templatePosition renders one byte offset in a template file as the position a
// report carries: the target-relative path of the file, the line, and a column
// counting the UTF-16 code units before the offset on that line.
func templatePosition(file string, src []byte, at int) token.Position {
	at = min(max(at, 0), len(src))
	before := src[:at]
	line := 1 + strings.Count(string(before), "\n")
	start := strings.LastIndexByte(string(before), '\n') + 1

	column := 1
	for _, r := range string(src[start:at]) {
		if units := utf16.RuneLen(r); units > 0 {
			column += units
			continue
		}
		column++
	}
	return token.Position{Filename: file, Offset: at, Line: line, Column: column}
}

// member is one declaration of the inventory a name reaches.
type member struct {
	id       graph.SymbolID
	kind     graph.SymbolKind
	exported bool
}

// nameIndex maps a declared identifier to the members of the inventory that
// carry it. It is what the two classes whose evidence is a name rather than a
// type relation read: a template action and a reflective lookup both hold a name
// and nothing else, so each retains every declaration of that name its own
// mechanism can reach.
type nameIndex map[string][]member

// newNameIndex indexes the inventory by the identifier each declaration is
// written with, taken from the type checker rather than from a symbol's rendered
// name. A declaration outside the inventory, a local variable among them, is not
// indexed.
func newNameIndex(in *Input) nameIndex {
	symbols := make(map[graph.SymbolID]*graph.Symbol, len(in.Symbols))
	for i := range in.Symbols {
		symbols[in.Symbols[i].ID] = &in.Symbols[i]
	}

	index := make(nameIndex)
	for _, p := range in.Result.Packages {
		if p.TypesInfo == nil {
			continue
		}
		for _, obj := range p.TypesInfo.Defs {
			index.add(in.Resolve, symbols, obj)
		}
	}
	for name := range index {
		slices.SortFunc(index[name], func(a, b member) int { return strings.Compare(string(a.id), string(b.id)) })
	}
	return index
}

// add indexes one declared object under the identifier it is written with. One
// declaration is defined once per package variant that type-checks its file, and
// every variant resolves to the one symbol of that site, so the second and later
// variants add nothing.
func (x nameIndex) add(resolve *graph.Resolver, symbols map[graph.SymbolID]*graph.Symbol, obj types.Object) {
	if obj == nil {
		return
	}
	id, held := resolve.Object(obj)
	if !held {
		return
	}
	s := symbols[id]
	if s == nil {
		return
	}
	name := obj.Name()
	if slices.ContainsFunc(x[name], func(m member) bool { return m.id == id }) {
		return
	}
	x[name] = append(x[name], member{id: id, kind: s.Kind, exported: s.Exported})
}

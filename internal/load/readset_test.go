package load

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"testing"
)

// Only the compiled file lists enter the read set and the counted set
// ([program.4]): no function of the package but [readFiles.addCompiled] and
// [readFiles.merge] writes their field, [readFiles.holds] alone reads it, only
// [newReadFiles] fills a literal, and only [compiledFiles] calls addCompiled, on
// the file a range over a package's GoFiles gives. Any other site in a production
// file fails the test ([readSetSites]).
func TestOnlyCompiledFileListsEnterTheReadSet(t *testing.T) {
	t.Parallel()
	if sites := readSetSites(t, productionSources(t)); len(sites) > 0 {
		t.Errorf("%v may put into the read set a file no compiled list holds; add it through compiledFiles", sites)
	}
}

// Every form of putting a file into the read set other than a compiled list's is
// refused, round 8's IgnoredFiles range among them; the package's own forms are
// not.
func TestReadSetSitesRefusesEveryOtherWayIntoTheReadSet(t *testing.T) {
	t.Parallel()
	const head = "package p\n\ntype readFiles struct{ compiledByDir map[string]map[string]bool }\n\n" +
		"type pkg struct{ GoFiles, IgnoredFiles []string }\n\n"
	for _, tc := range []struct{ name, body string }{
		{"a write to the field from another function", "func f(r readFiles) { r.compiledByDir[\"d\"] = nil }"},
		{"an alias of the field", "func f(r readFiles) { m := r.compiledByDir; m[\"d\"] = nil }"},
		{"a delete from the field", "func f(r readFiles) { delete(r.compiledByDir, \"d\") }"},
		{"the field passed to a call", "func f(r readFiles, g func(map[string]map[string]bool)) { g(r.compiledByDir) }"},
		{"a literal filled elsewhere", "func f(m map[string]map[string]bool) readFiles { return readFiles{compiledByDir: m} }"},
		{"addCompiled outside compiledFiles", "func f(r readFiles) { r.addCompiled(\"d\", \"x.go\") }"},
		{"addCompiled over the ignored files", "func compiledFiles(r readFiles, p *pkg) {\n\tfor _, file := range p.IgnoredFiles {\n\t\tr.addCompiled(\"d\", file)\n\t}\n}"},
		{"addCompiled as a method value", "func compiledFiles(r readFiles) { add := r.addCompiled; add(\"d\", \"x.go\") }"},
		{"addCompiled of another name", "func compiledFiles(r readFiles, p *pkg) {\n\tfor _, file := range p.GoFiles {\n\t\tr.addCompiled(\"d\", file + \"x\")\n\t}\n}"},
		{"addCompiled with the file as its directory", "func compiledFiles(r readFiles, p *pkg) {\n\tfor _, file := range p.GoFiles {\n\t\tr.addCompiled(file, \"x.go\")\n\t}\n}"},
		{"addCompiled of one argument", "func compiledFiles(r readFiles, p *pkg) {\n\tfor _, file := range p.GoFiles {\n\t\tr.addCompiled(file)\n\t}\n}"},
		{"a write in holds", "func (f readFiles) holds(id, name string) bool { f.compiledByDir[id] = nil; return false }"},
		{"a package variable's function literal", "var w = func(r readFiles) { r.compiledByDir[\"d\"] = nil }"},
	} {
		if sites := readSetSites(t, map[string]string{"p.go": head + tc.body + "\n"}); len(sites) == 0 {
			t.Errorf("readSetSites(%s) = none, want it refused", tc.name)
		}
	}
	own := head + "func newReadFiles() readFiles { return readFiles{compiledByDir: make(map[string]map[string]bool)} }\n\n" +
		"func compiledFiles(r readFiles, p *pkg) {\n\tfor _, file := range p.GoFiles {\n\t\tr.addCompiled(\"d\", file)\n\t}\n}\n\n" +
		"func (f readFiles) addCompiled(id, path string) { f.compiledByDir[id] = nil }\n\n" +
		"func (f readFiles) merge(o readFiles) {\n\tfor id, names := range o.compiledByDir {\n\t\tf.compiledByDir[id] = names\n\t}\n}\n\n" +
		"func (f readFiles) holds(id, name string) bool { return f.compiledByDir[id][name] }\n\n" +
		"func cleared() readFiles { return readFiles{} }\n"
	if sites := readSetSites(t, map[string]string{"p.go": own}); len(sites) > 0 {
		t.Errorf("readSetSites(the package's own forms) = %v, want none", sites)
	}
}

// readSetSites is each site of sources, file name to content, that may put into
// the read set a file no compiled list holds: the field compiledByDir named
// outside addCompiled, merge and holds, a write or a call in holds, a filled
// readFiles literal outside newReadFiles, and addCompiled named other than as a
// call in compiledFiles whose file argument is the value of a range over GoFiles.
func readSetSites(t *testing.T, sources map[string]string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var sites []string
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		file, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			sites = append(sites, readSetSitesOf(fset, decl, ownerOf(decl))...)
		}
	}
	return sites
}

// ownerOf is the name of the function decl declares, Type.Name for a method,
// and empty for any other declaration.
func ownerOf(decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	switch {
	case !ok:
		return ""
	case fn.Recv == nil || len(fn.Recv.List) == 0:
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// readSetSitesOf is [readSetSites] of one declaration of the function owner.
func readSetSitesOf(fset *token.FileSet, decl ast.Decl, owner string) []string {
	var sites []string
	var stack []ast.Node
	ast.Inspect(decl, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		refuse := func(why string) { sites = append(sites, fset.Position(n.Pos()).String()+": "+why) }
		switch n := n.(type) {
		case *ast.SelectorExpr:
			switch n.Sel.Name {
			case "compiledByDir":
				if owner != "readFiles.addCompiled" && owner != "readFiles.merge" && owner != "readFiles.holds" {
					refuse("the read set's field in " + owner)
				}
			case "addCompiled":
				if call, ok := stack[len(stack)-1].(*ast.CallExpr); owner != "compiledFiles" || !ok || call.Fun != n || !rangedOverGoFiles(stack, call) {
					refuse("addCompiled other than on a file of GoFiles in compiledFiles")
				}
			}
		case *ast.AssignStmt, *ast.IncDecStmt, *ast.CallExpr:
			if owner == "readFiles.holds" {
				refuse("a write or a call in holds")
			}
		case *ast.CompositeLit:
			if id, ok := n.Type.(*ast.Ident); ok && id.Name == "readFiles" && len(n.Elts) > 0 && owner != "newReadFiles" {
				refuse("a filled read set in " + owner)
			}
		}
		stack = append(stack, n)
		return true
	})
	return sites
}

// rangedOverGoFiles reports whether call's second argument, of two, the file it
// adds, is the value of a range statement of stack over a GoFiles field.
func rangedOverGoFiles(stack []ast.Node, call *ast.CallExpr) bool {
	if len(call.Args) != 2 {
		return false
	}
	arg, ok := call.Args[1].(*ast.Ident)
	if !ok {
		return false
	}
	return slices.ContainsFunc(stack, func(n ast.Node) bool {
		loop, ok := n.(*ast.RangeStmt)
		if !ok {
			return false
		}
		value, isIdent := loop.Value.(*ast.Ident)
		list, isField := loop.X.(*ast.SelectorExpr)
		return isIdent && value.Name == arg.Name && isField && list.Sel.Name == "GoFiles"
	})
}

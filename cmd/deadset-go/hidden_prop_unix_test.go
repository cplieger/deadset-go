//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/token"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/report"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// hiddenDraw is one tree of the hidden-entry property: an application whose main
// package calls along a chain of one to three packages, each in the module itself,
// a module a replace directive names, a workspace module, a directory the module
// ignores, a testdata or an underscore directory, or store/objects beside the HEAD
// file and the refs directory of git's layout, with a vendor tree, a nested module
// building against the target, or both, and the directory a pipe goes in.
type hiddenDraw struct {
	files map[string]string
	pipe  string // the directory, relative to the tree, a pipe named zz.go goes in
	vend  bool
}

// drawHidden is the tree seed draws.
func drawHidden(seed uint64) hiddenDraw {
	r := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
	workspace := r.IntN(2) == 0
	stops := []string{"p", "b/q", "data/d", "testdata/t", "_ex/e", "store/objects"}
	if workspace {
		stops = append(stops, "w/t")
	}
	r.Shuffle(len(stops), func(i, j int) { stops[i], stops[j] = stops[j], stops[i] })
	chain := stops[:1+r.IntN(3)]
	files := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27.1\n\nignore ./data\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ./b\n",
		"b/go.mod":         "module example.com/b\n\ngo 1.27.1\n",
		"b/b.go":           "package b\n",
		repositoryDocument: `{"target": {"kind": "application"}}`,
	}
	if slices.Contains(chain, "store/objects") {
		files["store/HEAD"] = "ref: refs/heads/main\n"
		files["store/refs/heads/main"] = ""
	}
	if workspace {
		files["go.work"] = "go 1.27.1\n\nuse (\n\t.\n\t./w\n)\n"
		files["w/go.mod"] = "module example.com/w\n\ngo 1.27.1\n"
		files["w/w.go"] = "package w\n"
	}
	importOf := func(dir string) string {
		switch {
		case strings.HasPrefix(dir, "b/"):
			return "example.com/" + dir
		case strings.HasPrefix(dir, "w/"):
			return "example.com/" + dir
		}
		return "example.com/app/" + dir
	}
	files["main.go"] = fmt.Sprintf("package main\n\nimport %q\n\nfunc main() { %s.Used() }\n\nfunc unused() {}\n", importOf(chain[0]), path.Base(chain[0]))
	for i, dir := range chain {
		name := path.Base(dir)
		body := "package " + name + "\n\n"
		call := ""
		if i+1 < len(chain) {
			body += fmt.Sprintf("import %q\n\n", importOf(chain[i+1]))
			call = path.Base(chain[i+1]) + ".Used()"
		}
		body += "// Used is what the chain calls.\nfunc Used() { " + call + " }\n\n// Unused is what nothing calls.\nfunc Unused() {}\n\nfunc helper() {}\n"
		files[dir+"/"+name+".go"] = body
	}
	if r.IntN(2) == 0 {
		files["sub/go.mod"] = "module example.com/sub\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n"
		files["sub/main.go"] = fmt.Sprintf("package main\n\nimport %q\n\nfunc main() { %s.Unused() }\n", importOf(chain[0]), path.Base(chain[0]))
	}
	var dirs []string
	for name := range files {
		if dir := path.Dir(name); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	vend := !workspace && r.IntN(2) == 0
	if vend {
		dirs = append(dirs, "vendor/example.com/b")
	}
	slices.Sort(dirs)
	pipe := dirs[r.IntN(len(dirs))]
	// A uniform draw over a dozen seeds seldom lands in the lookalike store.
	if slices.Contains(chain, "store/objects") && r.IntN(2) == 0 {
		pipe = "store/objects"
	}
	return hiddenDraw{files: files, pipe: pipe, vend: vend}
}

// analyzedWith is the findings and the unread-entry notes of an analysis of the
// draw's tree at the confidence floor possible, a pipe in it where piped.
func (d hiddenDraw) analyzedWith(t *testing.T, piped bool) (findings map[string]vocab.Class, exported map[string]bool, notes []string) {
	t.Helper()
	dir := writeModule(t, d.files)
	if d.vend {
		vendor := exec.CommandContext(t.Context(), "go", "mod", "vendor")
		vendor.Dir = dir
		vendor.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
		if out, err := vendor.CombinedOutput(); err != nil {
			t.Fatalf("Setup: go mod vendor: %v\n%s", err, out)
		}
	}
	if piped {
		at := filepath.Join(dir, filepath.FromSlash(d.pipe))
		if err := sandbox.MkdirAll(at, 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		sandbox.Fifo(t, filepath.Join(at, "zz.go"))
	}
	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	got, stalled := invokedWithin(t, 2*time.Minute, dir, "analyze", "--target=.", "--report="+reportPath, "--min-confidence=possible")
	if stalled {
		t.Fatalf("analyze over %v, piped %t, did not return within two minutes", d, piped)
	}
	if got.code != contractExitCodes(t)["findings"] && got.code != contractExitCodes(t)["clean"] {
		t.Fatalf("analyze over %v, piped %t = %d, want findings or clean\nstderr: %s", d, piped, got.code, got.stderr)
	}
	envelope := envelopeAt(t, reportPath)
	findings, exported = make(map[string]vocab.Class), make(map[string]bool)
	for i := range envelope.Findings {
		f := &envelope.Findings[i]
		key := string(f.Code) + " " + f.Position.Path + " " + f.Symbol.Name
		findings[key] = f.Confidence
		exported[key] = token.IsExported(f.Symbol.Name[strings.LastIndex(f.Symbol.Name, ".")+1:])
	}
	for _, note := range envelope.Notes {
		if note.Kind == report.NoteKindUnreadEntry {
			notes = append(notes, note.Path)
		}
	}
	return findings, exported, notes
}

// TestAnalyzeReportsAroundAHiddenEntryAsWithoutIt draws trees with a pipe named
// as a Go file in one of their directories and pins, within two minutes a run,
// what hiding it does ([program.6]): the run never fails; it reports the same
// findings as over the tree without the pipe; where a note names the pipe, a
// finding about a declaration of its directory's package may be possible instead,
// and one about any other exported declaration of the target too; and where none
// does, every class is the same.
func TestAnalyzeReportsAroundAHiddenEntryAsWithoutIt(t *testing.T) {
	t.Parallel()
	for seed := range uint64(12) {
		d := drawHidden(seed)
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			removed, _, _ := d.analyzedWith(t, false)
			got, exported, notes := d.analyzedWith(t, true)
			noted := slices.Contains(notes, path.Join(d.pipe, "zz.go"))
			if !slices.Equal(slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(removed))) {
				t.Fatalf("analyze over %v reported %v, want the findings without the pipe, %v", d, got, removed)
			}
			for key, class := range got {
				was := removed[key]
				inside := path.Dir(strings.SplitN(key, " ", 3)[1]) == d.pipe
				lowered := noted && class == vocab.Possible && (inside || exported[key])
				if class != was && !lowered {
					t.Errorf("analyze over %v, the pipe noted %t, reported %s at %s, want %s as without the pipe", d, noted, key, class, was)
				}
			}
		})
	}
}

// linkDraw is one tree of the link property, with no special file: below m, an
// application whose main package imports, through drawn links or not, packages of
// its module and of out beside it, through a link to an ancestor, a link cycle, a
// graph of links, a link out of the module, to / or into /proc, or a missing one.
// A workspace member or a replace target may be listed through a link inside m
// (plain then spells the tree with no such link, and unlinked names those links),
// and a consumer above m may be declared through a link longer than m's directory.
type linkDraw struct {
	files    map[string]string
	links    map[string]string // below the tree, to their targets
	plain    map[string]string
	calls    map[string]string // import path to the call main makes through it
	imports  []string          // below example.com/m
	unlinked []string
	ancestor bool // a consumer at the tree's directory, m's parent
}

// drawLinks is the tree seed draws.
func drawLinks(seed uint64) linkDraw {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	d := linkDraw{
		files: map[string]string{
			"m/go.mod":                "module example.com/m\n\ngo 1.22\n",
			"m/" + repositoryDocument: `{"target": {"kind": "application"}}`,
			"m/a/d/d.go":              "package d\n\n// F is what nothing calls.\nfunc F() {}\n",
			"m/c/c.go":                "package c\n\n// Label spells what e declares.\nvar Label = \"E\"\n",
			"m/e/e.go":                "package e\n\n// E is what nothing calls.\nfunc E() {}\n",
			"out/k/k.go":              "package k\n\n// K is what nothing calls.\nfunc K() {}\n",
		},
		links: make(map[string]string),
		plain: make(map[string]string),
		calls: make(map[string]string),
	}
	through := func(path string) {
		if r.IntN(2) == 0 {
			d.imports = append(d.imports, path)
		}
	}
	if r.IntN(2) == 0 {
		d.links["m/a/b/up"] = ".."
		through("a/b/up/d")
	}
	if r.IntN(2) == 0 {
		d.links["m/c/x"], d.links["m/e/y"] = "../e", "../c"
		through("c/x/y/x")
	}
	if r.IntN(2) == 0 {
		depth := 1 + r.IntN(6)
		via := "dag/L0"
		for i := range depth {
			for j := range 2 {
				d.links[fmt.Sprintf("m/dag/L%d/s%d", i, j)] = fmt.Sprintf("../L%d", i+1)
			}
			via += fmt.Sprintf("/s%d", r.IntN(2))
		}
		d.files[fmt.Sprintf("m/dag/L%d/z.go", depth)] = "package z\n"
		through(via)
	}
	// The link out of the module lies below a, which a/b/up leads to.
	switch r.IntN(4) {
	case 1:
		d.links["m/a/c/o"] = "../../../out"
		through("a/c/o/k")
	case 2:
		d.links["m/a/c/o"] = "/"
	case 3:
		d.links["m/a/c/o"] = "/proc"
	}
	if links := slices.Sorted(maps.Keys(d.links)); len(links) > 0 && r.IntN(4) == 0 {
		d.imports = append(d.imports, strings.TrimPrefix(links[r.IntN(len(links))], "m/")+"/nope")
	}
	if r.IntN(2) == 0 {
		d.files["m/go.work"] = "go 1.22\n\nuse (\n\t.\n\t./l/u\n)\n"
		d.plain["m/go.work"] = "go 1.22\n\nuse (\n\t.\n\t./w/u\n)\n"
		d.files["m/w/u/go.mod"] = "module example.com/u\n\ngo 1.22\n"
		d.files["m/w/u/u.go"] = runAndAdd
		d.links["m/l"], d.calls["example.com/u"] = "w", "u.Run()"
		d.unlinked = append(d.unlinked, "m/l")
	}
	if r.IntN(2) == 0 {
		module := d.files["m/go.mod"] + "\nrequire example.com/v v0.0.0\n\n"
		d.files["m/go.mod"] = module + "replace example.com/v => ./k/v\n"
		d.plain["m/go.mod"] = module + "replace example.com/v => ./y/v\n"
		d.files["m/y/v/go.mod"] = "module example.com/v\n\ngo 1.22\n"
		d.files["m/y/v/v.go"] = "// Package v is a replace target.\npackage v\n\n// V is what main calls.\nfunc V() { println(half(4, 0)) }\n\nfunc half(n, unused int) int { return n / 2 }\n"
		d.links["m/k"], d.calls["example.com/v"] = "y", "v.V()"
		d.unlinked = append(d.unlinked, "m/k")
	}
	if r.IntN(2) == 0 {
		d.files["go.mod"] = "module example.com/top\n\ngo 1.22\n\nrequire example.com/m v0.0.0\n\nreplace example.com/m => ./m\n"
		d.files["top.go"] = "package main\n\nimport _ \"example.com/m/c\"\n\nfunc main() {}\n"
		d.ancestor = true
	}
	d.files["m/main.go"] = mainCalling(d.imports, d.calls)
	return d
}

// ancestorScope is the scope document a draw with a consumer above m writes beside m.
const ancestorScope = "ancestor-scope.json"

// invocation is where analyze runs over the draw's tree at dir and with what: in m
// on m, and where a consumer above m is declared, in its directory, which holds
// both, through the scope document.
func (d linkDraw) invocation(dir string) (from string, args []string) {
	if !d.ancestor {
		return filepath.Join(dir, "m"), []string{"--target=."}
	}
	return dir, []string{"--scope=" + ancestorScope, "--config=m/" + repositoryDocument}
}

// written is the directory of the draw's tree, through every drawn link where
// linked, and otherwise spelled as plain spells it, with no link plain leaves out.
// A consumer above m is declared through a link where linked, else by its directory.
func (d linkDraw) written(t *testing.T, linked bool) string {
	t.Helper()
	files := maps.Clone(d.files)
	if !linked {
		maps.Copy(files, d.plain)
	}
	dir := writeModule(t, files)
	for _, link := range slices.Sorted(maps.Keys(d.links)) {
		if !linked && slices.Contains(d.unlinked, link) {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(link))
		if err := sandbox.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		symlinkTo(d.links[link])(t, path)
	}
	if d.ancestor {
		consumer := dir
		if linked {
			consumer = filepath.Join(sandbox.Dir(t), strings.Repeat("l", 96))
			symlink(t, dir, consumer)
		}
		writeDocument(t, dir, ancestorScope, `{"target": {"id": "example.com/m", "role": "target", "path": "m"},`+
			` "consumers": [{"id": "example.com/top", "role": "consumer", "path": "`+consumer+`"}]}`)
	}
	return dir
}

// mainCalling is the main package of the link property: a blank import of each
// package of paths below example.com/m, and an import of each package calls names
// with the call main makes through it.
func mainCalling(paths []string, calls map[string]string) string {
	if len(calls) == 0 {
		return "package main\n" + blankImports(paths) + "\nfunc main() {}\n\nfunc unused() {}\n"
	}
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "\t_ %q\n", "example.com/m/"+p)
	}
	called := slices.Sorted(maps.Keys(calls))
	for _, p := range called {
		fmt.Fprintf(&b, "\t%q\n", p)
	}
	b.WriteString(")\n\nfunc main() {\n")
	for _, p := range called {
		b.WriteString("\t" + calls[p] + "\n")
	}
	b.WriteString("}\n\nfunc unused() {}\n")
	return b.String()
}

// blankImports is a Go import declaration of each package of paths below
// example.com/m, blank.
func blankImports(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nimport (\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "\t_ %q\n", "example.com/m/"+p)
	}
	b.WriteString(")\n")
	return b.String()
}

// analyzedBy is the exit code, the standard error, the findings exactly as the
// report writes them, and the unread-entry notes of an analysis of the draw's
// module m below dir by binary ([linkDraw.invocation]).
func (d linkDraw) analyzedBy(t *testing.T, binary, dir string) (code int, stderr, findings string, notes []string) {
	t.Helper()
	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	from, args := d.invocation(dir)
	got, stalled := invokedBinaryWithin(t, binary, 2*time.Minute, from, append([]string{"analyze", "--report=" + reportPath}, args...)...)
	if stalled {
		t.Fatalf("%s analyze did not return within two minutes", binary)
	}
	text, err := os.ReadFile(reportPath)
	if err != nil {
		return got.code, got.stderr, "", nil
	}
	var written struct {
		Findings json.RawMessage `json:"findings"`
		Notes    []report.Note   `json:"notes"`
	}
	if err := json.Unmarshal(text, &written); err != nil {
		t.Fatalf("Setup: read the report of %s: %v", binary, err)
	}
	for _, note := range written.Notes {
		if note.Kind == report.NoteKindUnreadEntry {
			notes = append(notes, note.Path)
		}
	}
	var compact bytes.Buffer
	if len(written.Findings) > 0 {
		if err := json.Compact(&compact, written.Findings); err != nil {
			t.Fatalf("Setup: read the findings of %s: %v", binary, err)
		}
	}
	return got.code, got.stderr, compact.String(), notes
}

// referenceBinary names the deadset-go binary the link property compares against,
// one built from v2.0.0-dev.50, the last release that hides no entry.
const referenceBinary = "DEADSET_GO_REFERENCE"

// TestAnalyzeReportsATreeOfLinksWithNoSpecialFileAsTheReference draws trees of
// links in and out of the module ([linkDraw]) with no special file, and pins,
// within two minutes a run, that the scan changes nothing there ([program.6]): no
// note names a hidden entry; a member, a replace target or a consumer above the
// module spelled through a link reports as the tree spelling it with no link; and
// where [referenceBinary] names a binary of v2.0.0-dev.50, the run over the tree
// with no such link ends with its exit code, standard error and findings.
func TestAnalyzeReportsATreeOfLinksWithNoSpecialFileAsTheReference(t *testing.T) {
	t.Parallel()
	reference := os.Getenv(referenceBinary)
	for seed := range uint64(32) {
		d := drawLinks(seed)
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			dir := d.written(t, true)
			code, stderr, findings, notes := d.analyzedBy(t, commandBinary(t), dir)
			if len(notes) > 0 {
				t.Errorf("analyze over %v wrote unread-entry notes for %q, want none: the tree holds no special file", d, notes)
			}
			plain := dir
			if len(d.unlinked) > 0 || d.ancestor {
				plain = d.written(t, false)
				plainCode, plainStderr, plainFindings, _ := d.analyzedBy(t, commandBinary(t), plain)
				if code != plainCode || stderr != plainStderr || findings != plainFindings {
					t.Errorf("analyze over %v = %d writing %q and the findings %s, want what it reports with no link to a member or a replace target, %d writing %q and %s",
						d, code, stderr, findings, plainCode, plainStderr, plainFindings)
				}
			}
			if reference == "" {
				return
			}
			wantCode, wantStderr, wantFindings, _ := d.analyzedBy(t, reference, plain)
			if code != wantCode || stderr != wantStderr || findings != wantFindings {
				t.Errorf("analyze over %v = %d writing %q and the findings %s, want the reference's %d writing %q and %s",
					d, code, stderr, findings, wantCode, wantStderr, wantFindings)
			}
		})
	}
	if reference == "" {
		t.Logf("%s names no binary, so the draws are compared with no reference", referenceBinary)
	}
}

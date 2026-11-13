package scope

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	spec "github.com/cplieger/deadset-spec/v8"
	"github.com/cplieger/deadset-spec/v8/document"
	"github.com/cplieger/deadset-spec/v8/vocab"
	"github.com/cplieger/go/filesafe"
)

// writeDocument writes body as a scope document in a fresh directory and returns
// both paths, so a test can assert how a relative path inside the document
// resolves.
func writeDocument(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = sandbox.Dir(t)
	path = filepath.Join(dir, "scope.json")
	if err := sandbox.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return dir, path
}

// refusedAt matches the refusal of a document the scope schema refuses at one
// place, named as a dotted path.
func refusedAt(place string) func(error) bool {
	return func(err error) bool {
		return errors.Is(err, errRefused) && strings.Contains(err.Error(), ": "+place+": ")
	}
}

// notStrictJSON matches the refusal of a document that is not one strict JSON value.
func notStrictJSON(err error) bool {
	return errors.Is(err, errRefused) && errors.Is(err, document.ErrMalformed)
}

func TestReadResolvesTheTargetAndItsConsumers(t *testing.T) {
	dir, path := writeDocument(t, `{
  "target": { "id": "example.com/app", "role": "target", "path": "app" },
  "consumers": [ { "role": "consumer", "path": "../consumer" } ]
}`)

	got, err := read(t.Context(), path)
	if err != nil {
		t.Fatalf("read(%s) = _, %v, want no error", path, err)
	}

	want := Document{
		Target: Module{ID: "example.com/app", Path: filepath.Join(dir, "app")},
		Consumers: []Module{
			{Path: filepath.Join(filepath.Dir(dir), "consumer")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read(%s) = %+v, want %+v", path, got, want)
	}
}

func TestReadResolvesTheWorkspaceTheDocumentNames(t *testing.T) {
	cases := map[string]struct {
		declared string
		want     func(dir string) string
	}{
		"a relative path, against the document's own directory": {
			declared: "go.work",
			want:     func(dir string) string { return filepath.Join(dir, "go.work") },
		},
		"a relative path above the document": {
			declared: "../go.work",
			want:     func(dir string) string { return filepath.Join(filepath.Dir(dir), "go.work") },
		},
		"a rooted path, on the volume of the document": {
			declared: "/src/go.work",
			want:     func(dir string) string { return filepath.VolumeName(dir) + filepath.FromSlash("/src/go.work") },
		},
		"no workspace at all": {
			declared: "",
			want:     func(string) string { return "" },
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"target": {"path": "app"}`
			if test.declared != "" {
				body += `, "workspace": ` + strconv.Quote(test.declared)
			}
			body += `}`
			dir, path := writeDocument(t, body)

			got, err := read(t.Context(), path)
			if err != nil {
				t.Fatalf("read(%s) with body %s = _, %v, want no error", path, body, err)
			}
			if want := test.want(dir); got.Workspace != want {
				t.Errorf("read(%s) with body %s .Workspace = %q, want %q", path, body, got.Workspace, want)
			}
		})
	}
}

func TestReadKeepsAnAbsolutePathAsWritten(t *testing.T) {
	absolute := filepath.Join(sandbox.Dir(t), "elsewhere")
	_, path := writeDocument(t, `{"target": {"path": `+strconv.Quote(absolute)+`}}`)

	got, err := read(t.Context(), path)
	if err != nil {
		t.Fatalf("read(%s) = _, %v, want no error", path, err)
	}
	if got.Target.Path != absolute {
		t.Errorf("read(%s).Target.Path = %q, want %q", path, got.Target.Path, absolute)
	}
	if got.Consumers != nil {
		t.Errorf("read(%s).Consumers = %v, want nil", path, got.Consumers)
	}
}

func TestReadAcceptsADocumentThatLeavesEveryRoleOut(t *testing.T) {
	dir, path := writeDocument(t, `{
  "target": { "path": "app" },
  "consumers": [ { "path": "consumer" } ]
}`)

	got, err := read(t.Context(), path)
	if err != nil {
		t.Fatalf("read(%s) = _, %v, want no error", path, err)
	}
	// A module's role is where it sits in the document, so a document that states
	// none is a document that states nothing the reader has to reconcile.
	if got.Target.Path != filepath.Join(dir, "app") {
		t.Errorf("Target.Path = %q, want %q", got.Target.Path, filepath.Join(dir, "app"))
	}
	if len(got.Consumers) != 1 {
		t.Fatalf("Consumers = %+v, want one consumer", got.Consumers)
	}
	if want := filepath.Join(dir, "consumer"); got.Consumers[0].Path != want {
		t.Errorf("Consumers[0].Path = %q, want %q", got.Consumers[0].Path, want)
	}
}

func TestReadRefusals(t *testing.T) {
	cases := map[string]struct {
		want func(error) bool
		body string
	}{
		"undeclared document key":                {body: `{"target": {"path": "app"}, "roots": ["x"]}`, want: refusedAt("roots")},
		"undeclared module key":                  {body: `{"target": {"path": "app", "kind": "library"}}`, want: refusedAt("target.kind")},
		"a declared key spelled in another case": {body: `{"target": {"path": "app"}, "Consumers": [{"path": "c"}]}`, want: refusedAt("Consumers")},
		"a module key spelled in another case":   {body: `{"target": {"Path": "app"}}`, want: refusedAt("target.Path")},
		"a key written twice":                    {body: `{"target": {"path": "app", "path": "other"}}`, want: notStrictJSON},
		"a null id":                              {body: `{"target": {"id": null, "path": "app"}}`, want: refusedAt("target.id")},
		"a null consumer":                        {body: `{"target": {"path": "app"}, "consumers": [null]}`, want: refusedAt("consumers[0]")},
		"a null consumer list":                   {body: `{"target": {"path": "app"}, "consumers": null}`, want: refusedAt("consumers")},
		"an empty target id":                     {body: `{"target": {"id": "", "path": "app"}}`, want: refusedAt("target.id")},
		"an empty consumer id":                   {body: `{"target": {"path": "app"}, "consumers": [{"id": "", "path": "c"}]}`, want: refusedAt("consumers[0].id")},
		"an empty workspace":                     {body: `{"target": {"path": "app"}, "workspace": ""}`, want: refusedAt("workspace")},
		"no target":                              {body: `{"consumers": [{"path": "consumer"}]}`, want: refusedAt("target")},
		"empty target path":                      {body: `{"target": {"path": ""}}`, want: refusedAt("target.path")},
		"target declaring the consumer role":     {body: `{"target": {"role": "consumer", "path": "app"}}`, want: refusedAt("target.role")},
		"consumer declaring the target role":     {body: `{"target": {"path": "app"}, "consumers": [{"role": "target", "path": "c"}]}`, want: refusedAt("consumers[0].role")},
		"unknown role":                           {body: `{"target": {"role": "producer", "path": "app"}}`, want: refusedAt("target.role")},
		"consumer with an empty path":            {body: `{"target": {"path": "app"}, "consumers": [{"path": ""}]}`, want: refusedAt("consumers[0].path")},
		"trailing content":                       {body: `{"target": {"path": "app"}} {"target": {"path": "other"}}`, want: notStrictJSON},
		"a bracket after the document":           {body: `{"target": {"path": "app"}}]`, want: notStrictJSON},
		"a brace after the document":             {body: `{"target": {"path": "app"}}}`, want: notStrictJSON},
		"an array rather than an object":         {body: `["app"]`, want: refusedAt("the document")},
		"malformed JSON":                         {body: `{"target": {"path": "app",}}`, want: notStrictJSON},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, path := writeDocument(t, test.body)
			got, err := read(t.Context(), path)
			if !test.want(err) {
				t.Errorf("read(%s) with body %s = _, %v, want the refusal %s names", path, test.body, err, name)
			}
			if !reflect.DeepEqual(got, Document{}) {
				t.Errorf("read(%s) = %+v, want the zero Document", path, got)
			}
		})
	}
}

func TestReadRefusesAnAbsentDocument(t *testing.T) {
	path := filepath.Join(sandbox.Dir(t), "missing.json")

	_, err := read(t.Context(), path)

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read(%s) = _, %v, want an error matching fs.ErrNotExist", path, err)
	}
	if err != nil && !strings.Contains(err.Error(), path) {
		t.Errorf("read(%s) error = %q, want it to name the path", path, err)
	}
}

func TestReadRefusesADocumentAboveTheSizeBound(t *testing.T) {
	// Padding inside a declared string keeps the document valid JSON, so the
	// size bound is the only thing that can refuse it.
	padding := strings.Repeat("p", vocab.DocumentMaxBytes)
	_, path := writeDocument(t, `{"target": {"id": "`+padding+`", "path": "app"}}`)

	_, err := read(t.Context(), path)

	if !errors.Is(err, filesafe.ErrFileTooLarge) {
		t.Errorf("read(oversize document) = _, %v, want an error matching filesafe.ErrFileTooLarge", err)
	}
}

func TestReadAcceptsADocumentAtTheSizeBound(t *testing.T) {
	prefix := `{"target": {"id": "`
	suffix := `", "path": "app"}}`
	padding := strings.Repeat("p", vocab.DocumentMaxBytes-len(prefix)-len(suffix))
	_, path := writeDocument(t, prefix+padding+suffix)

	got, err := read(t.Context(), path)
	if err != nil {
		t.Fatalf("read(document of exactly %d bytes) = _, %v, want no error", vocab.DocumentMaxBytes, err)
	}
	if got.Target.ID != padding {
		t.Errorf("Target.ID has %d characters, want %d", len(got.Target.ID), len(padding))
	}
}

func TestForDirIsTheTargetAndNothingElse(t *testing.T) {
	dir := sandbox.Dir(t)

	got, err := ForDir(dir)
	if err != nil {
		t.Fatalf("ForDir(%s) = _, %v, want no error", dir, err)
	}

	want := Document{Target: Module{Path: dir}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ForDir(%s) = %+v, want %+v", dir, got, want)
	}
}

func TestForDirResolvesARelativeDirectory(t *testing.T) {
	t.Chdir(sandbox.Dir(t))

	got, err := ForDir(".")
	if err != nil {
		t.Fatalf("ForDir(.) = _, %v, want no error", err)
	}
	if !filepath.IsAbs(got.Target.Path) {
		t.Errorf("ForDir(.).Target.Path = %q, want an absolute path", got.Target.Path)
	}
}

func TestForDirRefusals(t *testing.T) {
	file := filepath.Join(sandbox.Dir(t), "go.mod")
	if err := sandbox.WriteFile(file, []byte("module example.com/app\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}

	cases := map[string]struct {
		dir     string
		wantErr error
	}{
		"absent directory": {dir: filepath.Join(sandbox.Dir(t), "missing"), wantErr: fs.ErrNotExist},
		"a file":           {dir: file, wantErr: errNotDirectory},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ForDir(test.dir)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("ForDir(%s) = _, %v, want an error matching %v", test.dir, err, test.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), test.dir) {
				t.Errorf("ForDir(%s) error = %q, want it to name the path", test.dir, err)
			}
			if !reflect.DeepEqual(got, Document{}) {
				t.Errorf("ForDir(%s) = %+v, want the zero Document", test.dir, got)
			}
		})
	}
}

// publishedScope writes one published document as a scope document in a fresh
// directory, so read reads it through the production path.
func publishedScope(t *testing.T, name string) (dir, path string) {
	t.Helper()
	body, err := spec.Examples.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return writeDocument(t, string(body))
}

func TestReadAcceptsEveryPublishedScopeDocument(t *testing.T) {
	entries, err := fs.ReadDir(spec.Examples, "examples/scope")
	if err != nil {
		t.Fatalf("read examples/scope: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("examples/scope publishes no document, so this test pins nothing")
	}
	for _, entry := range entries {
		t.Run(strings.TrimSuffix(entry.Name(), ".json"), func(t *testing.T) {
			_, path := publishedScope(t, "examples/scope/"+entry.Name())
			if _, err := read(t.Context(), path); err != nil {
				t.Errorf("read(examples/scope/%s) = _, %v, want no error", entry.Name(), err)
			}
		})
	}
}

func TestReadResolvesThePublishedDocumentNamingEveryMember(t *testing.T) {
	dir, path := publishedScope(t, "examples/scope/target-and-consumers.json")

	got, err := read(t.Context(), path)
	if err != nil {
		t.Fatalf("read(target-and-consumers.json) = _, %v, want no error", err)
	}
	want := Document{
		Workspace: filepath.Join(dir, "go.work"),
		Target:    Module{ID: "example.com/app", Path: filepath.Join(dir, "app")},
		Consumers: []Module{
			{ID: "example.com/consumer", Path: filepath.Join(dir, "consumer")},
			{Path: filepath.VolumeName(dir) + filepath.FromSlash("/src/example.com/tool")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read(target-and-consumers.json) = %+v, want %+v", got, want)
	}
}

func TestReadRefusesEveryPublishedScopeNegative(t *testing.T) {
	data, err := spec.Examples.ReadFile("examples/negatives/index.json")
	if err != nil {
		t.Fatalf("read examples/negatives/index.json: %v", err)
	}
	var index struct {
		Negatives []struct {
			File   string `json:"file"`
			Schema string `json:"schema"`
		} `json:"negatives"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode examples/negatives/index.json: %v", err)
	}
	refused := 0
	for _, row := range index.Negatives {
		if row.Schema != "contract/scope.schema.json" {
			continue
		}
		refused++
		t.Run(strings.TrimSuffix(row.File, ".json"), func(t *testing.T) {
			_, path := publishedScope(t, "examples/negatives/"+row.File)
			got, err := read(t.Context(), path)
			if err == nil {
				t.Errorf("read(examples/negatives/%s) = %+v, nil, want a refusal", row.File, got)
			}
		})
	}
	if refused == 0 {
		t.Fatal("examples/negatives/index.json names no refused scope document, so this test pins nothing")
	}
}

// linkedTree is a directory real holding app/sub and other, a link alias to real
// in another directory, and a link real/inner to real/app inside it.
func linkedTree(t *testing.T) (base, real, alias string) {
	t.Helper()
	base = sandbox.Dir(t)
	real = filepath.Join(base, "real")
	for _, dir := range []string{filepath.Join(real, "app", "sub"), filepath.Join(real, "other")} {
		if err := sandbox.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	alias = filepath.Join(sandbox.Dir(t), "alias")
	if err := sandbox.Symlink(real, alias); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Symlink(filepath.Join(real, "app"), filepath.Join(real, "inner")); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return base, real, alias
}

// A directory is placed where it resolves, a link on its path or the directory
// itself, and an entry where its directory resolves, keeping its own name.
func TestPlaceComparesAPathAndTheRootWhereBothResolve(t *testing.T) {
	base, real, alias := linkedTree(t)
	for _, tc := range []struct {
		name, root, path, want string
		entry, inside          bool
	}{
		{"the root itself", real, real, ".", false, true},
		{"the root itself as an entry", real, real, ".", true, true},
		{"the root spelled through a link, as an entry", real, alias, ".", true, true},
		{"a link from outside the root to it, as an entry", filepath.Join(real, "app"), filepath.Join(real, "inner"), ".", true, true},
		{"an entry spelled through a link outside and one inside", filepath.Join(real, "app"), filepath.Join(alias, "inner", "sub"), "sub", true, true},
		{"a directory below by its names", real, filepath.Join(real, "app", "sub"), "app/sub", false, true},
		{"a link inside the root, as a directory", base, filepath.Join(real, "inner"), "real/app", false, true},
		{"a link inside the root, as an entry", base, filepath.Join(real, "inner"), "real/inner", true, true},
		{"a directory below an in-tree link", real, filepath.Join(real, "inner", "sub"), "app/sub", false, true},
		{"an entry below an in-tree link", real, filepath.Join(real, "inner", "sub", "x.go"), "app/sub/x.go", true, true},
		{"the root spelled through a link", alias, filepath.Join(real, "app"), "app", false, true},
		{"a path spelled through a link", real, filepath.Join(alias, "app", "sub"), "app/sub", false, true},
		{"a path through a link outside and one inside", real, filepath.Join(alias, "inner", "sub"), "app/sub", false, true},
		{"a sibling", filepath.Join(real, "app"), base, "", false, false},
		{"a path that does not exist", real, filepath.Join(alias, "none", "y.go"), "none/y.go", true, true},
		{"a path that resolves nowhere", real, filepath.Join(base, "outside"), "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			place := RootOf(tc.root).Place
			if tc.entry {
				place = RootOf(tc.root).PlaceEntry
			}
			got, inside := place(tc.path)
			if got != tc.want || inside != tc.inside {
				t.Errorf("RootOf(%s) placing %s (entry %v) = %q, %v, want %q, %v", tc.root, tc.path, tc.entry, got, inside, tc.want, tc.inside)
			}
		})
	}
	if got, inside := (Root{}).Place(real); inside {
		t.Errorf("the zero Root places %s at %q, want nowhere", real, got)
	}
}

// A path is named relative to a root where both resolve: as PlaceEntry places it
// where the root holds it, and otherwise from the resolved root to where the
// path's directory resolves, with `..` segments, whichever spelling either side
// has.
func TestRootRelNamesAPathWhereBothResolve(t *testing.T) {
	base, real, alias := linkedTree(t)
	for _, tc := range []struct{ name, root, path, want string }{
		{"the root itself", filepath.Join(real, "app"), filepath.Join(real, "app"), "."},
		{"a path below by its names", real, filepath.Join(real, "app", "sub"), "app/sub"},
		{"a link inside the root keeps its name", base, filepath.Join(real, "inner"), "real/inner"},
		{"a path below an in-tree link", real, filepath.Join(real, "inner", "sub", "x.go"), "app/sub/x.go"},
		{"a path below spelled through a link", filepath.Join(real, "app"), filepath.Join(alias, "app", "sub", "x.go"), "sub/x.go"},
		{"a path outside spelled through a link", filepath.Join(real, "app"), filepath.Join(alias, "other", "x.go"), "../other/x.go"},
		{"a path outside the root spelled through a link", filepath.Join(alias, "app"), filepath.Join(real, "other", "x.go"), "../other/x.go"},
		{"a path outside below a directory that does not exist", filepath.Join(real, "app"), filepath.Join(alias, "none", "y.go"), "../none/y.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RootOf(tc.root).Rel(tc.path); got != tc.want {
				t.Errorf("RootOf(%s).Rel(%s) = %q, want %q", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

// A directory the root holds is named as the root spells it, however the path
// reaches it, through a link outside the root or one inside it; a directory the
// root does not hold, and any path of the zero Root, keeps its name.
func TestRootRespellNamesADirectoryTheRootHoldsAsTheRootSpellsIt(t *testing.T) {
	_, real, alias := linkedTree(t)
	app, linkedApp := filepath.Join(real, "app"), filepath.Join(alias, "app")
	for _, tc := range []struct {
		name       string
		root       Root
		path, want string
	}{
		{"a directory below by its names", RootOf(app), filepath.Join(app, "sub"), filepath.Join(app, "sub")},
		{"a directory below spelled through a link", RootOf(app), filepath.Join(linkedApp, "sub"), filepath.Join(app, "sub")},
		{"a directory below an in-tree link", RootOf(real), filepath.Join(real, "inner", "sub"), filepath.Join(real, "app", "sub")},
		{"the root through an in-tree link", RootOf(app), filepath.Join(real, "inner"), app},
		{"a root spelled through a link", RootOf(linkedApp), filepath.Join(app, "sub"), filepath.Join(linkedApp, "sub")},
		{"a directory outside the root", RootOf(app), filepath.Join(alias, "other"), filepath.Join(alias, "other")},
		{"the zero Root", Root{}, filepath.Join(linkedApp, "sub"), filepath.Join(linkedApp, "sub")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.root.Respell(tc.path); got != tc.want {
				t.Errorf("Respell(%s) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// Every spelling of one file, through a link outside the root or inside it, is one
// name ([Root.Name]): the root's own spelling where the root holds the file's
// resolved directory, and the resolved path elsewhere. A name is its own name.
func TestRootNameGivesEverySpellingOfAFileOneName(t *testing.T) {
	_, real, alias := linkedTree(t)
	app, linkedApp := filepath.Join(real, "app"), filepath.Join(alias, "app")
	// The temporary directory may itself be spelled through a link.
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, tc := range []struct {
		name       string
		root       Root
		path, want string
	}{
		{"a file below by its names", RootOf(app), filepath.Join(app, "sub", "x.go"), filepath.Join(app, "sub", "x.go")},
		{"a file below spelled through a link outside the root", RootOf(app), filepath.Join(linkedApp, "sub", "x.go"), filepath.Join(app, "sub", "x.go")},
		{"a file below an in-tree link", RootOf(real), filepath.Join(real, "inner", "x.go"), filepath.Join(real, "app", "x.go")},
		{"a file below an in-tree link, the root spelled through a link", RootOf(alias), filepath.Join(real, "inner", "sub", "x.go"), filepath.Join(alias, "app", "sub", "x.go")},
		{"a file of a root spelled through a link", RootOf(linkedApp), filepath.Join(app, "x.go"), filepath.Join(linkedApp, "x.go")},
		{"a file outside the root, spelled through a link", RootOf(app), filepath.Join(alias, "other", "x.go"), filepath.Join(resolved, "other", "x.go")},
		{"a file of the zero Root", Root{}, filepath.Join(linkedApp, "x.go"), filepath.Join(resolved, "app", "x.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.root.Name(tc.path)
			if got != tc.want {
				t.Errorf("Name(%s) = %q, want %q", tc.path, got, tc.want)
			}
			if again := tc.root.Name(got); again != got {
				t.Errorf("Name(%s) = %q, want the name itself", got, again)
			}
		})
	}
}

// Where names are checked, as in every test binary, a parse by a path that is not
// the file's name, or of one file under two names, panics naming the file, and a
// parse by the file's name reads it.
func TestRootParseRefusesAFileByAnyNameButItsOwn(t *testing.T) {
	_, real, alias := linkedTree(t)
	file := filepath.Join(real, "app", "x.go")
	if err := sandbox.WriteFile(file, []byte("package app\n"), 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	panics := func(root Root, path string) (message string) {
		defer func() {
			if r := recover(); r != nil {
				message = fmt.Sprint(r)
			}
		}()
		if _, err := root.Parse(token.NewFileSet(), path, nil, parser.PackageClauseOnly); err != nil {
			t.Fatalf("Parse(%s): %v", path, err)
		}
		return ""
	}
	root := RootOf(real)
	if message := panics(root, filepath.Join(real, "app", "x.go")); message != "" {
		t.Errorf("Parse by the file's name panicked: %s", message)
	}
	for _, path := range []string{filepath.Join(real, "inner", "x.go"), filepath.Join(alias, "app", "x.go")} {
		if message := panics(root, path); !strings.Contains(message, "a name the run did not give it") {
			t.Errorf("Parse(%s) panicked with %q, want the refusal of a name the run did not give", path, message)
		}
	}
	if message := panics(root, root.Name(file)); message != "" {
		t.Errorf("a second parse by the file's name panicked: %s", message)
	}
	// A name recorded otherwise, as a parse that named the file through the link
	// would have left, makes the next parse refuse.
	root.at.names[root.entryPath(file)] = filepath.Join(real, "inner", "x.go")
	if message := panics(root, file); !strings.Contains(message, "is parsed as") {
		t.Errorf("Parse(%s) after another name panicked with %q, want the refusal of a second name", file, message)
	}
}

// Two paths are one directory exactly where both resolve to one, and an entry is
// one entry where its directories do and its names agree.
func TestSameIsOneDirectoryWhereBothResolve(t *testing.T) {
	base, real, alias := linkedTree(t)
	for _, tc := range []struct {
		name, a, b string
		want       bool
	}{
		{"identical", real, real, true},
		{"a trailing separator", real + string(filepath.Separator), real, true},
		{"an unnormalized parent", filepath.Join(real, "other", "..", "app"), filepath.Join(real, "app"), true},
		{"a link outside and its directory", alias, real, true},
		{"its directory and a link outside", real, alias, true},
		{"an in-tree link and its directory", filepath.Join(real, "inner"), filepath.Join(real, "app"), true},
		{"a different directory", filepath.Join(real, "app"), filepath.Join(real, "other"), false},
		{"one below the other", filepath.Join(real, "app", "sub"), filepath.Join(real, "app"), false},
		{"a link and a sibling of its directory", alias, base, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Same(tc.a, tc.b); got != tc.want {
				t.Errorf("Same(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
	if !SameEntry(filepath.Join(alias, "app", "x.go"), filepath.Join(real, "app", "x.go")) {
		t.Errorf("SameEntry of x.go spelled through %s and in %s = false, want true", alias, real)
	}
	if SameEntry(filepath.Join(real, "inner"), filepath.Join(real, "app")) {
		t.Errorf("SameEntry(%s, %s) = true, want false: the link is an entry of its own", filepath.Join(real, "inner"), filepath.Join(real, "app"))
	}
}

// The directory a run accepts as the target's is the directory it renames the
// target's files into: where Same holds two spellings one directory, Respell gives
// both one name, through a link outside the root, a link inside it to the root, and
// a case variant on a volume that folds case. The bind-mount case follows by
// construction: Same and Respell place a path through the one resolver.
func TestSameDirectoriesAreRespelledAlike(t *testing.T) {
	base := sandbox.Dir(t)
	app := filepath.Join(base, "real", "app")
	if err := sandbox.MkdirAll(filepath.Join(app, "sub"), 0o750); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	alias := filepath.Join(sandbox.Dir(t), "alias")
	if err := sandbox.Symlink(filepath.Join(base, "real"), alias); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := sandbox.Symlink(".", filepath.Join(app, "link")); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	spellings := []string{filepath.Join(alias, "app"), filepath.Join(app, "link"), filepath.Join(alias, "app", "link", "sub", "..")}
	if folded := filepath.Join(base, "REAL", "APP"); foldsCase(base) {
		spellings = append(spellings, folded)
	} else {
		t.Logf("%s keeps the case of a name, so no case variant of %s names it", base, app)
	}
	root := RootOf(app)
	for _, a := range spellings {
		if Same(a, app) && root.Respell(a) != root.Respell(app) {
			t.Errorf("Same(%s, %s) holds, but Respell names them %q and %q", a, app, root.Respell(a), root.Respell(app))
		}
		if !Same(a, app) && root.Respell(a) == root.Respell(app) {
			t.Errorf("Same(%s, %s) fails, but Respell names both %q", a, app, root.Respell(app))
		}
	}
}

// foldsCase reports whether the volume holding dir finds an entry by a name that
// differs from it only in case.
func foldsCase(dir string) bool {
	_, err := os.Stat(strings.ToUpper(dir))
	return err == nil && strings.ToUpper(dir) != dir
}

// A slash name is below another by whole elements, and names nothing to resolve.
func TestUnderNamePlacesASlashNameByWholeElements(t *testing.T) {
	for _, tc := range []struct {
		prefix, name, rest string
		under              bool
	}{
		{"example.com/m", "example.com/m", "", true},
		{"example.com/m", "example.com/m/sub/pkg", "sub/pkg", true},
		{"example.com/m", "example.com/mod", "", false},
		{"example.com/m", "example.com", "", false},
		{"", "example.com", "", false},
		{".", "./x", "x", true},
	} {
		rest, under := UnderName(tc.prefix, tc.name)
		if under != tc.under || rest != tc.rest {
			t.Errorf("UnderName(%q, %q) = %q, %v, want %q, %v", tc.prefix, tc.name, rest, under, tc.rest, tc.under)
		}
	}
}

func TestAnInputTheInvocationNamesThatCannotBeReadIsErrNamed(t *testing.T) {
	_, refused := writeDocument(t, `{"target": {"path": "."}, "unknown": true}`)
	file := filepath.Join(sandbox.Dir(t), "file")
	if err := sandbox.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, tc := range []struct {
		name, dir, document string
	}{
		{"an absent target", filepath.Join(sandbox.Dir(t), "absent"), ""},
		{"a target that is a file", file, ""},
		{"an absent scope document", ".", filepath.Join(sandbox.Dir(t), "missing.json")},
		{"a scope document the schema refuses", ".", refused},
	} {
		if _, err := Resolve(t.Context(), tc.dir, tc.document); !errors.Is(err, ErrNamed) {
			t.Errorf("Resolve(%s) = _, %v, want an error matching ErrNamed", tc.name, err)
		}
	}
}

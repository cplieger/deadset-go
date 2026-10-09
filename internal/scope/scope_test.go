package scope

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	spec "github.com/cplieger/deadset-spec/v7"
)

// writeDocument writes body as a scope document in a fresh directory and returns
// both paths, so a test can assert how a relative path inside the document
// resolves.
func writeDocument(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "scope.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return dir, path
}

// is matches an error against a sentinel.
func is(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}

// hasText matches an error by a phrase of its message, for a refusal
// encoding/json gives no sentinel and no type.
func hasText(phrase string) func(error) bool {
	return func(err error) bool { return err != nil && strings.Contains(err.Error(), phrase) }
}

// memberRefused matches a refusal of one member, naming it.
func memberRefused(named string) func(error) bool {
	return func(err error) bool { return errors.Is(err, ErrMember) && strings.Contains(err.Error(), named) }
}

// isSyntaxError matches the decoder's refusal of malformed JSON.
func isSyntaxError(err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr)
}

// isTypeError matches the decoder's refusal of a value of the wrong JSON type.
func isTypeError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func TestReadResolvesTheTargetAndItsConsumers(t *testing.T) {
	dir, path := writeDocument(t, `{
  "target": { "id": "example.com/app", "role": "target", "path": "app" },
  "consumers": [ { "role": "consumer", "path": "../consumer" } ]
}`)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read(%s) = _, %v, want no error", path, err)
	}

	want := Document{
		Target: Module{ID: "example.com/app", Path: filepath.Join(dir, "app")},
		Consumers: []Module{
			{Path: filepath.Join(filepath.Dir(dir), "consumer")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read(%s) = %+v, want %+v", path, got, want)
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
		"an absolute path, as written": {
			declared: "/src/go.work",
			want:     func(string) string { return filepath.FromSlash("/src/go.work") },
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

			got, err := Read(path)
			if err != nil {
				t.Fatalf("Read(%s) with body %s = _, %v, want no error", path, body, err)
			}
			if want := test.want(dir); got.Workspace != want {
				t.Errorf("Read(%s) with body %s .Workspace = %q, want %q", path, body, got.Workspace, want)
			}
		})
	}
}

func TestReadKeepsAnAbsolutePathAsWritten(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "elsewhere")
	_, path := writeDocument(t, `{"target": {"path": `+strconv.Quote(absolute)+`}}`)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read(%s) = _, %v, want no error", path, err)
	}
	if got.Target.Path != absolute {
		t.Errorf("Read(%s).Target.Path = %q, want %q", path, got.Target.Path, absolute)
	}
	if got.Consumers != nil {
		t.Errorf("Read(%s).Consumers = %v, want nil", path, got.Consumers)
	}
}

func TestReadAcceptsADocumentThatLeavesEveryRoleOut(t *testing.T) {
	dir, path := writeDocument(t, `{
  "target": { "path": "app" },
  "consumers": [ { "path": "consumer" } ]
}`)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read(%s) = _, %v, want no error", path, err)
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
		body string
		want func(error) bool
		desc string
	}{
		"undeclared document key": {
			body: `{"target": {"path": "app"}, "roots": ["x"]}`,
			want: memberRefused("roots"),
			desc: "ErrMember naming roots",
		},
		"undeclared module key": {
			body: `{"target": {"path": "app", "kind": "library"}}`,
			want: memberRefused("target.kind"),
			desc: "ErrMember naming target.kind",
		},
		"a declared key spelled in another case": {
			body: `{"target": {"path": "app"}, "Consumers": [{"path": "c"}]}`,
			want: memberRefused("Consumers"),
			desc: "ErrMember naming Consumers",
		},
		"a module key spelled in another case": {
			body: `{"target": {"Path": "app"}}`,
			want: memberRefused("target.Path"),
			desc: "ErrMember naming target.Path",
		},
		"a key written twice": {
			body: `{"target": {"path": "app", "path": "other"}}`,
			want: memberRefused("target.path"),
			desc: "ErrMember naming target.path",
		},
		"a null id": {
			body: `{"target": {"id": null, "path": "app"}}`,
			want: memberRefused("target.id"),
			desc: "ErrMember naming target.id",
		},
		"a null consumer": {
			body: `{"target": {"path": "app"}, "consumers": [null]}`,
			want: memberRefused("consumers[0]"),
			desc: "ErrMember naming consumers[0]",
		},
		"a null consumer list": {
			body: `{"target": {"path": "app"}, "consumers": null}`,
			want: memberRefused("consumers"),
			desc: "ErrMember naming consumers",
		},
		"an empty target id": {
			body: `{"target": {"id": "", "path": "app"}}`,
			want: memberRefused("empty id"),
			desc: "ErrMember naming the empty id",
		},
		"an empty consumer id": {
			body: `{"target": {"path": "app"}, "consumers": [{"id": "", "path": "c"}]}`,
			want: memberRefused("empty id"),
			desc: "ErrMember naming the empty id",
		},
		"an empty workspace": {
			body: `{"target": {"path": "app"}, "workspace": ""}`,
			want: memberRefused("workspace"),
			desc: "ErrMember naming the workspace",
		},
		"no target": {
			body: `{"consumers": [{"path": "consumer"}]}`,
			want: is(ErrNoTarget),
			desc: "ErrNoTarget",
		},
		"empty target path": {
			body: `{"target": {"path": ""}}`,
			want: is(ErrNoTarget),
			desc: "ErrNoTarget",
		},
		"target declaring the consumer role": {
			body: `{"target": {"role": "consumer", "path": "app"}}`,
			want: is(ErrRole),
			desc: "ErrRole",
		},
		"consumer declaring the target role": {
			body: `{"target": {"path": "app"}, "consumers": [{"role": "target", "path": "c"}]}`,
			want: is(ErrRole),
			desc: "ErrRole",
		},
		"unknown role": {
			body: `{"target": {"role": "producer", "path": "app"}}`,
			want: is(ErrRole),
			desc: "ErrRole",
		},
		"consumer with an empty path": {
			body: `{"target": {"path": "app"}, "consumers": [{"path": ""}]}`,
			want: hasText("empty path"),
			desc: "an error naming the empty path",
		},

		"trailing content": {
			body: `{"target": {"path": "app"}} {"target": {"path": "other"}}`,
			want: is(ErrTrailingContent),
			desc: "ErrTrailingContent",
		},
		"an array rather than an object": {
			body: `["app"]`,
			want: isTypeError,
			desc: "*json.UnmarshalTypeError",
		},
		"malformed JSON": {
			body: `{"target": {"path": "app",}}`,
			want: isSyntaxError,
			desc: "*json.SyntaxError",
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, path := writeDocument(t, test.body)
			got, err := Read(path)
			if !test.want(err) {
				t.Errorf("Read(%s) with body %s = _, %v, want %s", path, test.body, err, test.desc)
			}
			if !reflect.DeepEqual(got, Document{}) {
				t.Errorf("Read(%s) = %+v, want the zero Document", path, got)
			}
		})
	}
}

func TestReadRefusesAnAbsentDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	_, err := Read(path)

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read(%s) = _, %v, want an error matching fs.ErrNotExist", path, err)
	}
	if err != nil && !strings.Contains(err.Error(), path) {
		t.Errorf("Read(%s) error = %q, want it to name the path", path, err)
	}
}

func TestReadRefusesADocumentAboveTheSizeBound(t *testing.T) {
	// Padding inside a declared string keeps the document valid JSON, so the
	// size bound is the only thing that can refuse it.
	padding := strings.Repeat("p", maxDocumentBytes)
	_, path := writeDocument(t, `{"target": {"id": "`+padding+`", "path": "app"}}`)

	_, err := Read(path)

	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Read(oversize document) = _, %v, want an error matching ErrTooLarge", err)
	}
}

func TestReadAcceptsADocumentAtTheSizeBound(t *testing.T) {
	prefix := `{"target": {"id": "`
	suffix := `", "path": "app"}}`
	padding := strings.Repeat("p", maxDocumentBytes-len(prefix)-len(suffix))
	_, path := writeDocument(t, prefix+padding+suffix)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read(document of exactly %d bytes) = _, %v, want no error", maxDocumentBytes, err)
	}
	if got.Target.ID != padding {
		t.Errorf("Target.ID has %d characters, want %d", len(got.Target.ID), len(padding))
	}
}

func TestForDirIsTheTargetAndNothingElse(t *testing.T) {
	dir := t.TempDir()

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
	t.Chdir(t.TempDir())

	got, err := ForDir(".")
	if err != nil {
		t.Fatalf("ForDir(.) = _, %v, want no error", err)
	}
	if !filepath.IsAbs(got.Target.Path) {
		t.Errorf("ForDir(.).Target.Path = %q, want an absolute path", got.Target.Path)
	}
}

func TestForDirRefusals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(file, []byte("module example.com/app\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}

	cases := map[string]struct {
		dir     string
		wantErr error
	}{
		"absent directory": {dir: filepath.Join(t.TempDir(), "missing"), wantErr: fs.ErrNotExist},
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
// directory, so Read reads it through the production path.
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
			if _, err := Read(path); err != nil {
				t.Errorf("Read(examples/scope/%s) = _, %v, want no error", entry.Name(), err)
			}
		})
	}
}

func TestReadResolvesThePublishedDocumentNamingEveryMember(t *testing.T) {
	dir, path := publishedScope(t, "examples/scope/target-and-consumers.json")

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read(target-and-consumers.json) = _, %v, want no error", err)
	}
	want := Document{
		Workspace: filepath.Join(dir, "go.work"),
		Target:    Module{ID: "example.com/app", Path: filepath.Join(dir, "app")},
		Consumers: []Module{
			{ID: "example.com/consumer", Path: filepath.Join(dir, "consumer")},
			{Path: filepath.FromSlash("/src/example.com/tool")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read(target-and-consumers.json) = %+v, want %+v", got, want)
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
			got, err := Read(path)
			if err == nil {
				t.Errorf("Read(examples/negatives/%s) = %+v, nil, want a refusal", row.File, got)
			}
		})
	}
	if refused == 0 {
		t.Fatal("examples/negatives/index.json names no refused scope document, so this test pins nothing")
	}
}

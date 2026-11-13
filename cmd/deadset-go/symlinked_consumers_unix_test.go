//go:build unix

package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
)

// linkedModule is the fixture files written below a resolved temporary directory,
// that directory, and a link to it in another one: the spelling a scope document
// or an absolute directive gives the tree the run analyzes where it resolves.
func linkedModule(t *testing.T, write func(t *testing.T) string) (dir, alias string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(write(t))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	alias = filepath.Join(sandbox.Dir(t), "alias")
	symlink(t, dir, alias)
	return dir, alias
}

// A consumer the scope document names through a link reaches the target the run
// analyzes through that link, by its relative replace, so it loads and its
// references count: the export only it uses is not reported.
func TestAnalyzeCountsAConsumerSpelledThroughALinkToTheTarget(t *testing.T) {
	t.Parallel()
	dir, alias := linkedModule(t, consumedTree)
	writeDocument(t, dir, "linked-scope.json", `{"target": {"id": "example.com/app", "role": "target", "path": "target"},`+
		` "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "`+filepath.Join(alias, "consumer")+`"}]}`)

	envelope := envelopeAt(t, analyzedFrom(t, dir, "--scope=linked-scope.json", "--config=target/"+repositoryDocument))
	if loaded := envelope.Consumers.Loaded; len(loaded) != 1 || loaded[0].Path != "consumer" {
		t.Errorf("the report names the loaded consumers %+v, want the one at %q", loaded, "consumer")
	}
	if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the consumer's reference holds Used live", got, want)
	}
}

// A module below the target that replaces the target by an absolute path through
// a link builds against the target's own tree, so its references count: the
// export only it uses is not reported.
func TestAnalyzeCountsANestedModuleReplacingTheTargetThroughALink(t *testing.T) {
	t.Parallel()
	dir, alias := linkedModule(t, func(t *testing.T) string {
		return writeModule(t, map[string]string{
			"go.mod": "module example.com/app\n\ngo 1.27.1\n",
			"lib.go": "// Package app exports one declaration a nested module references and one\n" +
				"// nothing references.\npackage app\n\n" +
				"// Used is what the nested module calls.\nfunc Used() string { return \"used\" }\n\n" +
				"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
			repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
			"tools/main.go": "// Command tools references one exported declaration of the target.\npackage main\n\n" +
				"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n",
		})
	})
	writeDocument(t, dir, filepath.Join("tools", "go.mod"), "module example.com/tools\n\ngo 1.27.1\n\n"+
		"require example.com/app v0.0.0\n\nreplace example.com/app => "+alias+"\n")

	envelope := envelopeAt(t, analyzedFrom(t, dir))
	if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the nested module's reference holds Used live", got, want)
	}
}

// A consumer below the target that the scope document names through a link to the
// target is the module the target holds there: it loads once, as the declared
// consumer, renders against its own directory, and its references count.
func TestAnalyzeCountsAConsumerBelowTheTargetSpelledThroughALink(t *testing.T) {
	t.Parallel()
	dir, alias := linkedModule(t, func(t *testing.T) string {
		return writeModule(t, map[string]string{
			"go.mod": "module example.com/app\n\ngo 1.27.1\n",
			"lib.go": "// Package app exports one declaration a consumer references and one\n" +
				"// nothing references.\npackage app\n\n" +
				"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
				"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
			repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}}`,
			"c/go.mod":         "module example.com/c\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../\n",
			"c/main.go": "// Command c references one exported declaration of the target.\npackage main\n\n" +
				"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n",
		})
	})
	writeDocument(t, dir, "linked-scope.json", `{"target": {"id": "example.com/app", "role": "target", "path": "."},`+
		` "consumers": [{"id": "example.com/c", "role": "consumer", "path": "`+filepath.Join(alias, "c")+`"}]}`)

	envelope := envelopeAt(t, analyzedFrom(t, dir, "--scope=linked-scope.json", "--config="+repositoryDocument))
	if loaded := envelope.Consumers.Loaded; len(loaded) != 1 || loaded[0].Path != "c" {
		t.Errorf("the report names the loaded consumers %+v, want the one at %q", loaded, "c")
	}
	if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the consumer's reference holds Used live", got, want)
	}
}

// libraryFiles is the target of the link rows below the module directory: a
// library exporting Used, which only a consumer calls, and Forgotten, which nothing
// calls, reporting what possible confidence admits.
func libraryFiles(prefix string) map[string]string {
	return map[string]string{
		prefix + "go.mod": "module example.com/app\n\ngo 1.27.1\n",
		prefix + "lib.go": "// Package app exports one declaration a consumer references and one\n" +
			"// nothing references.\npackage app\n\n" +
			"// Used is what the consumer calls.\nfunc Used() string { return \"used\" }\n\n" +
			"// Forgotten is what nothing calls.\nfunc Forgotten() string { return \"forgotten\" }\n",
		prefix + repositoryDocument: `{"target": {"kind": "library"}, "consumers": {"complete": true}, "analysis": {"min_confidence": "possible"}}`,
	}
}

// A module below the target that replaces the target through a link inside the
// target to the target itself builds against the target's own tree, so its
// references count: the export only it uses is not reported.
func TestAnalyzeCountsANestedModuleReplacingTheTargetThroughALinkInsideIt(t *testing.T) {
	t.Parallel()
	files := libraryFiles("")
	files["tools/go.mod"] = "module example.com/tools\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../link\n"
	files["tools/main.go"] = "// Command tools references one exported declaration of the target.\npackage main\n\n" +
		"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n"
	dir := writeModule(t, files)
	symlink(t, ".", filepath.Join(dir, "link"))

	envelope := envelopeAt(t, analyzedFrom(t, dir))
	if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the nested module's reference holds Used live", got, want)
	}
}

// A declared consumer that reaches the target through a link inside the target to
// the target itself holds the target, so it loads and its references count: the
// export only it uses is not reported.
func TestAnalyzeCountsAConsumerReachingTheTargetThroughALinkInsideIt(t *testing.T) {
	t.Parallel()
	files := libraryFiles("target/")
	files["consumer/go.mod"] = "module example.com/consumer\n\ngo 1.27.1\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ../target/link\n"
	files["consumer/main.go"] = "// Command consumer references one exported declaration of the target.\npackage main\n\n" +
		"import \"example.com/app\"\n\nfunc main() { println(app.Used()) }\n"
	files["scope.json"] = `{"target": {"id": "example.com/app", "role": "target", "path": "target"},` +
		` "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "consumer"}]}`
	dir := writeModule(t, files)
	symlink(t, ".", filepath.Join(dir, "target", "link"))

	envelope := envelopeAt(t, analyzedFrom(t, dir, "--scope=scope.json", "--config=target/"+repositoryDocument))
	if loaded := envelope.Consumers.Loaded; len(loaded) != 1 || loaded[0].Path != "consumer" {
		t.Errorf("the report names the loaded consumers %+v, want the one at %q", loaded, "consumer")
	}
	if got, want := exportedFindings(envelope), []string{"go://example.com/app#Forgotten"}; !slices.Equal(got, want) {
		t.Errorf("the report reports %v under DS1001, want %v: the consumer's reference holds Used live", got, want)
	}
}

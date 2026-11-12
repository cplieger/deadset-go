package main

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// symlink makes link a symbolic link to target, ending the test where the
// filesystem refuses one.
func symlink(t *testing.T, target, link string) {
	t.Helper()

	if err := sandbox.Symlink(target, link); err != nil {
		t.Fatalf("Setup: link %s to %s: %v", link, target, err)
	}
}

// analyzedFrom runs analyze from run over target with args added, and returns the
// report, ending the test where the run reaches no verdict.
func analyzedFrom(t *testing.T, run string, args ...string) string {
	t.Helper()

	reportPath := filepath.Join(sandbox.Dir(t), "report.json")
	ran := invokedIn(t, run, append([]string{"analyze", "--report=" + reportPath}, args...)...)
	if ran.code != vocab.ExitClean && ran.code != vocab.ExitFindings {
		t.Fatalf("analyze %q from %s = %d, want a verdict\nstderr:\n%s", args, run, ran.code, ran.stderr)
	}
	return reportPath
}

// A directory spelled through a symbolic link is the directory it resolves to:
// macOS's temporary directories sit below /var, a link to /private/var, and the
// working directory a process reads back there is the resolved spelling. Each
// case spells the run directory and the target two different ways, so only
// resolving both decides that one holds the other.
func TestAReportNamesATargetSpelledThroughASymbolicLinkWhereItResolves(t *testing.T) {
	t.Parallel()
	base := sandbox.Dir(t)
	parent, name := filepath.Join(base, "real"), "app"
	dir := filepath.Join(parent, name)
	links := filepath.Join(base, "links")
	for _, made := range []string{dir, links} {
		if err := sandbox.MkdirAll(made, 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	if err := writeFiles(dir, findingsArchive(`{"target": {"kind": "application"}}`)); err != nil {
		t.Fatalf("Setup: write the fixture module: %v", err)
	}
	parentLink, rootLink := filepath.Join(links, "parent"), filepath.Join(links, "root")
	symlink(t, parent, parentLink)
	symlink(t, dir, rootLink)

	for _, tc := range []struct {
		name, run, target, want string
	}{
		{"a run directory spelled through its parent's link", filepath.Join(parentLink, name), dir, "."},
		{"a target spelled through its parent's link", dir, filepath.Join(parentLink, name), "."},
		{"a target that is itself a link", parent, rootLink, name},
		{"a run directory below a link over a target that is a link", parentLink, rootLink, name},
		{"a run directory below a link over the resolved target", parentLink, dir, name},
		{"a link inside the run directory, kept by its name", base, rootLink, "links/root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := envelopeAt(t, analyzedFrom(t, tc.run, "--target="+tc.target))
			if envelope.Target.Root != tc.want {
				t.Errorf("the report names the target root %q, want %q", envelope.Target.Root, tc.want)
			}
		})
	}
}

// A target and a consumer the scope document names through a link to the
// directory holding both are reported where they resolve, below the run directory
// the link's target is, and the consumer's references count.
func TestAReportNamesAConsumerSpelledThroughASymbolicLinkWhereItResolves(t *testing.T) {
	t.Parallel()
	dir := archiveModule(t, "findings-ledger/layout-through-a-consumers-generic.txtar")
	alias := filepath.ToSlash(filepath.Join(sandbox.Dir(t), "alias"))
	symlink(t, dir, alias)
	writeDocument(t, dir, "linked-scope.json", `{"target": {"id": "example.com/app", "role": "target", "path": "`+alias+`/target"},`+
		` "consumers": [{"id": "example.com/consumer", "role": "consumer", "path": "`+alias+`/consumer"}]}`)

	envelope := envelopeAt(t, analyzedFrom(t, dir, "--scope=linked-scope.json", "--config=target/"+repositoryDocument))
	if envelope.Target.Root != "target" {
		t.Errorf("the report names the target root %q, want %q", envelope.Target.Root, "target")
	}
	if loaded := envelope.Consumers.Loaded; len(loaded) != 1 || loaded[0].Path != "consumer" {
		t.Errorf("the report names the loaded consumers %+v, want the one at %q", loaded, "consumer")
	}
	if len(envelope.Findings) != 0 {
		t.Errorf("findings = %d, want none: the consumer's conversion lays S out", len(envelope.Findings))
	}
}

// A configured root's document spelled through a link is sited in the target where
// the document resolves.
func TestAConfiguredRootsDocumentSpelledThroughASymbolicLinkIsSitedWhereItResolves(t *testing.T) {
	t.Parallel()
	document := filepath.Join("sub", "other.json")
	dir := writeModule(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27.1\n",
		"app.go": "package main\n\nfunc main() { used() }\n\n" +
			"// used is what the entry point calls.\nfunc used() {}\n",
		document: `{"target": {"kind": "application"}, "roots": {"patterns": ["go://example.com/app#Absent"]}}`,
	})
	alias := filepath.Join(sandbox.Dir(t), "alias")
	symlink(t, dir, alias)

	for name, spelling := range map[string]struct{ target, config string }{
		"a target spelled through the link":   {target: alias, config: filepath.Join(dir, document)},
		"a document spelled through the link": {target: dir, config: filepath.Join(alias, document)},
	} {
		t.Run(name, func(t *testing.T) {
			envelope := envelopeAt(t, analyzedFrom(t, dir, "--target="+spelling.target, "--config="+spelling.config))
			found := reportedUnder(t, envelope.Findings, vocab.UnmatchedRoot)
			if want := "sub/other.json"; found.Position.Path != want {
				t.Errorf("%s is sited at %q, want %q, the declaring document", vocab.UnmatchedRoot, found.Position.Path, want)
			}
		})
	}
}

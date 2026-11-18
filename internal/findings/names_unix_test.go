//go:build unix

package findings

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/sandbox"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// spelledFile is a file base name holding a colon and a space, which only a
// Unix file system can carry, and the name a finding spells it as
// (finding.schema.json, symbol.name).
const (
	spelledFile = "tool: gen.go"
	spelledName = "tool:gen.go"
)

// writeTree writes files into a directory of the calling test's own.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := sandbox.Dir(t)
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := sandbox.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := sandbox.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	return dir
}

// wantSpelled checks the one finding about the file: its reference keeps the
// file's name, and its name and message spell it as a text line can carry it.
func wantSpelled(t *testing.T, found []Finding, ref, message string) {
	t.Helper()
	if len(found) != 1 || found[0].Symbol.Ref != ref || found[0].Symbol.Name != spelledName || found[0].Message != message {
		t.Errorf("the pass reports %v, want one finding about %s named %q saying %q", summary(found), ref, spelledName, message)
	}
}

func TestAFileNoConfigurationBuildsIsNamedAsATextLineCanCarryIt(t *testing.T) {
	t.Parallel()
	dir := writeTree(t, map[string]string{
		"go.mod":    "module example.com/app\n\ngo 1.27.0\n",
		"app.go":    "package app\n\n// Answer is compiled under every configuration.\nfunc Answer() int { return 1 }\n",
		spelledFile: "//go:build plan9\n\npackage app\n\n// Nine is compiled for no configuration.\nfunc Nine() int { return 9 }\n",
	})
	in := inputOfDir(t, dir, completeMatrixConfig(), Consumers{}, twoConfigurations()...)

	found := emitted(t, "FileNeverBuilt", vocab.FileNeverBuilt, fileNeverBuilt, in)
	wantSpelled(t, found, "go://example.com/app#"+spelledFile+":file",
		"no configuration of the build matrix compiles file "+spelledName)
}

// A file reached through the declaration-backed draft is spelled the same way,
// and so is its entry in a full component listing.
func TestAFilePackageNoImportReachesIsNamedAsATextLineCanCarryIt(t *testing.T) {
	t.Parallel()
	dir := writeTree(t, map[string]string{
		"go.mod":              "module example.com/app\n\ngo 1.27.0\n",
		"main.go":             "package main\n\nfunc main() {}\n",
		"tool/" + spelledFile: "package tool\n\n// Gen is referenced by nothing at all.\nfunc Gen() int { return 1 }\n",
	})
	resolved := applicationConfig()
	resolved.Reporters.CascadeFull = true
	in := inputOfDir(t, dir, resolved, Consumers{})

	result := computed(t, in, map[vocab.Code]Emitter{vocab.FileNeverImported: fileNeverImported})
	wantSpelled(t, result.Findings, "go://example.com/app/tool#"+spelledFile+":file",
		"no import reaches the package file "+spelledName+" declares and no root names a declaration in it")
	for id, members := range result.Members {
		for _, member := range members {
			if member.Name != spelledName {
				t.Errorf("component %s lists %q, want %q, the name its finding spells", id, member.Name, spelledName)
			}
		}
	}
}

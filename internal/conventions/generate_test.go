package conventions

import (
	"slices"
	"testing"
)

// mockgenBoolean are mockgen's flags that take no separate value.
var mockgenBoolean = []string{"typed", "version", "debug_parser", "write_package_comment", "write_source_comment", "write_generate_directive"}

func TestGeneratorOfNamesTheProgramADirectiveRuns(t *testing.T) {
	for _, test := range []struct {
		words []string
		name  string
		args  []string
	}{
		{words: []string{"stringer", "-type=A"}, name: "stringer", args: []string{"-type=A"}},
		{words: []string{"go", "run", "-mod=mod", "go.uber.org/mock/mockgen@v0.5.0", "x"}, name: "mockgen", args: []string{"x"}},
		{words: []string{"go", "tool", "enumer", "-type", "B"}, name: "enumer", args: []string{"-type", "B"}},
		{words: []string{"$GOBIN/mockgen"}, name: "mockgen", args: []string{}},
		{words: []string{"go", "run"}, name: "go", args: []string{"run"}},
		{words: []string{"go", "run", "-x"}, name: "", args: nil},
		{words: []string{"go", "run", "-mod", "mod", "-race", "golang.org/x/tools/cmd/stringer", "-type=A"}, name: "stringer", args: []string{"-type=A"}},
		{words: nil, name: "", args: nil},
	} {
		name, args := generatorOf(test.words)
		if name != test.name || !slices.Equal(args, test.args) {
			t.Errorf("generatorOf(%q) = %q, %q, want %q, %q", test.words, name, args, test.name, test.args)
		}
	}
}

func TestFlagValueAndPositionalArgsReadEachFlagForm(t *testing.T) {
	args := []string{"-typed", "-destination", "mock.go", "-package=app", ".", "Store,Cache"}
	if got := flagValue(args, "destination", mockgenBoolean); got != "mock.go" {
		t.Errorf("flagValue(%q, destination) = %q, want mock.go", args, got)
	}
	if got := flagValue(args, "package", mockgenBoolean); got != "app" {
		t.Errorf("flagValue(%q, package) = %q, want app", args, got)
	}
	if got := flagValue(args, "source", mockgenBoolean); got != "" {
		t.Errorf("flagValue(%q, source) = %q, want none", args, got)
	}
	if got, want := positionalArgs(args, mockgenBoolean), []string{".", "Store,Cache"}; !slices.Equal(got, want) {
		t.Errorf("positionalArgs(%q) = %q, want %q", args, got, want)
	}
	boolean := []string{"-linecomment", "-type", "Kind", "-json", "-output=x.go"}
	if got := flagValue(boolean, "type", nil); got != "Kind" {
		t.Errorf("flagValue(%q, type) = %q, want Kind: a flag before another flag takes no value", boolean, got)
	}
	unlisted := []string{"-self_package_check", "-destination", "mock.go", ".", "Store"}
	if got, want := positionalArgs(unlisted, mockgenBoolean), []string{".", "Store"}; !slices.Equal(got, want) {
		t.Errorf("positionalArgs(%q) = %q, want %q: a flag before another flag takes no value", unlisted, got, want)
	}
}

func TestGenerateWordsKeepsAQuotedStringOneWordAndExpandsEachWord(t *testing.T) {
	got := generateWords(`mockgen  -source "my file.go" -destination=mock_$GOFILE	-package=$GOPACKAGE`, "f.go", "p")
	want := []string{"mockgen", "-source", "my file.go", "-destination=mock_f.go", "-package=p"}
	if !slices.Equal(got, want) {
		t.Errorf("generateWords() = %q, want %q", got, want)
	}
}

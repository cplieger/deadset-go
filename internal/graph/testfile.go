package graph

import (
	"path/filepath"
	"strings"
)

// goTestSuffixRule names the Go language's own test-file rule.
const goTestSuffixRule = "go-test-suffix"

// testFileSuffix is what that rule matches.
const testFileSuffix = "_test.go"

// TestFileRule is one rule that classified files as test files and how many it
// matched. A rule that matched nothing is reported all the same, so a project
// whose tests are named some other way reads a count of zero rather than an
// absent rule.
type TestFileRule struct {
	Rule    string
	Matched int
}

// IsTestFile reports whether the language's own rule classifies the file as a
// test file, and names the rule that decided.
//
// The rule is the file name's _test.go suffix and nothing else: no build
// constraint, no directory and no package name takes part. A name the toolchain
// compiles nothing from, being one whose first character is an underscore or a
// full stop, is no test file either.
func IsTestFile(path string) (rule string, ok bool) {
	stem, found := strings.CutSuffix(filepath.Base(path), testFileSuffix)
	if !found || stem == "" || stem[0] == '_' || stem[0] == '.' {
		return "", false
	}
	return goTestSuffixRule, true
}

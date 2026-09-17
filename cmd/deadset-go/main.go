// Command deadset-go reports unused symbols in a Go module and its declared
// consumers. It implements the deadset Contract published at
// github.com/cplieger/deadset-spec, and no verb edits a source file.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	version         = "0.1.0-dev"
	contractVersion = "0.1.0"
)

const usage = "usage: deadset-go <analyze|explain|print-config|print-roots|print-retained|describe|version> [flags]"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one invocation and returns its exit code: 0 for a served verb,
// 2 for a usage error or a requested source edit.
func run(args []string, stdout, stderr io.Writer) int {
	if name, ok := sourceEditFlag(args); ok {
		fmt.Fprintf(stderr, "deadset-go: %s is not supported: deadset-go reports and never edits a source file\n", name)
		return 2
	}

	fs := flag.NewFlagSet("deadset-go", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	switch verb := fs.Arg(0); verb {
	case "version":
		fmt.Fprintf(stdout, "deadset-go %s\ncontract %s\n", version, contractVersion)
		return 0
	case "":
		fs.Usage()
		return 2
	default:
		fmt.Fprintf(stderr, "deadset-go: unknown verb %q\n", verb)
		fs.Usage()
		return 2
	}
}

// sourceEditFlag returns the first flag whose name contains "fix", in any
// position, because every verb is report-only.
func sourceEditFlag(args []string) (string, bool) {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, _ := strings.Cut(arg, "=")
		if strings.Contains(strings.TrimLeft(name, "-"), "fix") {
			return name, true
		}
	}
	return "", false
}

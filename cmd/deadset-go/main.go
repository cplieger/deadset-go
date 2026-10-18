// Command deadset-go reports unused symbols in a Go module and its declared
// consumers. It implements the deadset Contract published at
// github.com/cplieger/deadset-spec/v8, and no verb edits a source file.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/cplieger/deadset-go/v2/internal/memory"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

// name is this analyzer's name wherever a document names a product.
const name = "deadset-go"

// language is the language this analyzer claims.
const language = vocab.Go

const usage = "usage: deadset-go <analyze|explain|print-config|print-roots|print-retained|describe|version> [flags]"

func main() {
	os.Exit(governed(os.Args[1:], os.Stdout, os.Stderr, memory.Platform()))
}

// governed runs one invocation under the memory governor reading machine. An
// interrupt or a termination signal reaches the toolchain a load spawns through the context, so a cancelled
// run stops rather than waiting for the packages it asked for, and the governor ends
// a run that cannot fit by cancelling the same context.
func governed(args []string, stdout, stderr io.Writer, machine memory.Machine) int {
	signalled, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancelCause(signalled)
	defer cancel(nil)
	govern := memory.Start(ctx, cancel, machine)
	defer govern()
	return invoke(ctx, args, stdout, stderr)
}

// invoke executes one invocation and returns its exit code: 0 for a served verb that
// found nothing to report, 1 for a finding, 2 for a usage error, a requested
// source edit or a configuration this analyzer refuses, 3 for a failure that
// produced no answer.
func invoke(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if flagName, ok := sourceEditFlag(args); ok {
		fmt.Fprintf(stderr, "deadset-go: %s is not supported: deadset-go reports and never edits a source file\n", flagName)
		fmt.Fprintln(stderr, usage)
		return vocab.ExitUsage
	}

	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := set.Parse(args); err != nil {
		return vocab.ExitUsage
	}

	switch verb := set.Arg(0); verb {
	case "version":
		if len(set.Args()) > 1 {
			fmt.Fprintf(stderr, "deadset-go: version takes no argument, got %q\n", set.Arg(1))
			set.Usage()
			return vocab.ExitUsage
		}
		if _, err := fmt.Fprintf(stdout, "deadset-go %s\ncontract %s\n", version(), vocab.ContractVersion); err != nil {
			return unwrittenAnswer(stderr, "version", err)
		}
		return vocab.ExitClean
	case "describe":
		return describe(set.Args()[1:], stdout, stderr)
	case "print-config":
		return printConfig(ctx, set.Args()[1:], stdout, stderr)
	case "print-roots":
		return printRoots(ctx, set.Args()[1:], stdout, stderr)
	case "print-retained":
		return printRetained(ctx, set.Args()[1:], stdout, stderr)
	case "analyze":
		return analyze(ctx, set.Args()[1:], stderr)
	case "explain":
		return explain(ctx, set.Args()[1:], stdout, stderr)
	case "":
		set.Usage()
		return vocab.ExitUsage
	default:
		fmt.Fprintf(stderr, "deadset-go: unknown verb %q\n", verb)
		set.Usage()
		return vocab.ExitUsage
	}
}

// sourceEditTokens are the tokens whose presence in a flag's name makes that flag
// a request to edit a source file. The set is closed, and a token is matched in
// any position of the name.
var sourceEditTokens = []string{"fix", "edit", "delete", "rewrite"}

// sourceEditFlag returns the first flag whose name carries a source-edit token,
// because every verb is report-only. The match is on the name alone, so a flag
// whose value carries a token is not a request.
func sourceEditFlag(args []string) (string, bool) {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flagName, _, _ := strings.Cut(arg, "=")
		name := strings.TrimLeft(flagName, "-")
		if slices.ContainsFunc(sourceEditTokens, func(token string) bool {
			return strings.Contains(name, token)
		}) {
			return flagName, true
		}
	}
	return "", false
}

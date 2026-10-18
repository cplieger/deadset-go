package main

import (
	"context"
	"fmt"
	"go/token"
	"io"

	"github.com/cplieger/deadset-go/v2/internal/exempt"
	"github.com/cplieger/deadset-go/v2/internal/inventory"
	"github.com/cplieger/deadset-go/v2/internal/run"
	"github.com/cplieger/deadset-spec/v8/vocab"
)

const printRetainedUsage = "usage: deadset-go print-retained [--target=DIR] [--config=FILE] [--central=FILE] [--scope=FILE] [--min-confidence=CLASS] [--mode=MODE]"

// The modes --mode spells. The production mode is the report's: it counts no
// reference a test file made, so an exemption whose only evidence is in a test holds
// nothing. The plain mode counts every reference.
const (
	modeProduction = "production"
	modePlain      = "plain"
)

// printRetained writes the retained set of the matrix to stdout, one line per symbol
// an exemption held back under any configuration: the reference and position, then
// per class the class, the evidence site and the clause, separated by tabs. The set
// is the report's unless --mode names the plain mode, so this verb and explain
// answer about one analysis.
func printRetained(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := configuredFlagSet("print-retained", printRetainedUsage, stderr)
	named := flags.set.String("mode", modeProduction,
		"which analysis the retained set is read from: "+modeProduction+" or "+modePlain)
	if err := flags.set.Parse(args); err != nil {
		return vocab.ExitUsage
	}
	if flags.set.NArg() != 0 {
		fmt.Fprintf(stderr, "deadset-go: print-retained takes no argument, got %q\n", flags.set.Arg(0))
		flags.set.Usage()
		return vocab.ExitUsage
	}
	if *named != modeProduction && *named != modePlain {
		fmt.Fprintf(stderr, "deadset-go: --mode=%q: the values are %s and %s\n", *named, modeProduction, modePlain)
		flags.set.Usage()
		return vocab.ExitUsage
	}
	req, code := flags.request(ctx, stderr)
	if code != vocab.ExitClean {
		return code
	}

	swept, err := sweptOf(ctx, req, *named == modeProduction)
	if err != nil {
		return failed(ctx, stderr, err)
	}
	namedUnbuilt(stderr, swept.Unbuilt)
	positions := make(map[inventory.SymbolID]string, len(swept.Merged.Symbols))
	for i := range swept.Merged.Symbols {
		positions[swept.Merged.Symbols[i].ID] = swept.Merged.Symbols[i].Pos.String()
	}
	return printLines(stdout, stderr, "print-retained", retainedLines(swept.Refs, positions, swept.Retained()), swept.Unmatched)
}

// sweptOf runs a request through the sweep stage under the production or the plain
// mode.
func sweptOf(ctx context.Context, req *run.Request, production bool) (*run.Swept, error) {
	loaded, err := run.Load(ctx, req)
	if err != nil {
		return nil, err
	}
	prepared, err := loaded.Prepare(production)
	if err != nil {
		return nil, err
	}
	return prepared.Sweep()
}

// findingsOf runs a request through the findings stage under the production mode,
// which is the sweep the findings are about.
func findingsOf(ctx context.Context, req *run.Request) (*run.Findings, error) {
	swept, err := sweptOf(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return swept.Findings(ctx)
}

// retainedLines renders one line per retained symbol. The listing groups a symbol's
// classes adjacently, so one pass emits one line per symbol and nothing is re-sorted.
func retainedLines(refs, positions map[inventory.SymbolID]string, retained []exempt.Exemption) []string {
	var lines []string
	for i := range retained {
		held := &retained[i]
		if i == 0 || held.ID != retained[i-1].ID {
			lines = append(lines, refs[held.ID]+"\t"+positions[held.ID])
		}
		lines[len(lines)-1] += "\t" + held.Class + "\t" + evidenceSite(held) + "\t" + held.Detail
	}
	return lines
}

// evidenceSite renders the position an exemption recorded, as an empty field where it
// recorded none.
func evidenceSite(e *exempt.Exemption) string {
	if e.Site.Filename == "" {
		return ""
	}
	return consumerSite(e.Consumer, e.Site)
}

// consumerSite renders a position, a loaded consumer's file prefixed with that
// consumer's module path, the way a trimmed Go build names it. consumer is empty for
// a position in the target.
func consumerSite(consumer string, at token.Position) string {
	if consumer != "" {
		return consumer + "/" + at.String()
	}
	return at.String()
}

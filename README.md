# deadset-go

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/deadset-go.svg)](https://pkg.go.dev/github.com/cplieger/deadset-go)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/deadset-go)](https://github.com/cplieger/deadset-go/blob/main/go.mod)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/deadset-go/badges/mutation.json)](https://github.com/cplieger/deadset-go/issues?q=label%3Agremlins-tracker)

deadset-go finds dead code in a Go module and in the repositories that import it, and writes a report your CI can fail on.

It type-checks the module with its tests through `golang.org/x/tools`, its one run-time dependency, and reports without editing your code. It is in active development, so the report shape, exit codes and configuration keys can change between minor releases. It supports Linux only, needs Go 1.27.1 or later and is licensed under GPL-3.0-or-later.

## Why use it

deadset-go is built for a CI gate on dead code across a module, with every verdict from type information and the reference graph.

- It reports unused declarations, declarations only tests use, and tests of dead code.
- It reports exports only their own package uses, fields written and never read, and uncalled interface methods.
- Inside a function, it reports unused parameters, results and receivers, unreachable code and dead stores.
- It loads the consumers you name, so an export a downstream repository calls is never reported.
- Nine exemption classes hold back code that encoders, `fmt`, templates or reflection reach.
- The same tree gives the same report, as JSON, text, GitHub annotations, SARIF 2.1.0 or your own template.

Consider [`deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) if you want the functions no call path from a `main` package reaches. It follows calls through func values, interface methods and reflection, and `-whylive` shows the path that keeps a function live.

## Install

```sh
go install github.com/cplieger/deadset-go/cmd/deadset-go@latest
```

## Usage

deadset-go loads packages with the `go` toolchain on your `PATH` and never downloads a module, so run `go mod download` in the module and in every consumer first. Then create a `deadset.json` at the module root that says whether the module is an `application` or a `library`, and run `analyze` there:

```sh
echo '{ "target": { "kind": "application" } }' > deadset.json
deadset-go analyze --report=deadset-report.json
```

`analyze` writes the JSON report to `deadset-report.json` and a text rendering beside it as `deadset-report.json.txt`. Take this `main.go`:

```go
package main

import "fmt"

type counter struct {
	count int
	label string
}

func (c *counter) increment() { c.count++ }

func greet(name string) string { return "hello " + name }

func farewell(name string) string { return "bye " + name }

func main() {
	c := &counter{label: "x"}
	c.increment()
	fmt.Println(greet("you"), c.count)
}
```

The text rendering lists two findings:

```text
main.go:7:2: field counter.label: field counter.label is written once and never read [certain] (DS1301)
main.go:14:6: function farewell: unexported function has no reference in the target [certain] (DS1002)
summary: 2 findings (0 allow, 0 warn, 2 deny), 1 deletable line, 0 suppressions in effect, 0 reasons recorded, 0 stale suppressions, 0 pending, 0 omitted
```

The run exits 1 because the report holds a finding at `deny` severity, and 0 when it holds none. Common next steps:

- Add `--format=sarif` or `--format=github` for a SARIF file or GitHub annotations. Naming any format replaces the text rendering, so add `--format=text` to keep it.
- Run `deadset-go explain farewell` to see why one symbol is reported, retained, live or not reported.
- To keep one declaration, put `//deadset:ignore DS1002 -- <reason>` on the line above it. To accept today's findings, add `--baseline-write=deadset-baseline.json`.

A library's unused exports are reported only when a scope document lists its consumers. Put a `scope.json` in a folder that holds the library and its consumers, with each path relative to that file:

```json
{
  "target": { "path": "lib", "role": "target" },
  "consumers": [{ "path": "app", "role": "consumer" }]
}
```

Then run from that folder, with `--target` naming the library folder whose `deadset.json` says `library`:

```sh
deadset-go analyze --target=lib --scope=scope.json --report=deadset-report.json
```

## API

deadset-go is a standalone command, and its interface is seven verbs, the JSON report and the exit codes.

- `analyze` writes the report and exits with the verdict, and `explain` answers for one symbol.
- `print-config`, `print-roots` and `print-retained` print the resolved configuration with each setting's source, the root set, and every symbol an exemption held back.
- `describe` prints the analyzer, contract and schema versions and the conformance record as JSON, and `version` prints the analyzer and contract versions.
- The run exits 0 when no finding fails it and 1 when one does. It exits 2 on a usage error or a refused source-edit flag, and 3 when the analysis fails before a verdict. It exits 4 when a finding is about a symbol a cross-language edge names, which only the merge in [deadset](https://github.com/cplieger/deadset) can settle.

The report follows the [deadset contract](https://github.com/cplieger/deadset-spec). [Configuration and invocation](docs/configuration.md) lists every flag, setting and suppression form.

## Unsupported by design

- Source edits. Every verb only reports, and a flag whose name holds `fix`, `edit`, `delete` or `rewrite` exits with 2. The JSON report carries what an external codemod needs.
- Network access. Consumers are local checkouts, and the toolchain runs with `GOPROXY=off`. A module missing from the module cache then stops the run with exit 3 where a declared or host configuration needs it.
- Runtime evidence. Coverage profiles and production logs are not inputs.
- Guesses. A kind ships only where its answer follows exactly from type information and the module graph.
- Other languages. [deadset-ts](https://github.com/cplieger/deadset-ts) analyzes TypeScript and JavaScript.

[Conformance, platforms and non-goals](docs/conformance.md#non-goals) explains each one.

## Related projects

deadset-go implements the [deadset contract](https://github.com/cplieger/deadset-spec), which fixes the issue codes, the report schema and the exit codes. It passes all 52 of the contract's conformance fixtures that carry a Go rendering, and every report names that result.

- [deadset-ts](https://github.com/cplieger/deadset-ts) is the same analysis for TypeScript and JavaScript.
- [deadset](https://github.com/cplieger/deadset) runs both analyzers as one command and merges their reports, resolving the references between Go and TypeScript code.

## Documentation

- [Configuration and invocation](docs/configuration.md) lists every setting, verb, flag, suppression form and exit code.
- [Issue kinds](docs/kinds.md) states what each code reports and what counts as a use.
- [Exemption classes](docs/exemptions.md) explains the nine reasons a symbol is held back.
- [How the analysis decides](docs/analysis.md) covers the roots, consumers, build configurations and cgo files.
- [Conformance, platforms and non-goals](docs/conformance.md) gives the contract version, the conformance result, the platforms and the performance budget.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).

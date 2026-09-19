# deadset-go

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/deadset-go.svg)](https://pkg.go.dev/github.com/cplieger/deadset-go)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/deadset-go)](https://github.com/cplieger/deadset-go/blob/main/go.mod)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/deadset-go/badges/mutation.json)](https://github.com/cplieger/deadset-go/issues?q=label%3Agremlins-tracker)

Deterministic whole-program dead-code analysis for a Go module and the consumers it declares.

## ⚠️ Pre-release software

deadset-go implements the [deadset contract](https://github.com/cplieger/deadset-spec) at the version `describe` prints. `analyze` reads a Go module and writes the contract's JSON report, rendered beside it as text, GitHub annotations, SARIF 2.1.0 or a template of yours; `explain` answers whether one symbol is live, retained, reported or judged by nothing, and why. At its 1.10.0 release the analyzer reports 74 findings over its own source, every one of them true: exports narrower than they are declared, fields a pass writes that nothing reads, methods whose bodies never name their receiver, and two declarations only a test names. A green `go vet` or `golangci-lint` beside that number is not a contradiction, because both run per package with tests included, and these are the two questions they do not ask.

Every report names the analyzer's conformance result in its `analyzer.conformance` block, and an orchestrator admits a pass and nothing else, so read that block rather than gating blind on a report. The contract version, the corpus version, the declared gaps, the platform set and the non-goals are in [docs/conformance.md](docs/conformance.md). The report shape, the exit codes and the configuration keys can change until 1.0.

## What it does

deadset-go loads a Go module with its tests, type-checks it and reports the declarations nothing uses: a function nobody calls, a struct field nothing reads, an exported symbol no consumer imports, an interface no value is converted to. Every verdict comes from type information and the reference graph, never from a text search, so two runs over one tree produce one report. Every kind it reports, and what it treats as a use, is in [docs/kinds.md](docs/kinds.md).

Three properties separate it from a per-package linter:

- **Consumers are part of the program.** A library's exported API is dead only when no declared consumer uses it. Name the repositories that import the module and the analysis loads them into one whole-program graph, so an export a downstream repository calls is never reported.
- **Exemptions are computed, not configured.** A method that satisfies an interface a value of its type is converted to, a `main`, an `init`, a test function, a symbol a `go:linkname` directive names: each is held live by a documented exemption class, and `print-retained` lists every symbol an exemption held back and the class that held it. The nine classes are in [docs/exemptions.md](docs/exemptions.md).
- **A cascade is one finding.** When a dead function is the only caller of three more, the report names the root and counts what falls with it, so the deletion total is known before the edit.

It also reports over-visibility: an exported symbol only its own package uses, which can be unexported without a behavior change.

The output is a report, never an edit: a JSON document, one `path:line:col` text line per finding, or SARIF 2.1.0 for code-scanning upload. Every verb refuses a `--fix` flag with exit 2.

## Quick start

```sh
go install github.com/cplieger/deadset-go/cmd/deadset-go@latest
deadset-go version
```

Installing needs Go 1.27 or later, and running needs the `go` toolchain on `PATH`, which loads the packages. deadset-go runs no language server and builds with cgo disabled, so a host with no C toolchain runs it.

## Commands

| Verb | What it does |
| --- | --- |
| `analyze` | Loads the module and its declared consumers, writes the report, exits with the verdict |
| `explain` | Says why a symbol is dead, why it is live, or why it was not reported |
| `print-config` | Prints the resolved configuration with the source of every setting |
| `print-roots` | Prints the resolved root set |
| `print-retained` | Prints every symbol an exemption held back, with the classes that held it |
| `describe` | Prints the analyzer's name, version, contract version and conformance record as JSON |
| `version` | Prints the analyzer version and the contract version |

Exit codes follow the contract: 0 clean, 1 findings, 2 a usage error or a requested source edit, 3 a load or type-check failure, 4 a finding whose cross-language reference is unresolved. Every configuration key, every flag, the three suppression forms and the exit-code table are in [docs/configuration.md](docs/configuration.md).

## How it relates to deadcode

[`golang.org/x/tools/cmd/deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) answers a different question, and running both is worth it. `deadcode` builds a call graph from each `main` with rapid type analysis, which resolves calls through interfaces by tracking the types that reach run time, and reports the functions no call path reaches. deadset-go covers the symbol kinds a call graph has no node for (types, fields, constants, variables, interface methods, type parameters), treats a library's declared consumers as part of the program, and records on each finding which relation produced it: a reference count of zero, or unreachability from the roots. Run `deadcode` for the functions and deadset-go for everything else. The two liveness relations, the reachability classes, the build matrix and what leaves the analysis are in [docs/analysis.md](docs/analysis.md).

## Security

deadset-go reads source and writes a report. It edits no file, and every verb refuses a `--fix` flag, or any flag whose name contains `fix`, with exit 2. It spawns no analysis process other than the Go toolchain's own `go list`, which resolves the module's dependencies as `go build` does, and it opens no network connection of its own: a module download happens only where `go list` would download, so a run against a populated module cache stays offline.

## Related projects

- [deadset-spec](https://github.com/cplieger/deadset-spec): the contract and the conformance corpus deadset-go implements and passes before each release.
- [deadset-ts](https://github.com/cplieger/deadset-ts): the same analysis for TypeScript and JavaScript. Neither analyzer can analyze the other's language: the TypeScript 7 compiler module `github.com/microsoft/TypeScript/tsc` keeps every analysis package under `internal/`, its programmatic surface is a TypeScript client over a private protocol, and no port of Go's type checker to JavaScript exists.
- [deadset](https://github.com/cplieger/deadset): runs both analyzers over a repository holding both languages, resolves the references that cross the language boundary and merges the reports into one.

## Dependencies

deadset-go allows itself two dependencies: the Go standard library and `golang.org/x/tools`, which supplies the package loader and the type information no reimplementation can. `go version -m` on a released binary lists nothing else. Renovate keeps the pins in `go.mod` current.

## Contributing

Issues and pull requests are welcome. The general guidelines live in [cplieger/.github](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).

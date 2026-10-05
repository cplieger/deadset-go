# Conformance, platforms and non-goals

This page states what deadset-go implements, what it declines, where it runs and what it is built on. It is for anyone who pins a contract version or gates CI on a report.

## The contract version

deadset-go implements deadset contract version 5.3.0, and writes and reads report schema version 7.0.0. Every report names both, and `deadset-go describe` prints them with the analyzer's own version as JSON:

```sh
deadset-go describe
```

A consumer that requires a particular contract version reads it from that output or from the `contract_version` member of any report, rather than from the analyzer's version number. The two move independently.

## The conformance corpus

The contract publishes a conformance corpus, a set of fixtures each carrying its own expectation, and deadset-go runs it as part of its own test suite. The corpus version this analyzer answers is 3.0.0, which publishes 117 fixtures, and the run answers the 65 fixtures of it that carry a Go rendering. Every one of them passes, none is a gap and none fails.

Every report names the result in its `analyzer.conformance` block, with the corpus version answered and the digest of the results document the corpus run wrote. `describe` prints the same record. That document, `conformance-results.json` at the repository root, names the version of the build that ran the corpus. Under the test suite that version is `0.0.0-devel`, so the committed document is reproducible from the source at its commit, and its digest binds the record to that source. A merge admits a report whose result is a pass and no other, so a consumer gating on conformance reads that block rather than assuming one.

### Declared gaps

A capability this analyzer does not implement is declared rather than silently absent. The declarations live in `conformance.json` at the repository root, one row per fixture and capability, each naming the capability and the reason. A capability is an issue-kind code or an exemption class. An expectation the analyzer neither answers nor declares here is a failure of the corpus run rather than a gap.

At corpus version 3.0.0 this analyzer declares no gap, so `conformance.json` carries an empty list and the `declared_gaps` member of every report is empty.

Three codes of the contract are outside this analyzer, and none of them is a gap in its Go coverage. `DS1104` and `DS1706` apply to TypeScript and JavaScript, so the corpus fixtures for them carry no Go rendering, and `DS1705` is reported by the orchestrator's merge from the edge evaluations this analyzer publishes. All three are listed in [Issue kinds](kinds.md#kinds-this-analyzer-does-not-report). The four exemption classes the contract declares for TypeScript alone are outside this analyzer for the same reason. The nine Go classes are all implemented and are in [Exemption classes](exemptions.md).

## Platforms

deadset-go supports Linux, and its supported environment is a GitHub Actions Ubuntu runner with the Go toolchain that runner provides. The binary links no C code, so a host with no C toolchain runs it. A cgo package in the target is analyzed with the C half opaque rather than skipped, as [How the analysis decides](analysis.md#cgo-files-with-the-c-half-opaque) describes.

The analyzer is held to a budget of 250,000 lines in 60 seconds on four cores. The measurement recorded in `cmd/deadset-go/testdata/benchmark.json` analyzed a generated module of 250,803 lines in 3.7 seconds on a 20-core amd64 machine.

Running needs the `go` toolchain on `PATH`, which is what loads the target's packages. The load never downloads a module, so the local module cache must already hold every module the target and each consumer require. Run `go mod download` in each of them before the first run. No language server is needed and none is started.

A repository holding both Go and TypeScript runs [deadset](https://github.com/cplieger/deadset), because neither analyzer resolves a cross-language edge alone.

## Dependencies

The built binary links the Go standard library, `golang.org/x/tools`, and the modules `golang.org/x/tools` itself links. `x/tools` supplies the package loader and the type information no reimplementation can, which is the whole reason it is there. `go version -m` on an installed binary lists nothing else. The conformance corpus and the property-testing instrument are read by the test suite alone and reach no binary.

## Non-goals

Each of these is declined rather than unbuilt, and each names what provides the capability instead.

### No source edit, in any mode

Every verb reports and edits no file, and a flag asking for an edit exits with the usage code. A run leaves every file of the target byte-identical. The report is the interface for an edit. Per finding, the JSON report carries the position, the size of the declaration in lines, the symbol kind, the symbol name, the parent symbol, the dead component and the fixability. An external codemod can act on those fields without re-running the analysis.

### No runtime liveness

Coverage profiles, production logs and tombstones are not inputs. Every verdict comes from type information and the reference graph, so two runs over one tree produce one report and a symbol's liveness does not depend on what happened to run.

### No network request and no cache between runs

The consumer set comes from the scope document the invocation names, which holds local paths. The toolchain's `go list` runs with `GOPROXY=off`, so a module the local module cache does not hold ends the load with the toolchain's own message instead of being downloaded. Nothing is written to disk between runs, so no cached state changes an answer.

### One language

deadset-go analyzes only Go, and the TypeScript analyzer analyzes only TypeScript and JavaScript. Neither can analyze the other's language. The TypeScript 7 compiler module `github.com/microsoft/TypeScript/tsc` places every analysis package under `internal/` and exports no library package. The shipped TypeScript 7 programmatic surface is a TypeScript client over a private protocol, and no port of Go's type checker to JavaScript exists.

### No orchestrator hop for a single-language repository

deadset-go is installable and runnable on its own, and a repository holding only Go needs nothing else. A repository holding both languages runs the orchestrator, [deadset](https://github.com/cplieger/deadset), which invokes each analyzer as a process, resolves the cross-language edges neither analyzer can see alone and merges the reports into one.

### No kind whose answer needs a guess

A kind ships only where what it reports is deletable, or narrowable, without a behavior change, and where the answer follows exactly from type information and the graph. That is why the retired codes in [Issue kinds](kinds.md#retired-codes) stay retired. Each names a question whose use site lives outside anything a type checker or a module graph reads, or a defect that belongs to a linter rather than to a dead-code analyzer.

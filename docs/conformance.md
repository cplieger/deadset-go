# Conformance, platforms and non-goals

What this analyzer implements, what it declines, where it runs, and what it is built on.

## The contract version

deadset-go implements deadset contract version **1.5.0** and reads and writes report schema
version **4.0.0**. Every report names both, and `deadset-go describe` prints them with the
analyzer's own version as JSON:

```sh
deadset-go describe
```

A consumer that requires a particular contract version reads it from that output or from the
`contract_version` member of any report, rather than from the analyzer's version number: the two
move independently.

## The conformance corpus

The contract publishes a conformance corpus, a set of fixtures each carrying its own expectation,
and deadset-go runs it as part of its own test suite. The corpus version this analyzer answers is
**1.1.0**.

Every report names the result in its `analyzer.conformance` block, with the corpus version
answered and the digest of the results document the corpus run wrote; `describe` prints the same
record. That document, `conformance-results.json` at the repository root, names the version of the
build that ran the corpus, which under the test suite is `0.0.0-devel`, so the committed document
is reproducible from the source at its commit and its digest binds the record to that source. A
merge admits a report whose result is a pass and no other, so a consumer gating on conformance
reads that block rather than assuming one.

### Declared gaps

A capability this analyzer does not implement is declared rather than silently absent. The
declarations live in `conformance.json` at the repository root, one row per fixture and
capability, each naming the capability and the reason. A capability is an issue-kind code or an
exemption class; an expectation the analyzer neither answers nor declares here is a failure of the
corpus run rather than a gap.

At corpus version 1.1.0 this analyzer declares two gaps, both on the fixture
`unused-requirement-noop-replace` and both the same limitation. A suppression binds to a
declaration, and neither a requirement of the module file (`DS1601`) nor a `replace` directive of it
(`DS1605`) is one, so no ignore entry and no inline directive suppresses such a finding: an entry
naming one is reported as a stale suppression instead. The remedy is mechanical rather than a
suppression, `go mod tidy` for a requirement and deleting the directive for a `replace` whose
target is absent from the build list, and setting the severity of either kind to allow silences it.
Every report carries the same list in its `declared_gaps` member.

Two codes of the contract are outside the corpus and outside this analyzer, for reasons that are
not gaps in its Go coverage: `DS1104` applies to TypeScript and JavaScript, and `DS1705` is
reported by the orchestrator's merge from the edge evaluations this analyzer publishes. Both are
stated in [kinds.md](kinds.md). The four exemption classes the contract declares for TypeScript
alone are outside this analyzer for the same reason; the nine Go classes are all implemented and
are in [exemptions.md](exemptions.md).

## Platforms

deadset-go supports Linux. The binary is built with cgo disabled and links no C library, so a host
with no C toolchain runs it, and a cgo package in the target is analyzed with the C half opaque
rather than skipped; see [analysis.md](analysis.md).

Two environments are supported:

- **A GitHub Actions Ubuntu runner**, using the Go toolchain that runner provides.
- **The orchestrator's container image**, which carries this analyzer together with the runtimes an
  analysis needs. That image is the route for a repository holding both Go and TypeScript, because
  neither analyzer resolves a cross-language edge alone, and it is released by
  [deadset](https://github.com/cplieger/deadset) rather than here.

Running needs the `go` toolchain on `PATH`, which is what loads the target's packages, and the
module cache it reads. No language server is needed and none is started.

## Dependencies

The built binary links the Go standard library, `golang.org/x/tools`, and the modules
`golang.org/x/tools` itself links. `x/tools` supplies the package loader and the type information
no reimplementation can, which is the whole reason it is there; `go version -m` on a released
binary lists nothing else. The conformance corpus and the property-testing instrument are read by
the test suite alone and reach no binary.

## Non-goals

Each of these is declined rather than unbuilt, and each names what provides the capability
instead.

**No source edit, in any mode.** Every verb reports and edits no file, and a flag asking for an
edit exits with the usage code. A run leaves every file of the target byte-identical. The report is
the interface for an edit: the JSON report carries, per finding, the position, the size of the
declaration in lines, the symbol kind, the symbol name, the parent symbol, the dead component and
the fixability, which is what an external codemod needs to act without re-running the analysis.

**No runtime liveness.** Coverage profiles, production logs and tombstones are not inputs. Every
verdict comes from type information and the reference graph, so two runs over one tree produce one
report and a symbol's liveness does not depend on what happened to run.

**No network request, and no cache between runs.** The consumer set comes from the scope document
the invocation names, which holds local paths. A module download happens only where the toolchain's
own `go list` would download, so a run against a populated module cache stays offline. Nothing is
written to disk between runs, so no cached state changes an answer.

**One language.** deadset-go analyzes Go and nothing else, and the TypeScript analyzer analyzes
TypeScript and JavaScript and nothing else. Neither can analyze the other's language: the
TypeScript 7 compiler module `github.com/microsoft/TypeScript/tsc` places every analysis package
under `internal/` and exports no library package, the shipped TypeScript 7 programmatic surface is
a TypeScript client over a private protocol, and no port of Go's type checker to JavaScript exists.

**No orchestrator hop for a single-language repository.** deadset-go is installable and runnable on
its own, and a repository holding only Go needs nothing else. A repository holding both languages
runs the orchestrator, which invokes each analyzer as a process, resolves the cross-language edges
neither analyzer can see alone and merges the reports into one.

**No kind whose answer needs a guess.** A kind ships only where what it reports is deletable, or
narrowable, without a behavior change, and where the answer follows exactly from type information
and the graph. That is why the retired codes in [kinds.md](kinds.md) stay retired: each names a
question whose use site lives outside anything a type checker or a module graph reads, or a defect
that belongs to a linter rather than to a dead-code analyzer.

# Issue kinds

deadset-go reports 29 of the 31 issue kinds the deadset contract declares. This page states,
per code, what this analyzer reports under it and what it treats as a use, so a reader can tell
why a symbol was reported and why a symbol was not. The vocabulary itself, the code space and
the fields a finding carries are stated once in the contract's
[issue kinds page](https://github.com/cplieger/deadset-spec/blob/v2.0.0/docs/kinds.md), which
this page does not restate.

Every kind is enabled by default and every kind declares the confidence ceiling `certain`, so a
finding's `confidence` equals its `reachability_class`. `Severity` is what a finding does to the
run: a `deny` finding fails it, a `warn` finding does not. `Fixability` is what a mechanical edit
may do with the finding: `deletable` remove the declaration, `narrowable` reduce its visibility,
`manual` a maintainer decides, `none` nothing mechanical acts on it. A configuration changes a
severity by code or by two-digit family prefix; see
[configuration.md](configuration.md).

Two codes of the contract are absent from this page and named under
[Kinds this analyzer does not report](#kinds-this-analyzer-does-not-report).

## What counts as a use

Every kind below rests on one reference set, built once per build configuration from the
type-checked program. A reference is recorded where the type checker resolves an identifier to
the declaration it denotes, so a name in a comment, a string or a build directive is no
reference and a text search plays no part.

Each reference is classified by the position the identifier is written in: a read, a write, a
call, a type use, a conversion, an embedding or a type assertion. The classification decides the
read-and-write kinds and nothing else; every other kind asks only whether a reference exists.

Seven rules of that classification answer most questions about a symbol the analyzer did not
report:

- The defining identifier of a declaration is no reference to it, so a symbol named only at its
  own declaration site is unreferenced.
- A call a function makes to itself is a reference, so a recursive function is referenced.
- A selector resolves to the field or method it selects rather than to the enclosing type, and a
  selector through an embedded field records a read of each field on the path.
- The left side of an assignment, the operand of `++`, `--` or a compound assignment, a field key
  in a composite literal, an index or map assignment and a `delete` are writes. Everything else
  is a read.
- A struct value used as an operand of `==` or `!=`, as a map key, or as a switch tag or case
  expression reads every field of that type and of every struct field beneath it, because
  equality reads them all.
- `x = append(x, v)` reads nothing of `x`: the read inside the call is the mechanics of the
  store. `y = append(x, v)` reads `x`.
- The operand of `&` is a read, and `*p = v` reads `p`, because the write lands on the pointee
  rather than on the symbol.

A reference from a test file is a test reference, which is what separates the test-only kind from
the unused kinds. A reference from a declared consumer the run loaded is an ordinary reference;
a reference from a consumer's test files is a test reference unless the configuration counts
consumer tests as production.

Beyond the reference set, a symbol is held live by a root or by an exemption class. The root set
is printable with `print-roots` and the retained set with `print-retained`; the classes are in
[exemptions.md](exemptions.md) and the two liveness relations in [analysis.md](analysis.md).

## DS1000 to DS1099: unused declarations

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1001` | `unused-exported` | on | `deny` | `deletable` |
| `DS1002` | `unused-unexported` | on | `deny` | `deletable` |
| `DS1003` | `unused-member` | on | `deny` | `deletable` |
| `DS1004` | `test-only-use` | on | `deny` | `deletable` |
| `DS1005` | `test-of-dead-code` | on | `deny` | `deletable` |
| `DS1006` | `deprecated-unused` | on | `deny` | `deletable` |

### DS1001 unused-exported

An exported declaration that is not a member of a type and that no reference names, in the
target or in any loaded consumer.

The kind reports nothing for a library whose published API is open world, which is a library
whose configuration does not declare the consumer set complete or one of whose declared
consumers did not load: a caller the analysis cannot see may reference the symbol. An
application, and a library with a complete and fully loaded consumer set, report it.

### DS1002 unused-unexported

An unexported declaration that is not a member of a type and that no reference names.

### DS1003 unused-member

A struct field no reference names, whose container the analysis did not also find dead. A member
of a dead container falls with the container and is reported inside the container's dead
component rather than on its own.

### DS1004 test-only-use

A declaration no production file references and at least one test file does. It is the
unused-exported or unused-unexported answer for a symbol whose only callers are tests, reported
once under this code, because the symbol and its tests are deleted in one change.

### DS1005 test-of-dead-code

A test declaration whose set of referenced target production declarations is not empty and every
member of which is reported dead. A test that references one live target declaration is never
reported, and the message says so. A reported test joins the dead component of the declarations
it references, so the report names the test beside the code it exercises.

### DS1006 deprecated-unused

A declaration carrying a `Deprecated:` paragraph in its doc comment that no production file
references, which is the residue of a finished migration. It is reported under this code rather
than under the unused-exported, unused-unexported or unused-member code.

## DS1100 to DS1199: visibility narrowing

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1101` | `unnecessary-export` | on | `warn` | `narrowable` |
| `DS1102` | `unnecessary-exposure` | on | `warn` | `narrowable` |
| `DS1103` | `unreachable-export` | on | `deny` | `deletable` |

A narrowing is reported only where every reference that can exist is one the run loaded. A
`main` package, an external test package and a package under an `internal` tree are closed
whatever the run knows about consumers. A published package is closed only where the
configuration declares the consumer set complete and every declared consumer loaded. So a
library with no consumer information receives narrowing findings over its `internal` tree and
its `main` packages and none on its published API.

A declared cross-language edge counts as a reference from outside the symbol's own package, so a
narrowing finding on a symbol an edge names is published as a pending evaluation rather than as
a finding; see [analysis.md](analysis.md).

### DS1101 unnecessary-export

An exported declaration whose every reference is inside the package that declares it. The
finding names the narrower visibility those references support: the file, where every reference
is in the declaring file, and the package otherwise.

### DS1102 unnecessary-exposure

An exported declaration of a package an importer outside the module can name, whose every
reference is inside the target module. The declaration can move behind an `internal` boundary.

### DS1103 unreachable-export

An unused exported declaration of a package nothing outside can import: a `main` package, an
external test package, or a package under an `internal` tree no importer outside its parent
reaches. The claim is about the package graph rather than about the consumer set, so this kind
needs no consumer information and reaches `certain` without any. Its subject may be declared in
a test file, which is what puts an exported declaration of an external test package in this
population and in no other.

## DS1200 to DS1299: interfaces

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1201` | `unused-interface` | on | `deny` | `deletable` |
| `DS1203` | `uncalled-interface-method` | on | `warn` | `manual` |
| `DS1204` | `unused-satisfaction-assertion` | on | `deny` | `deletable` |

### DS1201 unused-interface

An interface declaration no symbol names as a type. The finding names the concrete types that
implement it and where each is written. A satisfaction assertion is itself a use of the
interface, so an interface whose only use is an assertion is live and the assertion is what is
reported, under `DS1204`. The interface's own members are not reported beside it: a member of a
dead container falls with the container.

### DS1203 uncalled-interface-method

A method an interface declares that no call site invokes and no expression selects through the
interface, whatever the number of implementations. A method selected as a value is selected
through the interface and counts as invoked; a call on a value of a concrete type invokes that
type's own method and not this one.

Two shapes are exempt. Every method of an interface whose method set holds an unexported method,
because such a method set exists to fix which types implement the interface rather than to be
called through it. And a marker method, being a method whose every implementation in the target
carries a body with no statement; an implementation the target does not declare is a body this
analysis cannot read, which leaves the method not a marker.

### DS1204 unused-satisfaction-assertion

A compile-time satisfaction assertion, which is a package-level declaration of the blank
identifier whose declared type is an interface of the target and whose value is of a concrete
type, where nothing other than such an assertion names that interface as a type. The methods the
assertion retains stay retained, so the finding names the one declaration a maintainer deletes.
Two assertions of one interface are two findings.

## DS1300 to DS1399: reads and writes

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1301` | `write-only-symbol` | on | `deny` | `deletable` |
| `DS1302` | `unused-enum-member` | on | `deny` | `deletable` |
| `DS1303` | `unused-type-parameter` | on | `deny` | `deletable` |

### DS1301 write-only-symbol

A package-level variable or a struct field the references store into and never read: at least
one write, and no reference of any other kind. The finding names every write position. A
constant cannot be written and is no subject; a variable a function declares is the dead-store
kind's subject instead.

The subject is a live declaration, because a write is a reference and holds the symbol live. The
kind asks whether anything reads the declaration, which is the question the liveness relations do
not ask.

### DS1302 unused-enum-member

A constant of an enumerated type that no reference names. The enumerated type is the one the
`enum-group` exemption recognises, so a type whose values can arrive by conversion retains every
member and no member of it reaches this kind. A member a reference names is not reported
whichever file holds the reference, so a member only a test names is the test-only kind's
subject. This kind takes precedence over the unused-exported and unused-unexported kinds for a
constant of an enumerated type.

### DS1303 unused-type-parameter

A type parameter of a function or a method that no part of the declaration's signature and no
part of its body names. A type parameter of a type declaration is never reported: a phantom
parameter makes two instantiations distinct types while naming the parameter nowhere, so
deleting it changes the program. This kind takes precedence over the unused-exported and
unused-unexported kinds for a type parameter.

## DS1500 to DS1599: non-code artifacts

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1501` | `file-never-built` | on | `deny` | `deletable` |
| `DS1502` | `file-never-imported` | on | `deny` | `deletable` |

### DS1501 file-never-built

A source file of the target that no configuration of the build matrix compiles, naming the build
constraint that excluded it.

The kind reports only where the configuration both lists the build configurations and declares
the matrix complete. A matrix the run derived holds the configurations the tree's own build
atoms imply and cannot claim to be every configuration the target builds, so under a derived
matrix the kind reports nothing and the report says why.

A file the toolchain ignored solely because it imports `"C"` is never reported here: it is
recorded in the report's `excluded_by_cgo` list instead. A file the `cgo` build tag excluded
stays in this population, because that tag is an atom a configuration names.

### DS1502 file-never-imported

A source file of a package no import reaches, that no root names, and whose every declaration
the analysis found dead. A `main` package is never a subject, because the toolchain builds one
whatever imports it, and neither is a package holding a root, which is what leaves a test
package with a test function out.

## DS1600 to DS1699: dependencies and module machinery

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1601` | `unused-dependency` | on | `deny` | `manual` |
| `DS1605` | `unused-module-directive` | on | `warn` | `deletable` |

Both kinds read the module file's own directives through the toolchain, and both report only
where every configuration of the matrix agrees, because a requirement one configuration imports
is used.

### DS1601 unused-dependency

A direct requirement of the module file whose module provides no package any package of the
target imports. A requirement marked indirect is never reported: it carries no import by
construction and pins a transitive version under module graph pruning, so deleting it changes
the build list.

### DS1605 unused-module-directive

A `replace` directive whose replaced module is absent from the build list, which is the one case
in which the directive redirects nothing. The finding names the directive by the replaced
module, which is the identity the toolchain gives a replaced module. An `exclude` directive and
a workspace `use` entry are not reported.

## DS1700 to DS1799: self-check

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1701` | `suppression-without-reason` | on | `deny` | `none` |
| `DS1702` | `unscoped-ignore-entry` | on | `deny` | `none` |
| `DS1703` | `stale-suppression` | fixed on | fixed `deny` | `none` |
| `DS1704` | `unmatched-root` | fixed on | fixed `deny` | `none` |

`DS1703` and `DS1704` both mean a configuration naming something that no longer exists, and both
are fixed: no severity setting reduces either below a finding, and a configuration naming
`DS1703`, `DS1704` or the family prefix `DS17` under a severity key is an unimplemented key and
exits with the usage code.

### DS1701 suppression-without-reason

Every suppression the grammar refused for carrying no reason, at each of the three suppression
mechanisms. A refusal binds nothing, so the finding the suppression was meant to cover stays
reported beside this one. A directive naming several codes and carrying no reason is one finding
per code, at the one position the directive is written on.

### DS1702 unscoped-ignore-entry

Every ignore-file entry and baseline row the grammar refused for naming a symbol and no path,
which is an instruction that would mask a match anywhere in the project. An entry lacking both
its reason and its path is reported here and under `DS1701` at one position, because the two
rules are separate checks over one record.

### DS1703 stale-suppression

Every suppression that matched no current finding, at either mechanism and in the baseline
alike. A record is stale when it withheld nothing: it bound no declaration, or it bound one and
the finding its code names was never held back, because a record in effect for nothing is a claim
nobody checks. A record naming a retired code is stale whatever it bound, since nothing can match
it.

A record bound to a symbol whose code the configuration silences is dormant instead: neither in
effect nor stale, and reported by nothing. Both ways of silencing count, the severity set to
`allow` and a confidence the configured minimum excludes, so turning a kind off never fails the
run over the adjudications turning it back on would need.

Several stale records at one site are one finding naming every code, because one directive above
a line that declares several symbols is one record per symbol and a maintainer edits one line.

### DS1704 unmatched-root

Every configured root and every configured root pattern that named no symbol of the inventory.
The subject is the configured string as the configuration spells it, pattern and all, because
that is the string a maintainer corrects. A pattern that matches under one build configuration
and not another is no finding: the row is reported once per run and never once per
configuration.

## DS1800 to DS1899: intra-function

| Code | Kind | Default | Severity | Fixability | Overlaps |
| --- | --- | --- | --- | --- | --- |
| `DS1801` | `unused-parameter` | on | `warn` | `manual` | `revive unused-parameter`, `gopls unusedparams`, `unparam` |
| `DS1802` | `unused-receiver` | on | `warn` | `deletable` | `revive unused-receiver` |
| `DS1803` | `unused-result` | on | `warn` | `manual` | `unparam` |
| `DS1805` | `unreachable-statement` | on | `deny` | `deletable` | `go vet unreachable` |
| `DS1807` | `dead-store` | on | `deny` | `deletable` | `ineffassign`, `wastedassign`, `staticcheck SA4006` |
| `DS1809` | `unreachable-case` | on | `deny` | `deletable` | `staticcheck SA4020` |

The six kinds are one family behind one switch, and `"DS18": "allow"` in the severity section
turns off all of them. `Overlaps` is the vocabulary's own list of external rules that report the
same kind, carried on every finding, so a project already running one of them silences whichever
side it prefers.

Three of the six edit a signature, and each reports only where the signature is free to change.
A signature is not free when an exemption class retained the declaration, when the function is
used as a value rather than called, when the linker or a foreign caller names it, or when the body
is a stub that is empty or only panics.

A published declaration of a library is free whatever the run knows about the library's consumers:
a parameter or a receiver a body never names is one no caller can make it read. The fixability of
each kind says what the edit costs on such a declaration.

### DS1801 unused-parameter

A named, non-blank parameter of a function or method whose body names it nowhere, on a function
whose signature is free. A parameter declared with the blank identifier, and one the signature
leaves unnamed, name nothing and are no subject. A parameter used under one build configuration
is used, so a configuration whose constraint excludes the body that reads it reports nothing.

### DS1802 unused-receiver

A named, non-blank method receiver the method body names nowhere, under the same free-signature
rule. The fix deletes an identifier rather than changing a signature, because Go permits a
method with no receiver name, which is why this kind is `deletable` where the parameter kind is
`manual`.

### DS1803 unused-result

A result of a function every call site discards, on a function whose signature is free and every
call site of which is in the loaded graph. An unknown caller may consume a result, so this kind
alone carries the closed-world precondition beside the free-signature rule, and a function with no
call site at all is an unused-declaration kind's subject instead.

A call whose result the source discards is a call in statement position, a call under `go` or
`defer`, and a call whose value is assigned to the blank identifier at that result's own index.
Every other context uses the result, a multi-value call passed straight to another call
included.

### DS1805 unreachable-statement

A statement control flow cannot reach. The rule is the toolchain's own unreachable pass, run
over each loaded package's syntax and type information: the finding's position is the
diagnostic's own and its subject is the declaration the statement is written in.

### DS1807 dead-store

A write to a local variable no read reaches, decided by a liveness walk over the control-flow
graph of one function body: no path from the store reaches a read before the next store to the
same variable or the end of the body.

The subject is a variable the body declares. A parameter, a result and a receiver are not
subjects, which keeps a named result a deferred function assigns out of the population, and
neither is a package-level variable or a field, which `DS1301` answers for. Four constructs take
a variable out of the population, each because a store to it may be read where the graph cannot
see: its address is taken, a function literal of the body names it, a method with a pointer
receiver is selected on it, and a selector or an index on it is assigned to. A call is assumed
to return, apart from a call to the `panic` built-in.

### DS1809 unreachable-case

A case clause of a type switch that can never match because an earlier clause of the same switch
always matches first. One clause subsumes a later one when the earlier names an interface every
value the later names implements, which covers an interface ahead of a concrete type, an
interface ahead of a wider interface, and two structurally identical interfaces. A clause naming
`nil` and the default clause are always reachable and are no subject, and a type parameter names
no method set to compare. A switch over values has no population, because the language refuses a
duplicated constant case at compile time.

## Kinds this analyzer does not report

| Code | Kind | Why |
| --- | --- | --- |
| `DS1104` | `redundant-export-keyword` | The kind applies to TypeScript and JavaScript, which this analyzer does not read. |
| `DS1705` | `stale-cross-language-edge` | An edge has two sides and this analyzer evaluates one. It publishes its side as an edge evaluation and the orchestrator's merge reports the code. |

The gaps this analyzer declares against the conformance corpus are in
[conformance.md](conformance.md).

## Retired codes

A code names at most one kind for the life of the code space, so a retired code stays retired and
is never reused. These are the retired codes of the contract this analyzer implements, by code
and name.

| Code | Name |
| --- | --- |
| `DS1202` | `single-implementation-interface` |
| `DS1401` | `alias-only-declaration` |
| `DS1402` | `forwarding-only-symbol` |
| `DS1503` | `duplicate-export` |
| `DS1504` | `import-cycle` |
| `DS1505` | `orphan-test-data` |
| `DS1506` | `unread-embed-pattern` |
| `DS1507` | `unused-message-key` |
| `DS1602` | `undeclared-dependency` |
| `DS1603` | `test-only-dependency` |
| `DS1604` | `unresolved-import` |
| `DS1606` | `unused-tool-directive` |
| `DS1607` | `unused-workspace-entry` |
| `DS1608` | `unused-binary` |
| `DS1804` | `constant-result` |
| `DS1806` | `discarded-pure-result` |
| `DS1808` | `write-to-copy` |
| `DS1810` | `empty-branch` |
| `DS1811` | `redundant-conversion` |

The `DS1400` to `DS1499` range is retired as a whole. A suppression naming a retired code matches
nothing and is reported as `DS1703`.

# Issue kinds

deadset-go reports 29 of the 33 issue kinds the deadset contract declares. This page states, per code, what this analyzer reports under it and what it treats as a use, so a reader can tell why a symbol was reported and why a symbol was not. The vocabulary itself, the code space and the fields a finding carries are stated once in the contract's [issue kinds page](https://github.com/cplieger/deadset-spec/blob/v8.0.0/docs/kinds.md), which this page does not restate.

Every kind is enabled by default and every kind declares the confidence ceiling `certain`. `Severity` is what a finding does to the run. A `deny` finding fails it and a `warn` finding does not. `Fixability` is what a mechanical edit may do with the finding. A `deletable` finding's declaration can be removed, a `narrowable` one's visibility reduced, a `manual` one is for a maintainer to decide, and nothing mechanical acts on a `none` one. A configuration changes a severity by code or by two-digit family prefix, as [Configuration and invocation](configuration.md#settings) describes.

The four codes of the contract it does not report have no section here and are listed under [Kinds this analyzer does not report](#kinds-this-analyzer-does-not-report).

## What counts as a use

Every kind below rests on one reference set, built once per build configuration from the type-checked program. A reference is recorded where the type checker resolves an identifier to the declaration it denotes. So a name in a comment, a string or a build directive is no reference, and a text search plays no part.

The position the identifier is written in classifies each reference as a read, a write, a call, a type use, a conversion, an embedding or a type assertion. The classification decides the read-and-write kinds and nothing else. Every other kind asks only whether a reference exists.

Nine rules of that classification answer most questions about a symbol the analyzer did not report:

- The defining identifier of a declaration is no reference to it, so a symbol named only at its own declaration site is unreferenced.
- A call a function makes to itself is a reference, so a recursive function is referenced.
- A selector resolves to the field or method it selects rather than to the enclosing type, and a selector through an embedded field records a read of each field on the path.
- The left side of an assignment, the operand of `++`, `--` or a compound assignment, a field key in a composite literal, an index or map assignment and a `delete` are writes. Everything else is a read.
- A store through a struct field holding a map, a slice or a pointer reads the field, because the store lands in shared storage. Through a field holding an array or a struct, it writes the field.
- A struct value used as an operand of `==` or `!=`, as a map key, or as a switch tag or case expression reads every field of that type. It also reads every field of every struct field beneath it, because equality reads them all. A key handed to a method of `sync.Map` or to `context.WithValue`, and the old value a compare-and-swap or compare-and-delete compares, is read the same way.
- `x = append(x, v)` reads nothing of `x`, because the read inside the call is the mechanics of the store. `y = append(x, v)` reads `x`.
- The operand of `&` is a read, and `*p = v` reads `p`, because the write lands on the pointee rather than on the symbol.
- A conversion between a pointer and `unsafe.Pointer` reads every field the pointed-to type lays out, through nested structs and arrays. A struct with a `structs.HostLayout` field reads all its fields the same way.

A reference from a test file is a test reference, which is what separates the test-only kind from the unused kinds. A reference from a declared consumer the run loaded is an ordinary reference. A reference from a consumer's test files is a test reference unless the configuration counts consumer tests as production.

Beyond the reference set, a symbol is held live by a root or by an exemption class. The root set is printable with `print-roots` and the retained set with `print-retained`. The classes are in [Exemption classes](exemptions.md) and the two liveness relations in [How the analysis decides](analysis.md).

## DS1000 to DS1099: unused

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1001` | `unused-exported` | on | `deny` | `deletable` |
| `DS1002` | `unused-unexported` | on | `deny` | `deletable` |
| `DS1003` | `unused-member` | on | `deny` | `deletable` |
| `DS1005` | `unused-enum-member` | on | `deny` | `deletable` |
| `DS1006` | `unused-type-parameter` | on | `deny` | `deletable` |

### DS1001 unused-exported

An exported declaration that is not a member of a type and that no reference names, in the target or in any loaded consumer.

The kind reports nothing for a library whose published API is open world, because a caller the analysis cannot see may reference the symbol. The published API is open world when the run holds no consumer information, or when a consumer the scope document declared did not load. An application reports it, and so does a library whose declared consumers all loaded or whose configuration declares the consumer set complete.

### DS1002 unused-unexported

An unexported declaration that is not a member of a type and that no reference names.

A constant or a type a function body declares, a function literal's body included, is reported here when nothing in that body names it outside its own declaration. Such a declaration has no reference of its own, so the finding carries the enclosing declaration's reference and the local declaration's own position.

### DS1003 unused-member

A struct field no reference names, whose container the analysis did not also find dead. A member of a dead container falls with the container and is reported inside the container's dead component rather than on its own.

### DS1005 unused-enum-member

A constant of an enumerated type that no reference names. The enumerated type is the one the `enum-group` exemption recognises, so a type whose values can arrive by conversion retains every member and no member of it reaches this kind. A member a reference names is not reported whichever file holds the reference, so a member only a test names is the test-only kind's subject. This kind takes precedence over the unused-exported and unused-unexported kinds for a constant of an enumerated type.

### DS1006 unused-type-parameter

A type parameter of a function or a method that no part of the declaration's signature and no part of its body names. A type parameter of a type declaration is never reported, because a phantom parameter makes two instantiations distinct types while naming the parameter nowhere, so deleting it changes the program. This kind takes precedence over the unused-exported and unused-unexported kinds for a type parameter.

## DS1100 to DS1199: only dead code or tests use it

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1101` | `unreachable-export` | on | `deny` | `deletable` |
| `DS1102` | `test-only-use` | on | `deny` | `deletable` |
| `DS1103` | `test-of-dead-code` | on | `deny` | `deletable` |
| `DS1104` | `deprecated-unused` | on | `deny` | `deletable` |

### DS1101 unreachable-export

An unused exported declaration of a package nothing outside can import, which is a `main` package, an external test package, or a package under an `internal` tree no importer outside its parent reaches. The claim is about the package graph rather than about the consumer set, so this kind needs no consumer information and reaches `certain` without any. Its subject may be declared in a test file, which is what puts an exported declaration of an external test package, and an exported test-file declaration nothing reaches, in this population and in no other.

### DS1102 test-only-use

A declaration no production file references and at least one test file does, or one that only dead code references in production and a test of live code references. It is the unused-exported or unused-unexported answer for a symbol whose only callers are tests, reported once under this code, because the symbol and its tests are deleted in one change. An interface method only test code calls through the interface is reported here too, unless the `DS1202` exemptions cover it, and the methods implementing it stay live while the test reaches them through it.

Test-support code is a package only tests reach, through its own test files or through other such packages, one of whose files other than its test files imports `testing` or a package of a test library: `github.com/google/go-cmp`, `github.com/onsi/ginkgo`, `github.com/onsi/gomega`, `github.com/stretchr/testify` or `gotest.tools`. A package only tests reach that imports none of them is production code a test uses. A declaration of it that a test references is reported at confidence `possible`, under this code or the one its shape selects. So is every dead declaration it reaches, every test of dead code that tests only such declarations, and every dead declaration such a test reaches, its own file's helpers included. The package may be a test helper with no such import, which only its maintainer can tell, so the default `analysis.min_confidence` withholds these findings. Each is withheld on its own: a dead declaration that shares a dead callee with them keeps its own confidence. A declaration of the package that nothing references stays `certain`. In a library, only a package under an `internal` tree can be test-support code. A package holding a root other than an `init`, `blank` or `test` root never is. Test-support code is test code, so a declaration of it that a reference names is never reported here. One that nothing references is reported under the code its shape selects.

### DS1103 test-of-dead-code

A test declaration whose set of referenced target production declarations is not empty and every member of which is reported dead. A test that references one live target declaration is never reported, and the message says so. A reported test joins the dead component of the declarations it references, so the report names the test beside the code it exercises. A test-file declaration nothing reaches that is no such test is reported under the code a production declaration of its shape carries.

Test-support code that a test of live code also references counts as live for every test that references it.

### DS1104 deprecated-unused

A declaration carrying a `Deprecated:` paragraph in its doc comment that no production file references, which is the residue of a finished migration. It is reported under this code rather than under the unused-exported, unused-unexported or unused-member code.

## DS1200 to DS1299: interfaces

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1201` | `unused-interface` | on | `deny` | `deletable` |
| `DS1202` | `uncalled-interface-method` | on | `warn` | `manual` |
| `DS1203` | `unused-satisfaction-assertion` | on | `deny` | `deletable` |

### DS1201 unused-interface

An interface declaration no symbol names as a type. The finding names the concrete types that implement it and where each is written. A satisfaction assertion is itself a use of the interface, so an interface whose only use is an assertion is live and the assertion is what is reported, under `DS1203`. The interface's own members are not reported beside it, because a member of a dead container falls with the container.

### DS1202 uncalled-interface-method

A method an interface declares that no call site of production code invokes and no expression of it selects through the interface, whatever the number of implementations. A method only test code calls through the interface is `DS1102`'s. A method selected as a value is selected through the interface and counts as invoked. A call on a value of a concrete type invokes that type's own method and not this one. Each method implementing it that nothing else keeps is reported under the code its own references select, as a member of this finding's component.

Two shapes are exempt. Every method of an exported interface whose method set holds an unexported method, a method of an interface it embeds included, because such a method set exists to fix which types outside the package implement the interface rather than to be called through it. An unexported interface restricts no implementor outside its package, so a method only it holds is judged. And a marker method, being a method whose every implementation in the target carries a body with no statement. An implementation the target does not declare is a body this analysis cannot read, which leaves the method not a marker.

### DS1203 unused-satisfaction-assertion

A compile-time satisfaction assertion is a package-level declaration of the blank identifier whose declared type is an interface of the target and whose value is of a concrete type. The kind reports one where nothing other than such an assertion names that interface as a type. The methods the assertion retains stay retained, so the finding names the one declaration a maintainer deletes. Two assertions of one interface are two findings. In a library, an assertion whose interface and concrete type are both published API is not reported, because it states to every importer a promise of that API.

## DS1300 to DS1399: data nobody reads

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1301` | `write-only-symbol` | on | `deny` | `deletable` |

### DS1301 write-only-symbol

A package-level variable or a struct field the references store into and never read, which means at least one write and no reference of any other kind. The finding names every write position. Its message says when only test files read the subject, and names any type parameter, import or declaration the deletion also removes. Such a declaration is one whose every use is in a reported subject's type or writes, and no finding is reported about it. A constant cannot be written and is no subject. A variable a function declares is the dead-store kind's subject instead.

The subject is a live declaration, because a write is a reference and holds the symbol live. The kind asks whether anything reads the declaration, which is the question the liveness relations do not ask. A loaded consumer's write counts as a read. A read from a declaration nothing references, found dead at `certain`, is no read, so a variable live code writes and only such a declaration reads is reported. A type argument of a type parameter constrained by `comparable` has every field read, as a comparison of it does.

## DS1400 to DS1499: inside functions

| Code | Kind | Default | Severity | Fixability | Overlaps |
| --- | --- | --- | --- | --- | --- |
| `DS1401` | `unused-parameter` | on | `warn` | `manual` | `revive unused-parameter`, `gopls unusedparams`, `unparam` |
| `DS1402` | `unused-receiver` | on | `warn` | `deletable` | `revive unused-receiver` |
| `DS1403` | `unused-result` | on | `warn` | `manual` | `unparam` |
| `DS1404` | `dead-store` | on | `deny` | `deletable` | `ineffassign`, `wastedassign`, `staticcheck SA4006` |
| `DS1405` | `unreachable-statement` | on | `deny` | `deletable` | `go vet unreachable` |
| `DS1406` | `unreachable-case` | on | `deny` | `deletable` | `staticcheck SA4020` |

The six kinds are one family behind one switch, and `"DS14": "allow"` in the severity section turns off all of them. `Overlaps` is the vocabulary's own list of external rules that report the same kind, carried on every finding, so a project already running one of them silences whichever side it prefers.

Two of the six, `DS1401` and `DS1403`, edit a signature, and each reports only where the signature is free to change. A signature is not free in seven cases:

- An exemption class retained the declaration, on evidence in any file, a test file included.
- The function is used as a value rather than called.
- The method has the name and signature of a method of an exported interface declared outside the target.
- The linker or a foreign caller names it.
- The test driver runs it as a test, a benchmark, a fuzz test or `TestMain`.
- A type error roots it, because the code the error left unresolved may fix the signature.
- The body is a stub that is empty or only panics.

A fuzz target passed to `Fuzz` is used as a value.

A published declaration of a library is free whatever the run knows about the library's consumers, because a parameter a body never names is one no caller can make it read. The fixability of each kind says what the edit costs on such a declaration.

### DS1401 unused-parameter

A named, non-blank parameter of a function or method whose body names it nowhere, on a function whose signature is free. A parameter declared with the blank identifier, and one the signature leaves unnamed, name nothing and are no subject. A parameter used under one build configuration is used, so a configuration whose constraint excludes the body that reads it reports nothing.

A function literal that initializes a local variable has a free signature when every use of the variable calls it, and so does a literal that is the callee of the call it is written in, under `go` or `defer` or in place. Its unread parameters are then reported too.

### DS1402 unused-receiver

A named, non-blank method receiver the method body names nowhere, whatever fixes the method's signature. The fix deletes an identifier rather than changing a signature, because Go permits a method with no receiver name, which is why this kind is `deletable` where the parameter kind is `manual`.

### DS1403 unused-result

A result of a function every call site discards, on a function whose signature is free and every call site of which is in the loaded graph. An unknown caller may consume a result, so this kind alone carries the closed-world precondition beside the free-signature rule, and a function a program run on its own names has callers outside the graph, and a function with no call site at all is an unused-declaration kind's subject instead.

A call whose result the source discards is a call in statement position, a call under `go` or `defer`, and a call whose value is assigned to the blank identifier at that result's own index. Every other context uses the result, a multi-value call passed straight to another call included.

### DS1404 dead-store

A write to a local variable no read reaches, decided by a liveness walk over the control-flow graph of one function body. No path from the store reaches a read before the next store to the same variable or the end of the body. Each function literal is a body of its own, a literal of a package-level variable's initializer included, and its findings carry that variable's reference, or the package's for a blank variable. A range statement stores its key and value at the start of every iteration.

The subject is a variable the body or its signature declares, so a parameter, a named result and a receiver are subjects too. A return naming no value reads every named result. A package-level variable and a field are not subjects, which `DS1301` answers for.

Two constructs take a variable out of the population, each because a store to it may be read where the graph cannot see. Its address is taken, or a method with a pointer receiver is selected on it while it is not a pointer and no embedded pointer field carries the selection. A selector or an index on it that is assigned to reads it. A function literal reads every variable it names where it is written, and a store a literal naming the variable may precede on some path, a literal of an earlier loop iteration included, is not reported, because a call of the literal may read it. A call is assumed to return, apart from a call to the `panic` built-in.

### DS1405 unreachable-statement

A statement control flow cannot reach. The rule is the toolchain's own unreachable pass, run over each loaded package's syntax and type information. The finding's position is the diagnostic's own and its subject is the declaration the statement is written in.

### DS1406 unreachable-case

A case clause of a type switch that can never match because an earlier clause of the same switch always matches first. One clause subsumes a later one when the earlier names an interface every value the later names implements. That covers an interface ahead of a concrete type, an interface ahead of a wider interface, and two structurally identical interfaces.

A clause naming `nil` and the default clause are always reachable and are no subject, and a type parameter names no method set to compare. A switch over values has no population, because the language refuses a duplicated constant case at compile time.

## DS1500 to DS1599: too visible

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1501` | `unnecessary-export` | on | `warn` | `narrowable` |
| `DS1502` | `unnecessary-exposure` | on | `warn` | `narrowable` |

A narrowing is reported only where every reference that can exist is one the run loaded. A `main` package, an external test package and a package under an `internal` tree are closed whatever the run knows about consumers. A published package is closed only where the configuration declares the consumer set complete and every declared consumer loaded. So a library with no consumer information receives narrowing findings over its `internal` tree and its `main` packages and none on its published API.

A method that matches a method of an exported interface declared outside the target is never narrowed. That interface fixes its name.

A declared cross-language edge counts as a reference from outside the symbol's own package. So a narrowing finding on a symbol an edge names is published as a pending evaluation rather than as a finding, as [How the analysis decides](analysis.md#cross-language-edges) describes.

### DS1501 unnecessary-export

An exported declaration whose every reference is inside the package that declares it. The finding names the narrower visibility those references support, which is the file where every reference is in the declaring file, and the package otherwise. A type in a parameter or result of an exported function or method stays unreported while code outside the package references that function or method.

### DS1502 unnecessary-exposure

An exported declaration of a package an importer outside the module can name, whose every reference is inside the target module. The declaration can move behind an `internal` boundary. A type in a parameter or result of an exported function or method stays unreported while code outside the module references that function or method.

## DS1600 to DS1699: files

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1601` | `file-never-built` | on | `deny` | `deletable` |
| `DS1602` | `file-never-imported` | on | `deny` | `deletable` |

### DS1601 file-never-built

A source file of the target that no configuration of the build matrix compiles, naming the build constraint that excluded it.

The kind reports only where the configuration both lists the build configurations and declares the matrix complete. A matrix the run derived holds the configurations the tree's own build atoms imply, and it cannot claim to be every configuration the target builds. So under a derived matrix the kind reports nothing, and the report says why.

A file the toolchain ignored solely because it imports `"C"` is never reported here. It is recorded in the report's `excluded_by_cgo` list instead. A file the `cgo` build tag excluded stays in this population, because that tag is an atom a configuration names.

### DS1602 file-never-imported

A source file of a package no import reaches, that no root names, and whose every declaration the analysis found dead, a package whose files declare nothing included. A `main` package is never a subject, because the toolchain builds one whatever imports it, and neither is a package holding a root, which is what leaves a test package with a test function out.

## DS1700 to DS1799: dependencies and module files

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS1701` | `unused-dependency` | on | `deny` | `manual` |
| `DS1702` | `unused-module-directive` | on | `warn` | `deletable` |

Both kinds read the module file's own directives through the toolchain, and both report only where every configuration of the matrix agrees, because a requirement one configuration imports is used.

### DS1701 unused-dependency

A direct requirement of the module file whose module provides no package any package of the target imports. A requirement marked indirect is never reported, because it carries no import by construction and pins a transitive version under module graph pruning, so deleting it changes the build list.

### DS1702 unused-module-directive

A `replace` directive whose replaced module is absent from the build list, which is the one case in which the directive redirects nothing. The finding names the directive by the replaced module, which is the identity the toolchain gives a replaced module. An `exclude` directive and a workspace `use` entry are not reported.

## DS9000 to DS9099: deadset configuration

| Code | Kind | Default | Severity | Fixability |
| --- | --- | --- | --- | --- |
| `DS9001` | `suppression-without-reason` | on | `deny` | `none` |
| `DS9002` | `unscoped-ignore-entry` | on | `deny` | `none` |
| `DS9003` | `stale-suppression` | fixed on | fixed `deny` | `none` |
| `DS9004` | `unmatched-root` | fixed on | fixed `deny` | `none` |

`DS9003` and `DS9004` both mean a configuration naming something that no longer exists, and both are fixed. No severity setting reduces either below a finding, and a configuration naming `DS9003`, `DS9004` or the family prefix `DS90` under a severity key is an unimplemented key and exits with the usage code.

### DS9001 suppression-without-reason

Every suppression the grammar refused for carrying no reason, at each of the three suppression mechanisms. A refusal binds nothing, so the finding the suppression was meant to cover stays reported beside this one. A directive naming several codes and carrying no reason is one finding per code, at the one position the directive is written on.

### DS9002 unscoped-ignore-entry

Every ignore-file entry and baseline row the grammar refused for naming a symbol and no path, which is an instruction that would mask a match anywhere in the project. An entry lacking both its reason and its path is reported here and under `DS9001` at one position, because the two rules are separate checks over one record.

### DS9003 stale-suppression

Every suppression that matched no current finding, at either mechanism and in the baseline alike. A record is stale when it withheld nothing. Either it bound no declaration, or it bound one and the finding its code names was never held back. A record in effect for nothing is a claim nobody checks. A record naming a retired code is stale whatever it bound, since nothing can match it.

A record bound to a symbol whose code the configuration silences is dormant instead. It is neither in effect nor stale, and nothing reports it. Both ways of silencing count, the severity set to `allow` and a confidence the configured minimum excludes. So does either of them withholding a root of the component the record's finding falls in. As a result, turning a kind off never fails the run over the adjudications turning it back on would need.

Several stale records at one site are one finding naming every code, because one directive above a line that declares several symbols is one record per symbol and a maintainer edits one line.

### DS9004 unmatched-root

Every configured root and every configured root pattern that named no symbol of the inventory. The subject is the configured string as the configuration spells it, pattern and all, because that is the string a maintainer corrects. A pattern that matches under one build configuration and not another is no finding, because the row is reported once per run and never once per configuration.

## Kinds this analyzer does not report

| Code | Kind | Why |
| --- | --- | --- |
| `DS1004` | `unused-import` | The kind is a TypeScript import binding nothing reads. A Go file that imports a package it never uses does not compile, so no Go program has one. |
| `DS1503` | `redundant-export-keyword` | The kind applies to TypeScript and JavaScript, which this analyzer does not read. |
| `DS9005` | `stale-cross-language-edge` | An edge has two sides and this analyzer evaluates one. It publishes its side as an edge evaluation and the orchestrator's merge reports the code. |
| `DS9006` | `unmatched-configured-declaration` | The configuration keys whose entries name a declaration are the TypeScript analyzer's, and no key of the `go` section names one. |

The gaps this analyzer declares against the conformance corpus are in [conformance.md](conformance.md).

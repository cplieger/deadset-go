# Exemption classes

An exemption is a named reason for which deadset-go keeps a symbol the reference graph alone would report. Every class is computed from Go type information rather than configured, so a project writes no suppression for a symbol a class covers. deadset-go implements the nine classes the deadset contract declares for Go. The class vocabulary and its detection rules are stated once in the contract's [exemptions page](https://github.com/cplieger/deadset-spec/blob/v6.1.1/docs/exemptions.md), and this page states what each class does here.

## Reading the retained set

`deadset-go print-retained` lists every symbol an exemption held back, with the class that held it. Each entry also carries one clause of detail naming the relation and the thing it relates to, and the source site the evidence was found at. That answers why a symbol was not reported. If the symbol is in the retained set, the class named beside it is the reason.

Every finding a report carries names in `retained_by` the classes that retained its subject, where any did.

Three rules decide what the retained set holds:

- A symbol is in it only when an exemption is what kept the symbol from being reported. An exemption on a symbol something references anyway is computed and not listed, because the reference is the reason it is live.
- A symbol an exempt symbol keeps alive is in neither. The exemption seeds the reachability closure, so the private helper of a retained method is live rather than held back.
- One record per symbol, class, detail and holding declaration, at its first site by rendered position. A method one function converts to one interface at sixty sites is one record.

## Turning a class off

`exemptions.disabled` in the configuration lists class names that do not run, so an exemption suspected of hiding a defect can be tested. A disabled class retains nothing.

Any class of the contract's vocabulary may be named, so a configuration shared with the TypeScript analyzer works unchanged, and a class the vocabulary declares for TypeScript alone switches nothing off here. A name outside the vocabulary is refused. `analyze`, `explain` and `print-retained` exit with the usage code and list the classes, and `print-config` prints the list as written.

Two classes read files and retain nothing until the project configures them: `analysis.template_dirs` for `template-field`, and nothing at all for `reflective-lookup`, which reads only the program's own string literals. A configured template directory the target does not hold ends the run with the usage code rather than being passed over, the same way a configured root that matches nothing is reported rather than ignored.

## Evidence in a test file does not hold for production

A report is built from a production analysis, which counts no reference a test file made. An exemption whose evidence is in a test file does not hold there either. A test that marshals a value or compares one makes no member of that value live for production. Such evidence counts as a test reference, so a member nothing in production references is reported as `DS1004`. `print-retained --mode=plain` counts every reference and every piece of evidence, which is the wider set, and a symbol the report names may be held back in it.

## What leaves the analysis is fully reachable

A struct value or a pointer to one, handed to a parameter typed as the empty interface of a function or method outside the analyzed program, has left the analysis. So has a slice, an array or a map of such values. The callee's body is not in the program, and the parameter's type keeps nothing of the value. So the callee may read its fields and call the methods its own package can name.

So has such a value returned as an interface-typed result by a method that implements an exported interface declared outside the program. That interface's package then stands for the callee's package.

The value flows into `encoding-reflection`, recorded at the call or the return. It keeps its fields and the encoding and decoding methods of both directions.

It also keeps every method by which it satisfies an interface the callee's package declares, exported or not, or writes as an interface type literal. An exported interface of a package in the callee's import closure counts too. So does an exported interface of an outside package of the program's import closure that imports the callee's package. Where the callee's package's imports include a template engine, the value keeps every exported method.

A standard container hands the value back to the program unchanged. A value given to a method of `sync.Map`, to a method of `atomic.Value` or to `context.WithValue` keeps no field. It keeps only the methods by which it satisfies an interface the container's own package declares or writes as a literal.

A callee whose body only decodes into the parameter is a decoding destination rather than this rule's. Its body is read from the build list's source for that question alone.

A function of the program that hands one of its own empty-interface parameters on inherits the crossing at that parameter, to a fixpoint. So a wrapper of a wrapper carries the rule of the call it forwards to. A declared consumer is inside the program, so a consumer's own wrapper is walked like the target's and a target value handed to it reaches the destination through it.

The empty interface is the one parameter type this rule reads. A value handed to any other interface is a conversion the conversion set records, and `interface-satisfaction` retains the methods that interface requires, which is everything the callee can reach through the parameter's own type.

## The classes

### interface-satisfaction

Retains every method that satisfies an interface a value of the method's receiver type reaches.

The evidence is the conversion set, every site in the target or in a loaded consumer where a value of a concrete type reaches a position typed as an interface. Such a site is an explicit conversion or a satisfaction assertion, an assignment, an argument, a return value, or an element stored in an interface-typed container. For each such pair of concrete type and interface, the methods of the type that answer what the interface requires are retained at that site.

A method answering an interface method no call site invokes is not retained, and `DS1203` reports it with that interface method. Each retention is a use by the declaration that holds the site, so it holds only while that declaration is live. A satisfaction assertion is a use by the type it asserts, so it keeps nothing of a type only dead code builds.

A value converted to a defined interface that a package outside the program declares is held by that package, which may assert another interface on it. So the value also keeps the methods by which it, or a pointer to it, satisfies an interface that package's source asserts. An asserted interface is one a type assertion or a type switch case names, whichever package declares it. An interface that package declares and never asserts keeps nothing.

A type whose values never reach an interface retains nothing whatever it happens to implement, because no caller can dispatch to it through an interface the program never builds.

A method satisfying two interfaces at two sites is retained twice, once per site, so the retained set shows every conversion that depends on the method. A type registered as a flag value, or used as a writer, a round tripper or a sort interface, is an ordinary member of the conversion set.

### encoding-reflection

Retains, on a type whose values reach a consumer that names members by string at run time, the members that consumer reads. Those are the exported fields, every field carrying a struct tag, and the methods the consumer resolves by name.

[The encoding-reflection class](encoding-reflection.md) lists its destinations, what each retains and how a value reaches the types it holds.

### format-verb-contract

Retains the `String() string` and `Error() string` methods of a type whose values reach a facility that formats its operands with the `fmt` machinery, because the facility calls them through an interface and no static reference exists.

The facilities are:

- The print, format and error-construction functions of `fmt`.
- The print, fatal and panic functions of `log`, and the same methods of a `log.Logger`.
- The log, error, fatal and skip methods of a testing type.
- The logging functions of `log/slog`, with the methods of its logger and its attribute constructors.
- The functions of the analyzed program that forward their own variadic operands to any of those.

Three rules narrow it. A format string the call computes rather than writes is read as binding every operand, which retains more than the call can reach and never less. A method is retained only in the form a verb calls, taking no argument and returning one string, so a method of another shape that shares the name is not. And the reach stops at an interface-typed field and at an unexported field, which the machinery prints without calling a method.

An operand reaches through a pointer, a slice, an array and a map, because the machinery asks each element for its string in turn. It also reaches through every exported or embedded field, of a defined struct type or of a struct type with no name, at any depth.

### errors-duck-typing

Retains the methods the standard error helpers reach by duck typing, on every type a value of which reaches a position typed as `error`. Such a method's name and signature match `Is(error) bool`, `As(any) bool`, `Unwrap() error` or `Unwrap() []error`.

The signature decides as much as the name. A method named `Is` whose parameter is not `error` implements the program's own comparison rather than the helper's contract, `errors.Is` never calls it, and it is not retained. The interface reached must be `error` itself rather than an interface that embeds it, so a type the program only ever uses through an interface of its own retains nothing here.

### enum-group

Retains every member of an enumerated type whose values can arrive by conversion rather than by name, so a member reached only by value is never reported.

The enumerated type is a defined type whose constants are declared in an `iota` group. An `iota` group is a `const` block in which at least one specification's value expression mentions `iota`, the specifications that repeat the previous expression included. The class fires in any of three cases:

- The type declares a `String`, `MarshalText`, `UnmarshalText`, `MarshalJSON` or `UnmarshalJSON` method.
- A value of it is produced by a conversion whose operand is a non-constant integer.
- A value of it is the target of a decoder.

Every constant of a type that fires is retained, one declared outside the `iota` group included, because a value that arrives by conversion can equal any of them. A conversion method is matched by name alone. A constant declared with the blank identifier names no member and is retained by nothing.

### generated-file

Retains every declaration written in a generated file, which is a file carrying the standard Go generated-code header. The generator owns the file, so a deletion there is undone the next time it runs and the declaration to remove is in the generator's input.

Under `analysis.generated_files` set to `include` the class retains nothing and every declaration of a generated file is judged like any other. The site is the package clause of the generated file.

### linkname-cgo-asm-plugin

Retains a symbol another compilation unit reaches by a name the type checker never records, which is two mechanisms:

- The declaration a `TEXT` directive of an assembly file of the same package names, which is a line whose first word is `TEXT`, followed by the middle dot, the name, optionally `<>`, and then `(SB)`. A qualified form names a symbol of another package and retains nothing here.
- Every exported function and variable of a `main` package declaring no `main` function, which is the shape of a plugin's `main` package. Such a package cannot be linked as a program, and a plugin resolves a function or a variable by name, so no other kind is retained.

A name a `//go:linkname` directive joins and a function an `//export` directive gives C are the `linkname` and `cgo-export` roots of the analysis, so this class retains neither. The string a plugin lookup call names is the other side of the plugin mechanism and belongs to `reflective-lookup`.

### template-field

Retains every exported field and method, on any type, whose name a template under a configured template directory names as a field or a method reference. The class records the template file and line that named it.

The evidence is a name written in a template rather than a type relation, so the class applies only where `analysis.template_dirs` is set, and it is a weak class. Every file under a configured directory is read, whatever its name, and parsed with the action grammar `text/template` and `html/template` share, at the delimiters `analysis.template_delimiters` sets and at the grammar's own where it sets none. A field reference names the identifiers of its own chain, so `{{ .Page.Title }}` names both `Page` and `Title`. A variable's chain names every identifier after the variable. A chain on the result of a parenthesized pipeline or of a call names every identifier of the chain. A file the grammar cannot parse names nothing and is skipped.

### reflective-lookup

Retains the declarations a constant string names at a reflective lookup call, on any type, and records the call site. The lookups are `MethodByName` and `FieldByName` on a `reflect.Value` or a `reflect.Type`, and a `Lookup` method that resolves a name at run time.

The evidence is a string equal to an identifier beside a call, rather than a type relation. So this is the weakest class, and every exemption it records names a site a maintainer can go and read. A call whose name argument is not a constant retains nothing, because the class declines to guess rather than retaining every declaration a lookup might reach.

## What no class answers

Two reasons a maintainer may have for keeping a symbol are answered by something other than an exemption:

- A symbol a declared consumer references needs no class. Name the repositories that import the module in the scope document and the reference holds the symbol live like any other, as [How the analysis decides](analysis.md#consumers) describes.
- A symbol only a test references is reported as `DS1004`, which a project assigns its own severity to, rather than retained.

A symbol kept for a reason no class covers, a published API whose removal would be a breaking change, is an ignore entry with a reason, as [Configuration and invocation](configuration.md#ignore-file) describes.

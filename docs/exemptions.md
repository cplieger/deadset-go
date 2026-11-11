# Exemption classes

An exemption is a named reason for which deadset-go keeps a symbol the reference graph alone would report. Every class is computed from Go type information rather than configured, so a project writes no suppression for a symbol a class covers. deadset-go implements the nine classes the deadset contract declares for Go. The class vocabulary and its detection rules are stated once in the contract's [exemptions page](https://github.com/cplieger/deadset-spec/blob/v8.0.0/docs/exemptions.md), and this page states what each class does here.

## Reading the retained set

`deadset-go print-retained` lists every symbol an exemption held back, with its position and the class that held it. Each entry also carries one clause of detail naming the relation and the thing it relates to, and the source site the evidence was found at. That answers why a symbol was not reported. If the symbol is in the retained set, the class named beside it is the reason.

Every finding a report carries names in `retained_by` the classes that retained its subject, where any did.

Three rules decide what the retained set holds:

- A symbol is in it only when an exemption is what kept the symbol from being reported. An exemption on a symbol something references anyway is computed and not listed, because the reference is the reason it is live.
- A symbol an exempt symbol keeps alive is in neither. The exemption seeds the reachability closure, so the private helper of a retained method is live rather than held back.
- One record per symbol, class, detail and holding declaration, at its first site by rendered position. A method one function converts to one interface at sixty sites is one record.

## Turning a class off

`exemptions.disabled` in the configuration lists class names that do not run, so an exemption suspected of hiding a defect can be tested. A disabled class retains nothing.

Any class of the contract's vocabulary may be named, so a configuration shared with the TypeScript analyzer works unchanged, and a class the vocabulary declares for TypeScript alone switches nothing off here. A name outside the vocabulary is refused: every verb that reads the configuration exits with the usage code and lists the classes.

Two classes read files and retain nothing until the project configures them: `analysis.template_dirs` for `template-field`, and nothing at all for `reflective-lookup`, which reads only the program's own string literals. A configured template directory the target does not hold ends the run with the usage code rather than being passed over, the same way a configured root that matches nothing is reported rather than ignored.

## Evidence in a test file does not hold for production

A report is built from a production analysis, which counts no reference a test file made. An exemption whose evidence is in a test file does not hold there either. A test that marshals a value or compares one makes no member of that value live for production. Such evidence counts as a test reference, so a member nothing in production references is reported as `DS1102`. `print-retained --mode=plain` counts every reference and every piece of evidence, which is the wider set, and a symbol the report names may be held back in it.

## What leaves the analysis is fully reachable

A struct value or a pointer to one, handed to a parameter typed as the empty interface of a function or method outside the analyzed program, has left the analysis. So has a slice, an array or a map of such values. The callee's body is not in the program, and the parameter's type keeps nothing of the value. So the callee may read its fields and call the methods its own package can name.

So has such a value returned as an interface-typed result by a method that implements an exported interface declared outside the program. That interface's package then stands for the callee's package.

The value flows into `encoding-reflection`, recorded at the call or the return. It keeps its fields and the encoding and decoding methods of both directions.

It also keeps every method by which it satisfies an interface the callee's package declares, exported or not, or writes as an interface type literal. An exported interface of a package in the callee's import closure counts too. So does an exported interface of an outside package of the program's import closure that imports the callee's package. Where the callee's package's imports include a template engine, the value keeps every exported method.

A standard container hands the value back to the program unchanged. A value given to a method of `sync.Map`, to a method of `atomic.Value` or to `context.WithValue` keeps no field. It keeps only the methods by which it satisfies an interface the container's own package declares or writes as a literal.

A callee whose body only decodes into the parameter is a decoding destination rather than this rule's. Its body is read from the build list's source for that question alone.

A function of the program that hands one of its own empty-interface parameters on inherits the crossing at that parameter, to a fixpoint. So a wrapper of a wrapper carries the rule of the call it forwards to. A declared consumer is inside the program, so a consumer's own wrapper is walked like the target's and a target value handed to it reaches the destination through it.

A call through a function value reaches what a direct call of each function the value can hold reaches, for this rule and every other class that reads a call's callee. The value is a variable, a field, an element of a map, slice or array, a method value or a method expression, whose every write the analysis reads. A call through a value that may hold a function the analysis does not know is also a call of a function outside the program, whose arguments every outside package of the program's import closure may read. That is a parameter, a call's result, a function literal, a value whose address is taken or that is handed to other code that may write it, and an exported variable or field a non-test file of an importable package declares. A place nothing writes, and that code outside the analyzed program cannot write, holds no function, so a call through it reaches none. An exported variable or field a non-test file of an importable package declares is still a call of a function outside the analyzed program, whether or not the program writes it. A write in a test file reaches only the calls in test files, so a function only a test assigns is never what a production call reaches.

The empty interface is the one parameter type this rule reads. A value handed to any other interface is a conversion the conversion set records, and `interface-satisfaction` retains the methods that interface requires, which is everything the callee can reach through the parameter's own type.

## The classes

### interface-satisfaction

Retains every method that satisfies an interface a value of the method's receiver type reaches, and each embedded field a promoted method is selected through, since deleting the field removes the method.

The evidence is the conversion set, every site in the target or in a loaded consumer where a value of a concrete type reaches a position typed as an interface. Such a site is an explicit conversion or a satisfaction assertion, an assignment, an argument, a return value, or an element stored in an interface-typed container. For each such pair of concrete type and interface, the methods of the type that answer what the interface requires are retained at that site.

An assertion naming an interface, `errors.As` and `errors.AsType` included, adds a site for each type that implements it and whose value reaches the operand's interface. In a library whose run loads no consumer, a published parameter of an interface type adds one for each type an outside program can hold.

A method answering an interface method no call site invokes is not retained, and `DS1202` reports it with that interface method. Each retention is a use by the declaration that holds the site, so it holds only while that declaration is live. An assertion's site is held by the assertion's declaration as well. A satisfaction assertion is a use by the type it asserts, so it keeps nothing of a type only dead code builds.

A value converted to a defined interface that a package outside the program declares is held by that package, which may assert another interface on it. So the value also keeps the methods by which it, or a pointer to it, satisfies an interface that package's source asserts. An asserted interface is one a type assertion, a type switch case, or an `errors.As` or `errors.AsType` call names, whichever package declares it. An interface that package declares and never asserts keeps nothing.

A value converted to `error` may reach a dependency that asserts its own error interface on it, as a retry loop does with `errors.AsType[Transient](err)` on the error a callback returns. So the value also keeps the methods by which it satisfies a non-generic defined interface that embeds `error` or declares `Error() string`, where a package outside the program declares that interface and that package's own source asserts it. The type converted is the one that counts: a value of a type keeps no method only a pointer to it has, since the assertion cannot match that value. Only such a package's source is read for this, and only its assertions of that interface count.

A type whose values never reach an interface retains nothing whatever it happens to implement, because no caller can dispatch to it through an interface the program never builds.

A method satisfying two interfaces at two sites is retained twice, once per site, so the retained set shows every conversion that depends on the method. A type registered as a flag value, or used as a writer, a round tripper or a sort interface, is an ordinary member of the conversion set.

### encoding-reflection

Retains, on a type whose values reach a consumer that names members by string at run time, the members that consumer reads. Those are the exported fields, every field carrying a struct tag, and the methods the consumer resolves by name.

[The encoding-reflection class](encoding-reflection.md) lists its destinations, what each retains and how a value reaches the types it holds.

### format-verb-contract

Retains the `String() string` and `Error() string` methods of a type whose values reach a facility that formats its operands with the `fmt` machinery, because the facility calls them through an interface and no static reference exists. It also retains `Format(fmt.State, rune)`, which the machinery calls under every verb, and `GoString() string` where a `%#v` verb can print the value, and it reads every field the machinery prints.

The facilities are:

- The print, format and error-construction functions of `fmt`.
- The print, fatal and panic functions of `log`, and the same methods of a `log.Logger`.
- The log, error, fatal and skip methods of a testing type.
- The logging functions of `log/slog`, with the methods of its logger and its attribute constructors.
- The functions of the analyzed program that forward their own variadic operands to any of those.

Three rules narrow it. A format string the call computes rather than writes is read as binding every operand, which retains more than the call can reach and never less. A method is retained only in the form a verb calls, taking no argument and returning one string, so a method of another shape that shares the name is not. And the reach stops at an interface-typed field and at an unexported field, which the machinery prints without calling a method.

An operand reaches through a pointer, a slice, an array and a map, because the machinery asks each element for its string in turn. It also reaches through every exported or embedded field, of a defined struct type or of a struct type with no name, at any depth.

The machinery prints each field of a struct it does not print through a method, exported or not, so those fields are read at the call whether or not anything writes them. A value printed through `Format`, `GoString`, `Error` or `String` under a verb that calls it has none of its fields printed, and a pointer below the operand prints as an address. That reading, and `Format` and `GoString`, hold only for an operand whose every printed value has a static type that is no interface and no `reflect.Value`; any other operand keeps `String` and `Error` alone.

### errors-duck-typing

Retains the methods the standard error helpers reach by duck typing, on every type a value of which reaches a position typed as `error`. Such a method's name and signature match `Is(error) bool`, `As(any) bool`, `Unwrap() error` or `Unwrap() []error`.

The signature decides as much as the name. A method named `Is` whose parameter is not `error` implements the program's own comparison rather than the helper's contract, `errors.Is` never calls it, and it is not retained. A type reaching an interface of the program's own also counts where a chain of conversions leads from it to `error`, while every declaration holding the chain is live.

### enum-group

A constant, a member of an enumerated type among them, is retained unless the analysis proves that deleting it, alone or together with any other constant the class does not retain, leaves a program that still builds and does the same, and the class decides constant by constant. Deleting a constant that no code names changes what the program does only through the other constants its deletion moves, because a constant has no presence at run time through which Go code of the program could observe it. So such a deletion changes nothing when every constant it moves is still a valid constant of its type and none of them is one that code names, whatever values of the type arrive from a decoder or from code outside the program. Where it moves a constant that code names, the constant deleted is a candidate only when it is a member of an enumerated type, every constant it moves has the type's next value down, the move keeps every equality and order between the values code can hold of the type, every value of the type that arises in the analyzed program comes from a member's name, a constant or the type's zero value and is only compared or copied between places of the program, and no code outside the analyzed program, a library's importer among it, can hold a value of the type or name a moved member. A constant of a package that holds a source file in another language than Go, for any build configuration, is retained, because that file can name it in forms the analysis does not read. Every deletion of a subset of the candidates must keep all of this. A constant whose deletion the analysis cannot decide is retained.

The enumerated type is a defined type the target declares whose constants are declared in an `iota` group, and every non-blank constant of the type a package-level declaration declares is a member, its type written through an alias or not, one a later block declares included. A constant of the type a function body declares is none. Every other constant, an untyped one, one of a local type and one of a type no file under the target root declares (a standard-library or module cache type among them) included, is judged by the same rules as a member, save that it never passes the conditions that read a type's values below.

A constant of a package that holds a source file in another language than Go, for any build configuration, is retained, as linkname-cgo-asm-plugin states. A constant is named where a reference the run counts names it, in any build configuration and in a loaded consumer, and in a library where it is an exported constant of an importable package. The constant judged is not named in its own judgement. The candidates are the constants no reference names, less those the class retains. Deleting a set of candidates removes from each specification those names with the expressions paired with them, and removes a specification all of whose names are deleted. A specification keeps its place, and so its `iota` value, while one of its names stays. A candidate is retained where its specification has several names and a later specification repeats its expression list or it repeats an earlier one, and where its specification is the first of its block and the next one repeats its list, because removing it alone does not compile. A specification is removable when every name it declares is a candidate, a blank name never being one. Removing a specification moves three kinds of constant: one whose specification follows it in the same block and whose expression, stated or repeated, names `iota`, one whose specification repeats the list the removed one states, and one whose declaration names a moved constant. A deletion that removes no specification moves nothing.

Every moved constant, named or not and blank included, must move one step and stay valid. It moves one step when its expression, stated or repeated from a specification that is not removable, is `iota` plus or minus untyped constant expressions that name neither `iota` nor a moved constant, in parentheses or not, the whole converted to the constant's own type or not, or it is declared as the plain name of a constant that does. Its value must be an integer and fixed, naming through every constant it names only literals, `iota` and constants the target declares in files with no build constraint (no `//go:build` line, no `// +build` line where no `//go:build` line exists, no `_GOOS`, `_GOARCH` or `_GOOS_GOARCH` file-name suffix, and no import of `"C"`), and holding no call other than a conversion, `min` or `max`, no function of `unsafe` and no `^` on a `uint` or `uintptr` operand. And its value less the number of removable specifications that move it must be no less than the least value of its type on every target, `int`, `uint` and `uintptr` taken at 32 bits. Where one fails, every candidate of every removable specification that moves that constant is retained.

A candidate whose removal moves no named constant is not retained, whatever values of its type the program holds. Where a named constant moves, every candidate of every removable specification that moves it is retained unless that constant is a member of an enumerated type whose underlying type is an integer type and all of these hold for its type:

1. The items of the type are each named member, the value of each constant expression of the type that is not a member's name and that no larger constant expression of the type holds and stands outside a member's own declaration, and zero. A constant expression that names a moved constant other than as its plain name, converted to the type or not, retains every candidate of every removable specification that moves that constant. Each item moves down by one for each removed specification that moves it. For every two items, the relation between them (equal, less or greater) holds alike after the removal of every subset of the removable specifications: for two items _p_ and _q_ with _d_ = _q_ − _p_, _d_ less the number of removable specifications that move _q_ and not _p_, and _d_ plus the number that move _p_ and not _q_, both have the sign of _d_. For _d_ = 0 that means the two are moved by the same specifications. An item whose value is not fixed keeps its relation only with an item moved by the same specifications. Where a pair fails, every candidate of every specification that moves exactly one of the two is retained.
2. Every expression whose type holds the enumerated type is produced and used as this list names. A type holds it when it is the type, or a pointer, slice, array or channel whose element holds it, a map whose key or value holds it, a struct with a field that holds it, a signature with a parameter or result that holds it, an interface with a method whose signature holds it, or a defined type whose underlying type or one of whose methods holds it. An instance of a generic type holds what its origin holds and what each type argument holds. The forms that produce such a value are a member's name, a constant expression (judged by its value under 1, each constant expression of another type it holds being read as its own), a read of a variable, parameter, field, element, map entry or pointed-to value, a composite literal, the result of `make`, `new`, `append`, `min` or `max`, a receive, the `&` operator, and the result of a call whose every callee, read through the program's function values, the analyzed program declares with a body. Such a value may be an operand of `==`, `!=`, `<`, `<=`, `>` or `>=` against a value of the same type, or a switch tag or case. It may be the operand of a selector selecting a field, a method the analyzed program declares with a body, or a method of an embedded field whose type does not hold the enumerated type. It may be the operand an index or slice expression indexes, a map key or `delete` key of the map's key type, the operand of `*`, `&`, a receive, `len`, `cap` or `clear`, or the channel a send statement sends on. It may be assigned, declared, returned, sent, appended, copied, ranged into or placed in a composite literal where the place it goes to has an identical type, or passed to a parameter of identical type of a call whose every callee the analyzed program declares with a body, several results of one call spread into several parameters each counting so. It may be the range expression of a range over a slice, array, pointer to an array, map or channel, an operand of `min` or `max` of the same type, or a value the program discards. Any other form retains, among them a conversion to or from another type, an interface type included, any other operator, `++`, `--` and an assignment operator, a value of the type indexing an array, slice or string or standing as an array length, a shift count or a composite literal's index, a type assertion or type switch case naming a type that holds it, a type argument that holds it, written or inferred, a range over a value of the type or over a function, a call of code the analyzed program does not declare with a body or whose callee it does not know, `print`, `println`, `panic` and the functions of `unsafe`, a package-level variable a `//go:linkname` directive names or linkname-cgo-asm-plugin retains for a source file in another language than Go, and a function whose signature holds the type that a `//go:linkname` or `//export` directive names or that class retains so.
3. In a library, no exported constant, variable, function, type or alias of an importable package, and no method of a type such a declaration holds, has a type that holds the enumerated type.

A retained candidate makes its specification not removable, so the class applies these conditions again to the candidates left until none is retained, and every deletion of a subset of what is left then keeps them all. The class decides over the production files and again over every file, and a constant is retained when either retains it. A constant retained over the production files is retained at the first site, in position order, at which a condition fails there. A constant retained only over every file is retained at a test file's site: the first use a test file makes that the list does not name, or where only a test file's reference names a moved constant, that reference.

### generated-file

Retains every declaration written in a generated file, which is a file carrying the standard Go generated-code header. The generator owns the file, so a deletion there is undone the next time it runs and the declaration to remove is in the generator's input.

Under `analysis.generated_files` set to `include` the class retains nothing and every declaration of a generated file is judged like any other. The site is the package clause of the generated file.

### linkname-cgo-asm-plugin

Retains a symbol another compilation unit reaches by a name the type checker never records, which is three mechanisms:

- Every constant, type, struct field, variable, function and method of a package whose directory holds a source file in another language than Go, with the signature of each function and method. The directory's own listing is read, so a file whose name starts with `_` or `.` and a file a build constraint excludes count too. A source in another language is a file whose name ends in `.s`, `.S`, `.sx`, `.c`, `.cc`, `.cpp`, `.cxx`, `.h`, `.hh`, `.hpp`, `.hxx`, `.m`, `.f`, `.F`, `.for`, `.f90`, `.swig`, `.swigcxx` or `.syso`, or a Go file that imports `"C"`. `go_asm.h` names constants, field offsets and type sizes, and assembly operands, preprocessor directives, C and object files use them and reach functions and variables by symbol, so no form is parsed. The site is line 1 of the first such file of the directory.
- Every function and variable of the target whose name occurs as a run of identifier characters in any file with one of those extensions under the target root, outside `.git`, `node_modules`, `vendor` and another module's directory, which is how assembly reaches another package's symbol (`a·Probe(SB)`), an `#include`d file among them. The site is line 1 of the first file that writes the name. A directory or such a file of the tree the run cannot read counts as naming every function and variable, retained at that entry, and the report carries an `unread-entry` note naming it. A package directory the run cannot list counts as holding such a file. A symbolic link counts as what it points at, decided without opening it, as every walk of the tree decides an entry: one to nothing, one through a file or one that loops is no file, since no compiler can read it either, one to a directory, a device, a pipe or a socket is none, and one whose target the run cannot inspect counts as a file it cannot read.
- Every exported function and variable of a `main` package declaring no `main` function, which is the shape of a plugin's `main` package. Such a package cannot be linked as a program, and a plugin resolves a function or a variable by name, so no other kind is retained.

A name a `//go:linkname` directive joins and a function an `//export` directive gives C are the `linkname` and `cgo-export` roots of the analysis, so this class retains neither. The string a plugin lookup call names is the other side of the plugin mechanism and belongs to `reflective-lookup`.

### template-field

Retains every exported field and method, on any type, whose name a template under a configured template directory names as a field or a method reference. The class records the template file and line that named it.

The evidence is a name written in a template rather than a type relation, so the class applies only where `analysis.template_dirs` is set, and it is a weak class. Every file under a configured directory is read, whatever its name, and parsed with the action grammar `text/template` and `html/template` share, at the delimiters `analysis.template_delimiters` sets and at the grammar's own where it sets none. A field reference names the identifiers of its own chain, so `{{ .Page.Title }}` names both `Page` and `Title`. A variable's chain names every identifier after the variable. A chain on the result of a parenthesized pipeline or of a call names every identifier of the chain. A file the grammar cannot parse names nothing and is skipped.

### reflective-lookup

Retains the declarations a constant string names at a reflective lookup call, on any type, and records the call site. The lookups are `MethodByName` and `FieldByName` on a `reflect.Value` or a `reflect.Type`, and a `Lookup` method that resolves a name at run time.

The evidence is a string equal to an identifier beside a call, rather than a type relation. So this is the weakest class, and every exemption it records names a site a maintainer can go and read. A call whose name argument is not a constant retains nothing, because the class declines to guess rather than retaining every declaration a lookup might reach.

A router that registers a handler by its method name resolves the method the same way. So a call of a function or method outside the analyzed program that passes a value of a type `T`, or a pointer to one, retains each exported method of `T` that a constant string argument of the same call names. Each item of the string separated by `;` names one method, after an optional prefix ending in `:`, so `"get,post:Login;put:Update"` names `Login` and `Update`.

## What no class answers

Two reasons a maintainer may have for keeping a symbol are answered by something other than an exemption:

- A symbol a declared consumer references needs no class. Name the repositories that import the module in the scope document and the reference holds the symbol live like any other, as [How the analysis decides](analysis.md#consumers) describes.
- A symbol only a test references is reported as `DS1102`, which a project assigns its own severity to, rather than retained.

A symbol kept for a reason no class covers, a published API whose removal would be a breaking change, is an ignore entry with a reason, as [Configuration and invocation](configuration.md#ignore-file) describes.

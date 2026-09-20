# Exemption classes

An exemption is a named reason for which deadset-go keeps a symbol the reference graph alone
would report. Every class is computed from Go type information rather than configured, so a
project writes no suppression for a symbol a class covers. deadset-go implements the nine classes
the deadset contract declares for Go; the class vocabulary and its detection rules are stated once
in the contract's
[exemptions page](https://github.com/cplieger/deadset-spec/blob/v2.1.0/docs/exemptions.md), and
this page states what each class does here.

## Reading the retained set

`deadset-go print-retained` lists every symbol an exemption held back, with the class that held
it, one clause of detail naming the relation and the thing it relates to, and the source site the
evidence was found at. That is the answer to "why was this symbol not reported": if the symbol is
in the retained set, the class named beside it is the reason.

Every finding a report carries names in `retained_by` the classes that retained its subject,
where any did.

Three rules decide what the retained set holds:

- A symbol is in it only when an exemption is what kept the symbol from being reported. An
  exemption on a symbol something references anyway is computed and not listed, because the
  reference is the reason it is live.
- A symbol an exempt symbol keeps alive is in neither: the exemption seeds the reachability
  closure, so the private helper of a retained method is live rather than held back.
- One record per symbol, class and detail, at the first site by rendered position. A method
  converted to one interface at sixty sites is one record.

## Turning a class off

`exemptions.disabled` in the configuration lists class names that do not run, so an exemption
suspected of hiding a defect can be tested. A disabled class retains nothing. A name outside the
vocabulary disables nothing.

Two classes read files and retain nothing until the project configures them:
`analysis.template_dirs` for `template-field`, and nothing at all for `reflective-lookup`, which
reads only the program's own string literals. A configured template directory the target does not
hold ends the run with the usage code rather than being passed over, the same way a configured root
that matches nothing is reported rather than ignored.

## Evidence in a test file does not hold for production

A report is built from a production analysis, which counts no reference a test file made. An
exemption whose evidence is in a test file does not hold there either: a test that marshals a
value or compares one makes no member of that value live for production, exactly as a test's
reference is no reference there. `print-retained --mode=plain` counts every reference and every
piece of evidence, which is the wider set, and a symbol the report names may be held back in it.

## What leaves the analysis is fully reachable

A struct value, or a pointer to one, handed to a parameter typed as the empty interface of a
function or method outside the analyzed program has left the analysis. The callee's body is not in
the program and the parameter's type keeps nothing of the value, so whatever the callee does with
it reads its fields and may call its exported methods, and the value flows into
`encoding-reflection` with that class's full retained set, recorded at the call with the callee
named.

A function of the program that hands one of its own empty-interface parameters on inherits the
crossing at that parameter, to a fixpoint, so a wrapper of a wrapper carries the rule of the call
it forwards to. A declared consumer is inside the program, so a consumer's own wrapper is walked
like the target's and a target value handed to it reaches the destination through it.

The empty interface is the one parameter type this rule reads. A value handed to any other
interface is a conversion the conversion set records, and `interface-satisfaction` retains the
methods that interface requires, which is everything the callee can reach through the parameter's
own type.

## The classes

### interface-satisfaction

Retains every method that satisfies an interface a value of the method's receiver type reaches.

The evidence is the conversion set: every site where a value of a concrete type reaches a position
typed as an interface, which is an explicit conversion or a satisfaction assertion, an assignment,
an argument, a return value, or an element stored in an interface-typed container. For each such
pair of concrete type and interface, the methods of the type that answer what the interface
requires are retained at that site. A type whose values never reach an interface retains nothing
whatever it happens to implement, because no caller can dispatch to it through an interface the
program never builds.

A method satisfying two interfaces at two sites is retained twice, once per site, so the retained
set shows every conversion that depends on the method. A type registered as a flag value, or used
as a writer, a round tripper or a sort interface, is an ordinary member of the conversion set.

### encoding-reflection

Retains, on a type whose values reach a consumer that names members by string at run time, the
members that consumer reads: the exported fields and every field carrying a struct tag, and the
methods the consumer resolves by name.

The destinations, and what each retains:

- Fields and the methods it resolves by name for an argument of a function or method of
  `encoding/json`, `encoding/json/v2`, `encoding/xml` or `encoding/gob`.
- Fields alone for a `database/sql` scan target, an argument of `reflect.DeepEqual`, and an
  argument of any other function of `reflect` that hands out no method.
- Fields and the exported methods for a template engine, a sort interface, a `log/slog` logging
  call or attribute constructor, every entry point of `reflect` from which a method is reachable by
  name, and a destination outside the analyzed program.

An encoder resolves a method by name in the direction its entry point works in: an entry point
whose name begins `Encode` or `Marshal` retains the encoding methods, one whose name begins
`Decode` or `Unmarshal` retains the decoding methods, and one whose name begins with neither
retains both, because it takes a value for either direction.

| Package | Encoding | Decoding |
| --- | --- | --- |
| `encoding/json`, `encoding/json/v2` | `AppendText`, `MarshalJSON`, `MarshalJSONTo`, `MarshalText` | `UnmarshalJSON`, `UnmarshalJSONFrom`, `UnmarshalText` |
| `encoding/xml` | `MarshalText`, `MarshalXML`, `MarshalXMLAttr` | `UnmarshalText`, `UnmarshalXML`, `UnmarshalXMLAttr` |
| `encoding/gob` | `GobEncode`, `MarshalBinary` | `GobDecode`, `UnmarshalBinary` |

A value reaches through a pointer, a slice, an array, a map key or value and an embedded field,
and from every type so reached through that type's fields again, until no further type joins,
because an encoder walks the whole value rather than its outermost type. The walk stops at an
interface-typed field, whose dynamic type the analysis does not see. A method is retained where
the defined type declares it, so a method promoted from an embedded type is retained where the
embedded type is reached.

### format-verb-contract

Retains the `String() string` and `Error() string` methods of a type whose values reach a
facility that formats its operands with the `fmt` machinery, because the facility calls them
through an interface and no static reference exists.

The facilities are the print, format and error-construction functions of `fmt`, the print, fatal
and panic functions of `log` and the same methods of a `log.Logger`, the log, error, fatal and
skip methods of a testing type, the logging functions of `log/slog` with the methods of its logger
and its attribute constructors, and the functions of the analyzed program that forward their own
variadic operands to any of those.

Three rules narrow it. A format string the call computes rather than writes is read as binding
every operand, which retains more than the call can reach and never less. A method is retained
only in the form a verb calls, taking no argument and returning one string, so a method of another
shape that shares the name is not. And an operand reaches through a pointer, a slice, an array and
a map, because the machinery asks each element for its string in turn, but the reach stops at each
defined type.

### errors-duck-typing

Retains, on every type a value of which reaches a position typed as `error`, the methods the
standard error helpers reach by duck typing: a method whose name and signature match
`Is(error) bool`, `As(any) bool`, `Unwrap() error` or `Unwrap() []error`.

The signature decides as much as the name. A method named `Is` whose parameter is not `error`
implements the program's own comparison rather than the helper's contract, `errors.Is` never calls
it, and it is not retained. The interface reached must be `error` itself rather than an interface
that embeds it, so a type the program only ever uses through an interface of its own retains
nothing here.

### enum-group

Retains every member of an enumerated type whose values can arrive by conversion rather than by
name, so a member reached only by value is never reported.

The enumerated type is a defined type whose constants are declared in an `iota` group, which is a
`const` block in which at least one specification's value expression mentions `iota`, the
specifications that repeat the previous expression included. The class fires when the type
declares a `String`, `MarshalText`, `UnmarshalText`, `MarshalJSON` or `UnmarshalJSON` method, when
a value of it is produced by a conversion whose operand is a non-constant integer, or when a value
of it is the target of a decoder. Every constant of a type that fires is retained, one declared
outside the `iota` group included, because a value that arrives by conversion can equal any of
them. A conversion method is matched by name alone. A constant declared with the blank identifier
names no member and is retained by nothing.

### generated-file

Retains every declaration written in a generated file, which is a file carrying the standard Go
generated-code header. The generator owns the file, so a deletion there is undone the next time it
runs and the declaration to remove is in the generator's input.

Under `analysis.generated_files` set to `include` the class retains nothing and every declaration
of a generated file is judged like any other. The site is the package clause of the generated
file.

### linkname-cgo-asm-plugin

Retains a symbol another compilation unit or the runtime reaches by a name the type checker never
records, which is four mechanisms:

- A function or variable named on either side of a `//go:linkname` or `//go:linknamestd`
  directive, in a file importing `"unsafe"` as the toolchain requires of the directive. The local
  name is resolved in the declaring package's scope, and the qualified name the directive joins it
  to is resolved in the loaded package whose import path it spells.
- A function carrying an `//export` directive in a file importing `"C"`.
- The declaration a `TEXT` directive of an assembly file of the same package names, which is a
  line whose first word is `TEXT`, followed by the middle dot, the name, optionally `<>`, and then
  `(SB)`. A qualified form names a symbol of another package and retains nothing here.
- Every exported function and variable of a `main` package declaring no `main` function, which is
  the shape of a plugin's `main` package: such a package cannot be linked as a program, and a
  plugin resolves a function or a variable by name, so no other kind is retained.

The string a plugin lookup call names is the other side of the last mechanism and belongs to
`reflective-lookup`.

### template-field

Retains every exported field and method whose name a template under a configured template
directory names as a field or a method reference, on any type, and records the template file and
line that named it.

The evidence is a name written in a template rather than a type relation, so the class applies
only where `analysis.template_dirs` is set, and it is a weak class. Every file under a configured
directory is read, whatever its name, and parsed with the action grammar `text/template` and
`html/template` share, at the delimiters `analysis.template_delimiters` sets and at the grammar's
own where it sets none. A field reference names the identifiers of its own chain, so
`{{ .Page.Title }}` names both `Page` and `Title`; a variable's chain names every identifier after
the variable; and a chain on the result of a parenthesized pipeline or of a call names every
identifier of the chain. A file the grammar cannot parse names nothing and is skipped.

### reflective-lookup

Retains the declarations a constant string names at a reflective lookup call, on any type, and
records the call site. The lookups are `MethodByName` and `FieldByName` on a `reflect.Value` or a
`reflect.Type`, and a `Lookup` method that resolves a name at run time.

The evidence is a string equal to an identifier beside a call rather than a type relation, so this
is the weakest class and every exemption it records names a site a maintainer can go and read. A
call whose name argument is not a constant retains nothing: the class declines to guess rather
than retaining every declaration a lookup might reach.

## What no class answers

Two reasons a maintainer may have for keeping a symbol are answered by something other than an
exemption:

- A symbol a declared consumer references needs no class. Name the repositories that import the
  module in the scope document and the reference holds the symbol live like any other; see
  [analysis.md](analysis.md).
- A symbol only a test references is reported as `DS1004`, which a project assigns its own
  severity to, rather than retained.

A symbol kept for a reason no class covers, a published API whose removal would be a breaking
change, is an ignore entry with a reason; see [configuration.md](configuration.md).

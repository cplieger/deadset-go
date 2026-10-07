package report

// ruleText is the text one issue kind is described by: its rule, whole, and the
// condition it reports under where the kind declares one.
type ruleText struct {
	rule         string
	precondition string
}

// shortDescription is the first sentence of the rule: the text up to and including
// the first full stop that a space follows or that ends the rule, and the whole rule
// where no full stop does.
func (r ruleText) shortDescription() string {
	for i := range len(r.rule) {
		if r.rule[i] == '.' && (i+1 == len(r.rule) || r.rule[i+1] == ' ') {
			return r.rule[:i+1]
		}
	}
	return r.rule
}

// help is the rule, then, where the kind declares a precondition, one blank line and
// the precondition.
func (r ruleText) help() string {
	if r.precondition == "" {
		return r.rule
	}
	return r.rule + "\n\n" + r.precondition
}

// ruleTexts is the text of every live issue kind, by code, written out rather than
// read at run time; a test pins every entry equal to the vocabulary document.
var ruleTexts = map[string]ruleText{
	"DS1001": {
		rule: "An exported symbol with no reference in the target and no reference from any loaded consumer. A symbol referenced only from its own declaration site counts as unreferenced.",
	},
	"DS1002": {
		rule: "An unexported symbol with no reference in the target. A symbol referenced only from its own declaration site counts as unreferenced.",
	},
	"DS1003": {
		rule:         "A struct field, class member or type member with no reference, a private member included. A member referenced only from its own declaration site counts as unreferenced.",
		precondition: "A TypeScript member that no value writes and nothing reads, whose type names a type parameter of its declaring interface, class or type alias, is not reported when every part of that declaration that names the type parameter is a member no value writes and nothing reads. TypeScript compares object types by their members, so such members are what make two instantiations of their type distinct types, and deleting them leaves the type parameter phantom and lets one instantiation stand for another. A member a value writes is decided by the rule of DS1301 instead, whatever its type names.",
	},
	"DS1004": {
		rule: "A symbol with zero production references and at least one test reference: an unused-exported, unused-unexported or unused-member candidate whose test reference count is not zero, reported once under this code. A reference from a consumer's test files is a test reference unless the configuration counts consumer tests as production. A reference from test-support code, a package or file that only tests reach, is a test reference. A declaration of test-support code that test code references is reported under this code at the reachability class possible, as grammar/analysis.md states. Such a declaration that is an interface is reported under DS1201 instead.",
	},
	"DS1005": {
		rule: "A test symbol whose set of referenced target symbols is non-empty and every member of that set is reported dead. A test that references at least one live target symbol is never reported, no notion of a test's subject and no name matching enters the rule, and the message states the rule. The test joins the dead component of the symbols it references, and it and each of them count as referencing each other, so the test is a member of their cycle and never a dead symbol outside it.",
	},
	"DS1006": {
		rule: "A symbol carrying a deprecation marker and no production reference: an unused-exported, unused-unexported or unused-member candidate whose symbol is deprecated, reported once under this code. Go identifies the marker by the convention the standard tools recognize, TypeScript by the documentation tag its tools recognize.",
	},
	"DS1101": {
		rule:         "An exported symbol whose every reference is inside the symbol's own package or module, reported as a candidate for unexporting. The subject is a package-level declaration or a method. Three subjects are excluded. An interface method is excluded because its exportedness is the contract of the interface that declares it. A struct field is excluded because an encoder reads it by name. A method that satisfies an interface some symbol uses as a type is excluded because it cannot be unexported without its type ceasing to satisfy that interface. The finding names the narrower visibility the references support. A declared cross-language edge counts as an out-of-package reference. Where the edge's other side is unknown to the analyzer, the finding is emitted pending.",
		precondition: "Closed world only. A Go main package, and an internal/ directory tree for importers outside its parent, are closed worlds whatever the configuration declares, because no other program can import them, so this finding is reported there always. A published package is a closed world only when the configuration declares the consumer set complete and every declared consumer loads. A library with no consumer loaded and no complete consumer set declared gets this finding on its internal/ tree and its main packages and never on its published API. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first. A type that a parameter or a result of an exported function or method of its own package names is not reported when a reference from outside the package references that function or method, because the caller holds values of the type, and unexporting it would leave an exported signature naming a type its callers cannot name.",
	},
	"DS1102": {
		rule:         "An exported symbol of a non-internal package whose every reference is inside the target module, reported as a candidate for relocation behind an internal boundary. The subject is a package-level declaration or a method. Three subjects are excluded. An interface method is excluded because its exportedness is the contract of the interface that declares it. A struct field is excluded because an encoder reads it by name. A method that satisfies an interface some symbol uses as a type is excluded because it cannot be unexported without its type ceasing to satisfy that interface. The finding names the narrower visibility the references support. A declared cross-language edge counts as an out-of-package reference. Where the edge's other side is unknown to the analyzer, the finding is emitted pending.",
		precondition: "Closed world only. Reported always for a main package and an internal/ directory tree, and for a published package only when the configuration declares the consumer set complete and every declared consumer loads. A library with no consumer loaded and no complete consumer set declared gets this finding on its internal/ tree and its main packages and never on its published API. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first. A type that a parameter or a result of an exported function or method of its own package names is not reported when a reference from outside the target module references that function or method, because the caller holds values of the type, and moving it behind an internal boundary would leave an exported signature naming a type its callers cannot name.",
	},
	"DS1103": {
		rule: "An unused exported symbol in a package or file no external code can import: an unused-exported candidate whose enclosing package or file is unimportable from outside, reported once under this code at the certain class, because unimportability is a property of the package graph rather than of the consumer set.",
	},
	"DS1104": {
		rule:         "An exported declaration used only inside its own file, reported as a candidate for removing the export keyword. The finding names the narrower visibility the references support.",
		precondition: "Closed world only. Reported in a file that is not an entry file and that either belongs to a project whose consumer set the configuration declares complete or is reached by no manifest export. A declared cross-language edge counts as a reference from outside the file. Where the edge's other side is unknown to the analyzer, the finding is emitted pending. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first.",
	},
	"DS1201": {
		rule: "An interface no symbol uses as a type, counting uses from every loaded module. The finding names the concrete implementations and their positions, and the interface's members join its dead component rather than being reported as independent unused declarations.",
	},
	"DS1203": {
		rule:         "An interface method that no call site invokes or selects through the interface, whatever the number of implementations. The finding names the concrete implementations and their positions. Each method that implements it and that nothing else keeps is reported under the code its own references select. Such a method counts as referenced by the interface method alone, so it belongs to this finding's dead component, whose root member is the interface method.",
		precondition: "In Go, every method of an interface that declares an unexported method is exempt. That is the sum-type shape, whose method set exists to restrict the implementors, and a TypeScript interface cannot take it because it declares no member less visible than itself. In both languages, every marker method is exempt, an interface method whose every implementation carries an empty body.",
	},
	"DS1204": {
		rule: "A compile-time satisfaction assertion whose interface no symbol uses as a type.",
	},
	"DS1301": {
		rule: "A package-level or module-level variable, struct field, class member, type member or collection that production code writes and never reads. In production mode a read from a test file counts as no read. A write from a loaded consumer's code counts as a read, because the deletion the finding names would break a consumer the target does not own. A write in a consumer's test file counts as a read in the run that counts test references. It counts in a production sweep only where the configuration counts consumer tests as production. A comparison of struct values reads every field of the type: in Go, a value of a struct type used as an operand of `==` or `!=`, as a map key, or as a `switch` tag or `case` expression is a read of every field of that type, transitively through the fields of every struct type it holds, recorded at the comparison, because equality reads every field and a field that decides equality carries information. In Go a type argument the program instantiates a type parameter with, where the parameter's constraint is `comparable` or embeds it, is compared as an operand of `==` of that type is, recorded at the instantiation, so the struct values `slices.Equal`, `slices.Index` or `maps.Equal` compares have every field read. `reflect.DeepEqual` is not a comparison under this rule. In TypeScript a comparison of two object references reads no member, because `===` compares identity and never a member. In TypeScript a property of an object literal writes the member of the same name that the literal's contextual type declares, whether or not the compiler checks the literal for excess properties. Where that contextual type is a union or an intersection, the property writes the member of that name on each constituent that declares one. So a literal that a declaration annotates, a function returns, a call passes or a `satisfies` expression checks writes the members of the type it is given. A store through the value a field or a member holds reads that field or member rather than writing it, because the store lands in storage every other holder of the value reads. In Go that store is an assignment, an increment, a compound assignment or a `delete` whose target is reached through a struct field holding a map, a slice or a pointer. Examples are `s.f[k] = v`, `s.f.x = v` and `delete(s.f, k)`. The same store through a field holding an array or a struct is a store into the field itself. TypeScript holds every object by reference, so an element or property store through a member's value reads the member. The finding names each write position, so the deletion set is visible. Deleting the subject and its writes also deletes every declaration whose only uses they hold. Such a declaration is a type parameter of the subject's declaring type that the subject's type alone names, an import binding the writes alone use, or a declaration whose every use is in the type annotation of the subject or of another subject this rule reports, or in the writes those findings name. The finding's message names each such declaration, and no finding is reported about the declaration itself.",
	},
	"DS1302": {
		rule: "An enumerated member that no symbol names, at the reachability class derived for the member and with no confidence ceiling below it. Every member of a type that carries a string, text or binary conversion method, or that a conversion from an integer or a wire value produces, is retained by the enum-group exemption first, so a member reached only by value is never a candidate.",
	},
	"DS1303": {
		rule:         "A type parameter of a function or method that no part of the declaration's signature and no part of the declaration's body names, at the certain class. The deletion is local to the declaration and the explicit instantiations the reference set already lists. A type parameter of a function or method that is itself dead falls with it and is reported under no code of its own.",
		precondition: "Function and method type parameters only. A type parameter of a type declaration is never reported, because a phantom type parameter such as `type ID[T any] int` makes two instantiations distinct types while naming the parameter nowhere, so deleting it changes the program. A method signature an interface or an object type declares belongs to that type declaration, so its type parameters are never reported either.",
	},
	"DS1501": {
		rule:         "A source file no configuration in the build matrix builds. On the Go side the finding names the build constraint that excluded the file. A file whose build constraint is the ignore tag is never reported: the toolchain applies no build constraint to a file named on its own command line, so that tag is the toolchain's convention for a file built by hand. A file any other custom tag excludes is reported under a matrix the configuration declares complete, because completeness is the maintainer's assertion that the listed configurations are every one the target builds, and a configuration the maintainer builds by hand belongs in that list.",
		precondition: "Reported only when the configuration declares the build matrix complete, and never for a file the toolchain ignored solely because it imports \"C\" under a build with cgo disabled. Such a file is recorded as excluded by cgo rather than as never built.",
	},
	"DS1502": {
		rule: "A source file that no import reaches, that no root names, and in which no declared cross-language edge names a declaration. An import written in a loaded consumer reaches a file of the target as an import written in the target does. An edge's side names a declaration as a root names one, so a file holding a declaration an edge names is evaluated with the edge rather than reported: whether that declaration is dead is the edge evaluation's to say, and the merge resolves it against the paired side.",
	},
	"DS1601": {
		rule:         "A directly declared dependency that no import in the target needs. On the Go side, a direct require whose module provides no package any target package or test variant imports. On the TypeScript side, a manifest dependency, development dependency or peer dependency the project's import closure does not need. A deletion finding whose fix would remove the last use of a dependency names that dependency.",
		precondition: "On the Go side the rule is the semantics of `go mod tidy -diff` exactly: a requirement marked indirect is never reported, because it exists to pin a transitive version and removing it changes the build list. On the TypeScript side the import closure is the files of the run's projects, and four more dependencies are needed. The first is one whose installed manifest declares a command, because a command is run by name rather than imported. The second is one that the installed manifest of a needed dependency declares as a peer and does not mark optional. The third is every required peer of a dependency so held, to a fixpoint. The fourth is one that a string names under a manifest key of an applied convention row, as grammar/analysis.md states. An installed manifest is the package's manifest in the nearest node_modules directory at or above the target, and a dependency with none is decided by the import closure alone.",
	},
	"DS1605": {
		rule:         "A replace directive whose target module is absent from the build list, the one case in which the directive is exactly a no-op.",
		precondition: "Only a replace whose target is absent from the build list. No other module-file directive is reported. An exclude directive is not, because not selected is a counterfactual about a resolution that did not happen. A workspace use entry and a tool directive are not, because each is an external entry point whose invocation lives outside anything the analysis reads.",
	},
	"DS1701": {
		rule: "A suppression that carries no reason, in any of the three documents: an inline directive, an ignore-file entry or a baseline row. For the ignore file and the baseline, an entry or a row whose reason field is absent, empty or whitespace only is refused and reported rather than matched.",
	},
	"DS1702": {
		rule: "An ignore-file entry or a baseline row that names a symbol and no file path. The entry is reported rather than matched, so a bare name cannot mask a match anywhere else in the project. The inline directive is scoped by its position and cannot be unscoped.",
	},
	"DS1703": {
		rule: "A suppression that matches no current finding, at either mechanism and in the baseline alike. The run exits with the findings code when at least one is reported. The kind is fixed on at deny: no flag, no severity setting and no per-mechanism exception reduces it below a finding, and a configuration naming this code under a severity key is an unimplemented key.",
	},
	"DS1704": {
		rule: "A configured root that matches no symbol, or a configured root pattern that matches no symbol. A root set nobody checks silently changes every result, so a stale one is a finding. The kind is fixed on at deny: no flag, no severity setting and no exception reduces it below a finding, and a configuration naming this code under a severity key is an unimplemented key.",
	},
	"DS1705": {
		rule: "A declared cross-language edge with a side that at least one analyzer evaluated and every evaluation of that side reports absent, so no analyzer that ran enumerates that side's symbol. Emitted by the merge, once per edge, never by an analyzer, so a stale edge is visible rather than silently inert. A dead side paired with an absent side drops its pending finding, and the edge is the reported defect: a misspelled reference must not turn a live pairing into a deletion.",
	},
	"DS1706": {
		rule: "An entry of a configuration key that names a declaration, being an entry of ts.injection_registrations, of the components or the bases of an entry of ts.lifecycle_contracts, or of ts.serializers, that names no declaration in any project of the run, by the resolution the ts section of contract/config.schema.json states for its shape. An entry that names a declaration no call reaches matches. An exemption whose configured declarations nobody checks silently retains nothing, so a stale entry is a finding. The kind is fixed on at deny: no flag, no severity setting and no exception reduces it below a finding, and a configuration naming this code under a severity key is an unimplemented key.",
	},
	"DS1801": {
		rule:         "A parameter with no reference inside its function body, on a function whose signature is free to change.",
		precondition: "The signature must be free, defined as follows: the function is not a method retained by interface satisfaction, is not used as a value, is not a go:linkname or cgo target, is not a function the Go test driver runs (a test, benchmark or fuzz test of a test file, or TestMain), and is not a stub whose body is empty or only panics. A parameter its body never names is dead whatever the callers, so a published declaration of a library is reported too, with the fixability the vocabulary gives the kind: the signature change is a breaking change. In TypeScript a caller may pass a function used as a value more arguments than it declares parameters, so in such a function whose signature is free but for that use, a parameter its body never reads is reported when its body reads no parameter after it, and an unread parameter before the last read one is not. A TypeScript parameter that is a binding pattern is judged by the names it binds: each name the body never reads is reported wherever the pattern stands, because removing it from the pattern moves no argument, except a name beside a rest element of the same object pattern, whose removal changes what that element holds, and the pattern counts as a read parameter when the body reads any name it binds. A rest parameter is judged as the last parameter. A TypeScript `this` parameter declares the type of `this` and receives no argument, so it is never reported. No name exempts a parameter. A TypeScript parameter whose name starts with an underscore is judged as any other is, and a Go parameter named by the blank identifier declares no name a body could read and is never reported.",
	},
	"DS1802": {
		rule:         "A named method receiver with no reference inside the method body, on a method whose signature is free to change. Go permits a method with no receiver name, so the fix deletes an identifier and changes no signature.",
		precondition: "The same free-signature rule as unused-parameter, applied to the receiver.",
	},
	"DS1803": {
		rule:         "A result value that no call site of the function uses, on a function whose signature is free to change.",
		precondition: "The same free-signature rule as unused-parameter, plus a closed-world condition: every call site of the function must be in the loaded graph, because an unknown caller may consume the result, so the kind reports only for a function whose callers are all visible.",
	},
	"DS1805": {
		rule: "A statement that control flow cannot reach, at the precision the language's compiler applies to the same construct.",
	},
	"DS1807": {
		rule:         "A write to a local variable with no read before the next write to it or the end of its scope, computed on the same read-and-write classification the write-only-symbol kind uses. The finding names the write position.",
		precondition: "In TypeScript the binding of a catch clause is a local variable the clause writes when it catches, so a binding that is a name and that the clause's block never reads is a dead store, positioned at the binding: the clause written with no binding behaves the same.",
	},
	"DS1809": {
		rule: "A switch case whose type or value an earlier case in the same switch already covers.",
	},
}

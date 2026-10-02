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
		rule: "A struct field, class member or type member with no reference, a private member included. A member referenced only from its own declaration site counts as unreferenced.",
	},
	"DS1004": {
		rule: "A symbol with zero production references and at least one test reference: an unused-exported, unused-unexported or unused-member candidate whose test reference count is not zero, reported once under this code. A reference from a consumer's test files is a test reference unless the configuration counts consumer tests as production.",
	},
	"DS1005": {
		rule: "A test symbol whose set of referenced target symbols is non-empty and every member of that set is reported dead. A test that references at least one live target symbol is never reported, no notion of a test's subject and no name matching enters the rule, and the message states the rule. The test joins the dead component of the symbols it references, and it and each of them count as referencing each other, so the test is a member of their cycle and never a dead symbol outside it.",
	},
	"DS1006": {
		rule: "A symbol carrying a deprecation marker and no production reference: an unused-exported, unused-unexported or unused-member candidate whose symbol is deprecated, reported once under this code. Go identifies the marker by the convention the standard tools recognize, TypeScript by the documentation tag its tools recognize.",
	},
	"DS1101": {
		rule:         "An exported symbol whose every reference is inside the symbol's own package or module, reported as a candidate for unexporting. The subject is a package-level declaration or a method, and three subjects are excluded: an interface method, whose exportedness is the contract of the interface that declares it; a struct field, which an encoder reads by name; and a method that satisfies an interface some symbol uses as a type, which cannot be unexported without its type ceasing to satisfy that interface. The finding names the narrower visibility the references support. A declared cross-language edge counts as an out-of-package reference; where the edge's other side is unknown to the analyzer the finding is emitted pending.",
		precondition: "Closed world only. Reported always for a main package and an internal/ directory tree, and for a published package only when the configuration declares the consumer set complete and every declared consumer loads. A library with no consumer loaded and no complete consumer set declared gets this finding on its internal/ tree and its main packages and never on its published API. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first.",
	},
	"DS1102": {
		rule:         "An exported symbol of a non-internal package whose every reference is inside the target module, reported as a candidate for relocation behind an internal boundary. The subject is a package-level declaration or a method, and three subjects are excluded: an interface method, whose exportedness is the contract of the interface that declares it; a struct field, which an encoder reads by name; and a method that satisfies an interface some symbol uses as a type, which cannot be unexported without its type ceasing to satisfy that interface. The finding names the narrower visibility the references support. A declared cross-language edge counts as an out-of-package reference; where the edge's other side is unknown to the analyzer the finding is emitted pending.",
		precondition: "Closed world only. Reported always for a main package and an internal/ directory tree, and for a published package only when the configuration declares the consumer set complete and every declared consumer loads. A library with no consumer loaded and no complete consumer set declared gets this finding on its internal/ tree and its main packages and never on its published API. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first.",
	},
	"DS1103": {
		rule: "An unused exported symbol in a package or file no external code can import: an unused-exported candidate whose enclosing package or file is unimportable from outside, reported once under this code at the certain class, because unimportability is a property of the package graph rather than of the consumer set.",
	},
	"DS1104": {
		rule:         "An exported declaration used only inside its own file, reported as a candidate for removing the export keyword. The finding names the narrower visibility the references support.",
		precondition: "Closed world only. Reported in a file that is not an entry file and that either belongs to a project whose consumer set the configuration declares complete or is reached by no manifest export. A declared cross-language edge counts as a reference from outside the file; where the edge's other side is unknown to the analyzer the finding is emitted pending. A symbol the rule of DS1301 holds for is not reported under this code, whatever severity the configuration gives DS1301, because the write-only finding names the defect to fix first.",
	},
	"DS1201": {
		rule: "An interface no symbol uses as a type, counting uses from every loaded module. The finding names the concrete implementations and their positions, and the interface's members join its dead component rather than being reported as independent unused declarations.",
	},
	"DS1203": {
		rule:         "An interface method that no call site invokes or selects through the interface, whatever the number of implementations. The finding names the concrete implementations and their positions.",
		precondition: "Exempt: in Go, every method of an interface that declares an unexported method, the sum-type shape whose method set exists to restrict the implementors, a shape a TypeScript interface cannot take because it declares no member less visible than itself; and, in both languages, every marker method, an interface method whose every implementation carries an empty body.",
	},
	"DS1204": {
		rule: "A compile-time satisfaction assertion whose interface no symbol uses as a type.",
	},
	"DS1301": {
		rule: "A package-level or module-level variable, struct field, class member or collection that production code writes and never reads. In production mode a read from a test file counts as no read. A comparison of struct values reads every field of the type: in Go, a value of a struct type used as an operand of `==` or `!=`, as a map key, or as a `switch` tag or `case` expression is a read of every field of that type, transitively through the fields of every struct type it holds, recorded at the comparison, because equality reads every field and a field that decides equality carries information. In TypeScript a comparison of two object references reads no member, because `===` compares identity and never a member. The finding names each write position, so the deletion set is visible.",
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
		precondition: "Reported only when the configuration declares the build matrix complete, and never for a file the toolchain ignored solely because it imports \"C\" under a build with cgo disabled; such a file is recorded as excluded by cgo rather than as never built.",
	},
	"DS1502": {
		rule: "A source file that no import reaches, that no root names, and in which no declared cross-language edge names a declaration. An import written in a loaded consumer reaches a file of the target as an import written in the target does. An edge's side names a declaration as a root names one, so a file holding a declaration an edge names is evaluated with the edge rather than reported: whether that declaration is dead is the edge evaluation's to say, and the merge resolves it against the paired side.",
	},
	"DS1601": {
		rule:         "A directly declared dependency that no import in the target needs. On the Go side, a direct require whose module provides no package any target package or test variant imports. On the TypeScript side, a manifest dependency, development dependency or peer dependency the project's import closure does not need. A deletion finding whose fix would remove the last use of a dependency names that dependency.",
		precondition: "On the Go side the rule is the semantics of `go mod tidy -diff` exactly: a requirement marked indirect is never reported, because it exists to pin a transitive version and removing it changes the build list. On the TypeScript side the import closure is the files of the run's projects, and three more dependencies are needed: one whose installed manifest declares a command, because a command is run by name rather than imported; one that the installed manifest of a needed dependency declares as a peer and does not mark optional; and, to a fixpoint, the required peers of every dependency so held. An installed manifest is the package's manifest in the nearest node_modules directory at or above the target, and a dependency with none is decided by the import closure alone.",
	},
	"DS1605": {
		rule:         "A replace directive whose target module is absent from the build list, the one case in which the directive is exactly a no-op.",
		precondition: "Only a replace whose target is absent from the build list. No other module-file directive is reported: not an exclude directive, because not selected is a counterfactual about a resolution that did not happen; not a workspace use entry and not a tool directive, because each is an external entry point whose invocation lives outside anything the analysis reads.",
	},
	"DS1701": {
		rule: "A suppression that carries no reason, in any of the three documents: an inline directive, an ignore-file entry or a baseline row. For the ignore file and the baseline, an entry or a row whose reason field is absent, empty or whitespace only is refused and reported rather than matched.",
	},
	"DS1702": {
		rule: "An ignore-file entry or a baseline row that names a symbol and no file path. The entry is reported rather than matched, so a bare name cannot mask a match anywhere else in the project; the inline directive is scoped by its position and cannot be unscoped.",
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
		precondition: "The signature must be free, defined as follows: the function is not a method retained by interface satisfaction, is not used as a value, is not a go:linkname or cgo target, and is not a stub whose body is empty or only panics. A parameter its body never names is dead whatever the callers, so a published declaration of a library is reported too, with the fixability the vocabulary gives the kind: the signature change is a breaking change.",
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
		rule: "A write to a local variable with no read before the next write to it or the end of its scope, computed on the same read-and-write classification the write-only-symbol kind uses. The finding names the write position.",
	},
	"DS1809": {
		rule: "A switch case whose type or value an earlier case in the same switch already covers.",
	},
}

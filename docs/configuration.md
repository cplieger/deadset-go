# Configuration and invocation

Everything deadset-go reads is strict JSON or a command-line flag. The tool accepts no other syntax, comment form or converter from another tool's format. A key the contract's schema does not declare ends the run with the usage code naming the key, so no configuration is silently ignored.

## The sources, and which wins

Three sources supply a setting, and the higher-ranked one wins per setting:

1. A command-line flag.
2. The repository configuration, `deadset.json` at the target root, or the file `--config` names in its place.
3. The central configuration, the file `--central` names.

Under all three sits the default the contract's schema declares for the key. No source has to be complete, and none of them is required, but the resolved configuration must name a target kind. A run whose sources all omit `target.kind` exits with the usage code naming the field and the two files it looked in.

`deadset-go print-config` prints the resolved configuration with the source of every setting under a `provenance` object keyed by the setting's dotted path. That output reads back as a repository configuration. `provenance` is accepted on input and ignored by resolution, so printing and re-reading changes no resolved value.

A section's keys are replaced one by one, so a repository configuration naming one key of `analysis` leaves the rest at their defaults. `analysis.template_delimiters` is one setting rather than a section, so a higher-ranked source replaces both of its members together.

## Settings

Required. The analysis branches on this field alone and never on whether the target declares an executable entry point.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `target.kind` | `application` or `library`, or an object naming the kind of `go` and of `ts` | none | Whether every caller is in the analyzed graph, or the published API has callers outside it |

A repository that holds Go and TypeScript code of different kinds writes the object, for example `{"go": "application", "ts": "library"}`, and names both languages. deadset-go reads the `go` member. A string is the kind of both languages.

The analysis.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `analysis.min_confidence` | `certain`, `probable`, `possible` | `probable` | The lowest confidence a finding is reported at. The default withholds the findings about a library's published API when no consumer is declared. A finding below it is not reported, and when it is a root of its dead component no finding of that component is reported. The exception is a finding that is `possible` only because production code only tests use it, as [kinds](kinds.md) `DS1102` describes, which is withheld on its own. `totals.withheld` counts what the minimum withheld at each confidence, and the text and SARIF outputs name those counts in one line |
| `analysis.generated_files` | `exclude`, `include` | `exclude` | Whether declarations in generated files are judged. `include` reports them and marks every such finding as one no mechanical edit may act on |
| `analysis.consumer_tests` | `test`, `production` | `test` | How a reference from a loaded consumer's test file counts |
| `analysis.configurations` | array of objects | `[]` | The build matrix. A platform entry names an `id`, an `os`, an `arch` and optional `tags`. A project entry names an `id` and a TypeScript `project` file, which this analyzer ignores. With no platform entry the matrix derives from the target tree |
| `analysis.matrix.complete` | boolean | `false` | Declares that the platform entries of `analysis.configurations` are every configuration the target builds, which `DS1601` needs |
| `analysis.template_dirs` | array of strings | `[]` | Directories, relative to the target root, the `template-field` exemption scans |
| `analysis.template_delimiters` | object with `left` and `right` | `{{` and `}}` | The action delimiters that scan parses a template with |
| `consumers.complete` | boolean | `false` | Declares that every consumer of the published API is declared, which the narrowing kinds need |
| `roots.patterns` | array of strings | `[]` | Symbol references, or patterns over them, whose matches are roots |

Severity and exemptions.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `severity` | object | `{}` | Per-kind severity. A key is a code or a two-digit family prefix naming at least one kind, a value is `allow`, `warn` or `deny`. A finding at `allow` is not reported, and when it is a root of its dead component no finding of that component is reported |
| `exemptions.disabled` | array of strings | `[]` | Exemption classes that do not run |

Reporting.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `reporters.formats` | array of `text`, `json`, `github`, `sarif`, `template` | `["text"]` | The renderings written beside the report |
| `reporters.sort` | `position`, `size` | `position` | The order findings are rendered in. `size` orders by deletable lines, largest first |
| `reporters.cascade` | `roots`, `full` | `roots` | Whether a rendering lists a dead component's root members or every member |
| `reporters.max_findings` | integer | `0` | The most findings a rendering prints, `0` being all of them. The report names the number omitted |
| `reporters.fail_on` | `allow`, `warn`, `deny` | `deny` | The lowest severity that fails the run. A finding at `allow` is never reported, so `allow` fails on what `warn` fails on |

Keys this analyzer reads nothing from. It validates each one and prints it in the resolved configuration, so a document that one product refuses every product refuses.

| Key | Type | Default | What it is |
| --- | --- | --- | --- |
| `contract_version` | semantic version | this analyzer's contract version | The contract version a configuration is written against. An absent key means this analyzer's own |
| `analysis.languages` | array of `go`, `ts` | `[]` | The orchestrator's: the languages in scope. An empty array means the orchestrator detects them from the target tree |
| `providers.analyzers` | array of objects | `deadset-go` and `deadset-ts` | The orchestrator's: the analyzers it runs. An entry names a `name`, its `languages` and a `command`, and an entry the orchestrator acquires also names a `source`, a `version` and a `digest` |
| `ts.test_files` | array of strings | `["**/*.test.{ts,tsx,mts,cts}", "**/*.test-d.{ts,tsx,mts,cts}", "**/*.spec-d.{ts,tsx,mts,cts}", "**/*.spec.*", "**/__tests__/**", "**/__mocks__/**", "**/test-d/**"]` | The TypeScript analyzer's: glob patterns naming its test files |
| `ts.entry_files` | array of strings | `[]` | The TypeScript analyzer's: glob patterns naming files whose exports are roots |
| `ts.component_extensions` | array of strings | `[".vue", ".svelte", ".astro"]` | The TypeScript analyzer's: the extensions of component files, each a full stop followed by letters and digits |
| `ts.disabled_conventions` | array of strings | `[]` | The TypeScript analyzer's: the convention rows that do not apply, each lowercase words joined by hyphens |
| `ts.injection_registrations` | array of declaration entries | `[]` | The TypeScript analyzer's: the declarations whose call registers a class with a dependency-injection container |
| `ts.lifecycle_contracts` | array of objects | `[]` | The TypeScript analyzer's: one framework's lifecycle contract per entry, naming its `components`, its `bases` and the `members` the framework calls |
| `ts.serializers` | array of declaration entries | `[]` | The TypeScript analyzer's: the declarations whose call reads its arguments' data members by name |
| `ts.roots_complete` | boolean | `false` | The TypeScript analyzer's: whether the roots the analysis finds are every root the target has, so a finding under reachability alone is `certain` rather than `probable` |

A declaration entry names one declaration by `symbol`, by `module` and `name`, or by `global`. The contract's [configuration schema](https://github.com/cplieger/deadset-spec/blob/v7.0.0-dev.3/contract/config.schema.json) states each shape. `go` is the section this analyzer owns, and it declares no key in this contract version.

A pattern in `roots.patterns` is matched against the reference of every symbol the analysis enumerates. In a pattern, `*` matches any run of characters including the solidus. `?` matches exactly one character, counted as a Unicode code point, and no other character is special. An entry holding neither wildcard matches only the symbol whose reference it spells exactly. An entry that matches nothing is reported as `DS9004`.

### Naming a severity the contract fixes

The severity of `DS9003` and `DS9004` is fixed. A severity key naming either code, or the family prefix `DS17` whose range holds them, is an unimplemented key and exits with the usage code. So no configuration reduces a stale suppression or an unmatched root below a finding.

## Verbs and flags

| Verb | What it does |
| --- | --- |
| `analyze` | Loads the target and its declared consumers, writes the report, exits with the verdict |
| `explain` | Says why one symbol is dead, why it is live, or why it was not reported |
| `print-config` | Prints the resolved configuration with the source of every setting |
| `print-roots` | Prints the resolved root set |
| `print-retained` | Prints every symbol an exemption held back, with the classes that held it |
| `describe` | Prints the analyzer, the contract version, the schema versions and the conformance record as JSON |
| `version` | Prints the analyzer version and the contract version |

Every verb that resolves a configuration takes `--target` for the target root, `--config` and `--central` for the two configuration files, and `--min-confidence`. `analyze` and `explain` also take `--scope` for the scope document naming the target and its declared consumers.

Five flags supply a setting and outrank both configuration files for it:

| Flag | Setting |
| --- | --- |
| `--min-confidence` | `analysis.min_confidence` |
| `--sort` | `reporters.sort` |
| `--cascade` | `reporters.cascade` |
| `--max-findings` | `reporters.max_findings` |
| `--fail-on` | `reporters.fail_on` |

The rest belong to the invocation and commit nothing to a configuration file:

- `--report` names the path the JSON report is written to.
- `--format` names one rendering written beside the report, and repeats. Naming any format replaces the default `text` rendering, so add `--format=text` to keep it.
- `--template` names the file the template rendering reads. The file holds a template in the contract's [template subset](https://github.com/cplieger/deadset-spec/blob/v7.0.0-dev.3/contract/grammar/template.md), which runs over the JSON report and names its members as the report spells them. A file that cannot be read or does not parse refuses the invocation with 2 before any analysis, and a rendering that fails exits 3 with the report already written.
- `--baseline-write` names the path a baseline recording the findings of the target is written to.
- `--exit-code=off` writes every document and exits clean.

`print-retained` takes `--mode=production` or `--mode=plain`.

No verb accepts a flag that asks for a source edit. A flag whose name carries `fix`, `edit`, `delete` or `rewrite` exits with the usage code naming the flag, whatever the verb.

## Suppressing a finding

Three mechanisms, all of them requiring a reason. The grammar is stated in full in the contract's [suppression page](https://github.com/cplieger/deadset-spec/blob/v7.0.0-dev.3/contract/grammar/suppression.md).

### Inline directive

An inline directive is the first token of a `//` line comment above or beside the declaration:

```go
//deadset:ignore DS1001 -- Kept for the v3 API promise; removing it is a major bump.
```

| Where the directive sits | What it binds |
| --- | --- |
| Alone on the line immediately above the line where a declaration begins | That declaration |
| After the code on the line where a declaration begins | That declaration |
| Alone on the line immediately above a parameter or result on a later line of a wrapped signature | The function that declares the parameter or result |
| After a parameter or result on a later line of a wrapped signature, on that line | The function that declares the parameter or result |

A directive anywhere else binds nothing and is reported as a stale suppression. That covers a directive two lines up, one below the declaration, one beside code where no declaration begins, and one above another comment.

Both `//deadset:ignore` and `// deadset:ignore` parse to the same directive. The separator is exactly `--`, and the reason is everything after it to the end of the line.

### Ignore file

The ignore file is `deadset-ignore.json` at the target root. An absent file is an empty one. Each entry names the code, the stable symbol reference, the path of the file holding the declaration relative to the target root, and the reason:

```json
{
  "description": "Adjudications for example.com/app.",
  "ignore": [
    {
      "code": "DS1001",
      "symbol": "go://example.com/app#Catalog.ResolveAlias",
      "path": "catalog.go",
      "reason": "Kept for the v3 API promise; removing it is a major bump. Revisit at v4."
    }
  ]
}
```

An entry matches only when its code, its symbol reference and its path all equal the finding's own. An entry supports no glob, regular expression, bare name or substring match. So an adjudication written for one symbol in one file masks nothing else.

An entry can also keep a `DS1701` or `DS1702` finding about a requirement or a `replace` directive of `go.mod`, named by the `symbol.ref` and `position.path` the finding carries. An entry or a baseline row whose `symbol` starts with `ts://` belongs to the TypeScript analyzer: this analyzer checks its form, and it binds nothing here and is never reported stale here.

### Baseline

The baseline is `deadset-baseline.json` at the target root, written by `--baseline-write` and read back by a later run. It records a finding set so that only a finding absent from it fails the run, which is a ratchet on the total rather than an adjudication of any one finding. The write analyzes the target with no baseline read, then again with the rows recorded so far read back. Each round records the findings it exposes and removes every row it reports stale, and the write stops when a round records and removes nothing.

The document read back on the tree it was written from then reports no finding a row can withhold and no stale row. No row is recorded for a finding about a file, a suppression record or a configured root, which no record binds to. Such a finding fails a run after the baseline is written as it did before. A dependency finding gets its row. A write keeps every `ts://` row the document holds, after the `go://` rows, so the two analyzers can share one baseline.

### Rules for all three

Four rules bind all three mechanisms:

- A record binds to a declaration. A part of a declaration, which is a parameter, a receiver, a result, a statement, a store or a case, is suppressed through the declaration its own reference names. An unused parameter's finding names it by the function's reference followed by the parameter's name in parentheses: an entry or row with that reference holds that parameter alone, and one with the function's reference holds every parameter of it. The record withholds the part's finding without marking that declaration live. A directive for a parameter declared on the function's first line sits above the function. One for a parameter on a later line of a wrapped signature sits above that parameter, inside the list. A finding about a file no configuration built, a suppression record or a configured root has no suppression at all. An entry naming one matches nothing and is reported as `DS9003`. The remedy there is the change the finding names, or setting the severity of its code to `allow`. A finding about a requirement or a `replace` directive of the module file is kept only by an ignore entry or a baseline row, never by a directive. Its other remedies are `go mod tidy` for an unused requirement and deleting a `replace` that redirects nothing.
- A record with no reason is refused and reported as `DS9001`. The refusal binds nothing, so the finding it was meant to cover stays reported beside it.
- An ignore entry or baseline row naming a symbol and no path is refused and reported as `DS9002`. A record lacking both is two findings at one position.
- A record that matched no current finding is reported as `DS9003` and fails the run.

## Exit codes

| Code | Name | What it means |
| --- | --- | --- |
| 0 | clean | No finding at or above the failing severity, no stale suppression and no pending finding. Also what `--exit-code=off` returns while still writing every document |
| 1 | findings | At least one finding at or above the failing severity, or at least one stale suppression |
| 2 | usage | The invocation is malformed, no source supplied the target kind, a source named a key this analyzer does not implement, an explanation named a symbol that does not exist, or a flag asked for a source edit. No analysis runs |
| 3 | failure | A setup failure, a load failure or a lack of memory stopped the analysis. The errors are printed and no finding list is |
| 4 | pending | At least one pending finding, whose cross-language edge the other side has not evaluated. Such a report is an input to a merge rather than an answer |

A run that needs more memory than the machine makes available prints one line on stderr: `memory exhausted: at least N GB were needed, M GB are available`. N is the heap the run was about to need, rounded up, and M is the memory available to it, rounded down, both in decimal gigabytes. M is read from the tightest cgroup limit of the process and its parents, less their working set, or else from the system's available memory. A Go memory limit the process starts with (`GOMEMLIMIT`) bounds M too, so a run never takes more than that limit allows. Without `GOMEMLIMIT`, deadset-go sets its own soft memory limit of at most 70% of the tightest cgroup memory limit (`memory.max`, or `memory.limit_in_bytes` under cgroup v1), or else of the machine's physical memory. A run on a machine with free memory then stays bounded while it collects garbage less often. An explicit `GOMEMLIMIT`, `off` included, replaces that default.

A run whose own report breaks the contract's report schema also exits 3. The report is not written, and the error names the JSON Pointer of the first value the schema refuses.

Codes 2 and 3 end a run before any verdict exists. Codes 4, 1 and 0 are verdicts about a complete report, and the highest applicable code wins. Under the default `reporters.fail_on`, `deny`, a `warn` finding never fails a run.

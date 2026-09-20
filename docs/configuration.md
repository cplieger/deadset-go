# Configuration and invocation

Everything deadset-go reads is strict JSON or a command-line flag. There is no other syntax, no
comment form and no converter from another tool's format; a key the contract's schema does not
declare ends the run with the usage code naming the key, so no configuration is silently ignored.

## The sources, and which wins

Three sources supply a setting, and the higher-ranked one wins per setting:

1. A command-line flag.
2. The repository configuration, `deadset.json` at the target root, or the file `--config` names
   in its place.
3. The central configuration, the file `--central` names.

Under all three sits the default the contract's schema declares for the key. No source has to be
complete, and none of them is required, but the resolved configuration must name a target kind: a
run whose sources all omit `target.kind` exits with the usage code naming the field and the two
files it looked in.

`deadset-go print-config` prints the resolved configuration with the source of every setting under
a `provenance` object keyed by the setting's dotted path. That output reads back as a repository
configuration: `provenance` is accepted on input and ignored by resolution, so printing and
re-reading changes no resolved value.

A section's keys are replaced one by one, so a repository configuration naming one key of
`analysis` leaves the rest at their defaults. `analysis.template_delimiters` is one setting rather
than a section: a higher-ranked source replaces both of its members together.

## Settings

Required. The analysis branches on this field alone and never on whether the target declares an
executable entry point.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `target.kind` | `application` or `library` | none | Whether every caller is in the analyzed graph, or the published API has callers outside it |

The analysis.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `analysis.min_confidence` | `certain`, `probable`, `possible` | `possible` | The lowest confidence a finding is reported at. A finding below it is not reported and no count of the report stands for it |
| `analysis.generated_files` | `exclude`, `include` | `exclude` | Whether declarations in generated files are judged. `include` reports them and marks every such finding as one no mechanical edit may act on |
| `analysis.consumer_tests` | `test`, `production` | `test` | How a reference from a loaded consumer's test file counts |
| `analysis.configurations` | array of objects | `[]` | The build matrix, each entry naming an `id`, an `os`, an `arch` and optional `tags`. Empty derives the matrix from the target tree |
| `analysis.matrix.complete` | boolean | `false` | Declares that `analysis.configurations` lists every configuration the target builds, which `DS1501` needs |
| `analysis.template_dirs` | array of strings | `[]` | Directories, relative to the target root, the `template-field` exemption scans |
| `analysis.template_delimiters` | object with `left` and `right` | `{{` and `}}` | The action delimiters that scan parses a template with |
| `consumers.complete` | boolean | `false` | Declares that every consumer of the published API is declared, which the narrowing kinds need |
| `roots.patterns` | array of strings | `[]` | Symbol references, or patterns over them, whose matches are roots |

Severity and exemptions.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `severity` | object | `{}` | Per-kind severity. A key is a code or a two-digit family prefix, a value is `allow`, `warn` or `deny` |
| `exemptions.disabled` | array of strings | `[]` | Exemption classes that do not run |

Reporting.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `reporters.formats` | array of `text`, `json`, `github`, `sarif`, `template` | `["text"]` | The renderings written beside the report |
| `reporters.sort` | `position`, `size` | `position` | The order findings are rendered in. `size` orders by deletable lines, largest first |
| `reporters.cascade` | `roots`, `full` | `roots` | Whether a rendering lists a dead component's root members or every member |
| `reporters.max_findings` | integer | `0` | The most findings a rendering prints, `0` being all of them. The report names the number omitted |
| `reporters.fail_on` | `allow`, `warn`, `deny` | `deny` | The lowest severity that fails the run |

Two further keys exist and this analyzer has nothing to read in them. `contract_version` names the
contract version a configuration is written against, and an absent key means this analyzer's own.
`go` is the section this analyzer owns and it declares no key of its own in this contract version.

A pattern in `roots.patterns` is matched against the reference of every symbol the analysis
enumerates: `*` matches any run of characters including the solidus, `?` matches exactly one
character counted as a Unicode code point, no other character is special, and an entry holding
neither wildcard matches only the symbol whose reference it spells exactly. An entry that matches
nothing is reported as `DS1704`.

### Naming a severity the contract fixes

The severity of `DS1703` and `DS1704` is fixed. A severity key naming either code, or the family
prefix `DS17` whose range holds them, is an unimplemented key and exits with the usage code, so no
configuration reduces a stale suppression or an unmatched root below a finding.

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

Every verb that resolves a configuration takes `--target` for the target root, `--config` and
`--central` for the two configuration files, and `--min-confidence`. `analyze` and `explain` also
take `--scope` for the scope document naming the target and its declared consumers.

Five flags supply a setting and outrank both configuration files for it:

| Flag | Setting |
| --- | --- |
| `--min-confidence` | `analysis.min_confidence` |
| `--sort` | `reporters.sort` |
| `--cascade` | `reporters.cascade` |
| `--max-findings` | `reporters.max_findings` |
| `--fail-on` | `reporters.fail_on` |

The rest are the invocation's own and commit nothing to a configuration file: `--report` names the
path the JSON report is written to, `--format` names one rendering written beside it and repeats,
`--template` names the file the template rendering reads, `--baseline-write` names the path a
baseline recording every finding of the run is written to, and `--exit-code=off` writes every
document and exits clean. `print-retained` takes `--mode=production` or `--mode=plain`.

No verb accepts a flag that asks for a source edit. A flag whose name carries `fix`, `edit`,
`delete` or `rewrite` exits with the usage code naming the flag, whatever the verb.

## Suppressing a finding

Three mechanisms, all of them requiring a reason. The grammar is stated in full in the contract's
[suppression page](https://github.com/cplieger/deadset-spec/blob/v2.0.0/contract/grammar/suppression.md).

**An inline directive** is the first token of a `//` line comment above or beside the declaration:

```go
//deadset:ignore DS1001 -- Kept for the v3 API promise; removing it is a major bump.
```

Both `//deadset:ignore` and `// deadset:ignore` parse to the same directive. The separator is
exactly `--`, and the reason is everything after it to the end of the line.

**The ignore file** is `deadset-ignore.json` at the target root. An absent file is an empty one.
Each entry names the code, the stable symbol reference, the path of the file holding the
declaration relative to the target root, and the reason:

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

An entry matches only when its code, its symbol reference and its path all equal the finding's own.
There is no glob, no regular expression, no bare name and no substring match, so an adjudication
written for one symbol in one file masks nothing else.

**The baseline** is `deadset-baseline.json` at the target root, written by `--baseline-write` and
read back by a later run. It records a finding set so that only a finding absent from it fails the
run, which is a ratchet on the total rather than an adjudication of any one finding.

Four rules bind all three mechanisms:

- A record binds to a declaration. A part of a declaration, which is a parameter, a receiver, a
  result, a statement, a store or a case, is suppressed through the declaration its own reference
  names. A finding about a record of a document rather than about a declaration of the program,
  which is a requirement or a `replace` directive of the module file, a file no configuration
  built, a suppression record or a configured root, has no suppression at all: an entry naming one
  matches nothing and is reported as `DS1703`. The remedy there is the change the finding names,
  `go mod tidy` for an unused requirement and deleting a `replace` that redirects nothing, or
  setting the severity of its code to `allow`.
- A record with no reason is refused and reported as `DS1701`. The refusal binds nothing, so the
  finding it was meant to cover stays reported beside it.
- An ignore entry or baseline row naming a symbol and no path is refused and reported as `DS1702`.
  A record lacking both is two findings at one position.
- A record that matched no current finding is reported as `DS1703` and fails the run.

## Exit codes

| Code | Name | What it means |
| --- | --- | --- |
| 0 | clean | No finding at or above the failing severity, no stale suppression and no pending finding. Also what `--exit-code=off` returns while still writing every document |
| 1 | findings | At least one finding at or above the failing severity, or at least one stale suppression |
| 2 | usage | The invocation is malformed, no source supplied the target kind, a source named a key this analyzer does not implement, an explanation named a symbol that does not exist, or a flag asked for a source edit. No analysis runs |
| 3 | failure | The target, a declared consumer or a build configuration failed to load or type-check. The errors are printed and no finding list is |
| 4 | pending | At least one pending finding, whose cross-language edge the other side has not evaluated. Such a report is an input to a merge rather than an answer |

Codes 2 and 3 end a run before any verdict exists. Codes 4, 1 and 0 are verdicts about a complete
report, and the highest applicable code wins. A `warn` finding never fails a run.

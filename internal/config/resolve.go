package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ErrNoSourceLabel reports a resolved setting whose source names no file or flag.
// A caller fixes it by naming every document and flag it passed, which the
// Contract's provenance syntax requires.
var ErrNoSourceLabel = errors.New("the source of a resolved setting names no file or flag")

// flagsLabel names the command-line flags in a refusal, where a document is named
// by its path.
const flagsLabel = "the command-line flags"

// doc is one configuration document. Every setting is a pointer, so a key the
// document omits is distinguishable from one it sets to a zero value, which is
// what makes resolution per setting rather than per document.
type doc struct {
	ContractVersion *string             `json:"contract_version"`
	Target          *docTarget          `json:"target"`
	Analysis        *docAnalysis        `json:"analysis"`
	Consumers       *docConsumers       `json:"consumers"`
	Roots           *docRoots           `json:"roots"`
	Severity        map[string]Severity `json:"severity"`
	Exemptions      *docExemptions      `json:"exemptions"`
	Reporters       *docReporters       `json:"reporters"`
	Go              *Go                 `json:"go"`
	TS              *docTS              `json:"ts"`
	Provenance      map[string]string   `json:"provenance"`
}

type docTarget struct {
	Kind *TargetKind `json:"kind"`
}

type docAnalysis struct {
	Languages          *[]Language         `json:"languages"`
	MinConfidence      *Confidence         `json:"min_confidence"`
	GeneratedFiles     *GeneratedFiles     `json:"generated_files"`
	ConsumerTests      *ConsumerTests      `json:"consumer_tests"`
	Configurations     *[]Configuration    `json:"configurations"`
	Matrix             *docMatrix          `json:"matrix"`
	TemplateDirs       *[]string           `json:"template_dirs"`
	TemplateDelimiters *TemplateDelimiters `json:"template_delimiters"`
}

type docMatrix struct {
	Complete *bool `json:"complete"`
}

type docConsumers struct {
	Complete *bool `json:"complete"`
}

type docRoots struct {
	Patterns *[]string `json:"patterns"`
}

type docExemptions struct {
	Disabled *[]string `json:"disabled"`
}

type docReporters struct {
	Formats     *[]Format `json:"formats"`
	Sort        *Sort     `json:"sort"`
	Cascade     *Cascade  `json:"cascade"`
	MaxFindings *int      `json:"max_findings"`
	FailOn      *Severity `json:"fail_on"`
}

type docTS struct {
	TestFiles  *[]string `json:"test_files"`
	EntryFiles *[]string `json:"entry_files"`
}

// fill allocates the sections the document omits, so reading one setting never
// depends on which sections its document happened to carry. A filled section
// holds no setting, so it supplies no value.
func (d *doc) fill() {
	if d.Target == nil {
		d.Target = &docTarget{}
	}
	if d.Analysis == nil {
		d.Analysis = &docAnalysis{}
	}
	if d.Analysis.Matrix == nil {
		d.Analysis.Matrix = &docMatrix{}
	}
	if d.Consumers == nil {
		d.Consumers = &docConsumers{}
	}
	if d.Roots == nil {
		d.Roots = &docRoots{}
	}
	if d.Exemptions == nil {
		d.Exemptions = &docExemptions{}
	}
	if d.Reporters == nil {
		d.Reporters = &docReporters{}
	}
	if d.TS == nil {
		d.TS = &docTS{}
	}
}

// source is one document the resolution reads, with the origin its settings
// carry.
type source struct {
	document   *doc
	flagLabels map[string]string
	label      string
	kind       Source
}

// origin returns the origin one setting of this source carries. A flag names
// itself per setting, because one invocation carries many.
func (s source) origin(path string) Origin {
	if s.kind == SourceFlag {
		return Origin{Source: SourceFlag, Label: s.flagLabels[path]}
	}
	return Origin{Source: s.kind, Label: s.label}
}

// Resolve applies a command-line flag over the repository configuration over the
// central configuration over the default each key declares, and returns the
// resolved configuration with the origin of every setting. It refuses a document
// that names a key the closed key list does not declare, a document that names one
// key twice, and a resolved configuration no source supplied a target kind for,
// each as an *Error carrying the usage exit code.
//
//nolint:gocritic // hugeParam: the surface the CLI calls takes the inputs by value
func Resolve(in Inputs) (Config, Provenance, error) {
	sources, refusal := readSources(&in)
	if refusal != nil {
		return Config{}, nil, refusal
	}

	cfg := Default()
	provenance := make(Provenance)
	resolveRoot(&cfg, provenance, sources)
	resolveAnalysis(&cfg, provenance, sources)
	resolveReporters(&cfg, provenance, sources)
	resolveTS(&cfg, provenance, sources)
	resolveSeverity(&cfg, provenance, sources)

	if cfg.Target.Kind == "" {
		return Config{}, nil, missingTargetKind(&in)
	}
	if err := checkLabels(provenance); err != nil {
		return Config{}, nil, err
	}
	return cfg, provenance, nil
}

// readSources decodes the documents one invocation supplies, in the order they
// outrank each other, skipping every source it does not carry.
func readSources(in *Inputs) ([]source, *Error) {
	sources := make([]source, 0, 3)
	if in.Flags != nil {
		nested, refusal := nestFlags(in.Flags, flagsLabel)
		if refusal != nil {
			return nil, refusal
		}
		document, refusal := decodeChecked(nested, flagsLabel)
		if refusal != nil {
			return nil, refusal
		}
		sources = append(sources, source{document: document, flagLabels: in.FlagLabels, kind: SourceFlag})
	}
	for _, held := range []struct {
		label string
		named string
		kind  Source
		data  []byte
	}{
		{in.RepositoryLabel, "the repository configuration", SourceRepository, in.Repository},
		{in.CentralLabel, "the central configuration", SourceCentral, in.Central},
	} {
		if held.data == nil {
			continue
		}
		document, refusal := decode(held.data, labelOr(held.label, held.named))
		if refusal != nil {
			return nil, refusal
		}
		sources = append(sources, source{document: document, label: held.label, kind: held.kind})
	}
	return sources, nil
}

// labelOr names a document in a refusal: the path the invocation passed, or what
// the document is when the invocation passed none.
func labelOr(label, named string) string {
	if label == "" {
		return named
	}
	return label
}

// decode reads one configuration document written as the closed key list nests
// it: the key list walked, then the document decoded and checked.
func decode(data []byte, label string) (*doc, *Error) {
	if refusal := checkDocument(data, label); refusal != nil {
		return nil, refusal
	}
	return decodeChecked(data, label)
}

// decodeChecked decodes a document whose member names are already known to be
// declared, refusing an unimplemented key under DisallowUnknownFields and then
// every constraint the decoder cannot express.
func decodeChecked(data []byte, label string) (*doc, *Error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var document doc
	if err := dec.Decode(&document); err != nil {
		return nil, malformed(label, "", "%s", err)
	}
	if dec.More() {
		return nil, malformed(label, "", "want one JSON object, a second value follows it")
	}
	document.fill()
	if refusal := validate(&document, label); refusal != nil {
		return nil, refusal
	}
	return &document, nil
}

// resolveSetting assigns the value the highest-ranked source carrying one
// supplies, and records where it came from.
func resolveSetting[T any](into *T, path string, p Provenance, sources []source, read func(*doc) *T) {
	for _, s := range sources {
		if value := read(s.document); value != nil {
			*into = *value
			p[path] = s.origin(path)
			return
		}
	}
	p[path] = Origin{Source: SourceDefault}
}

// resolveRoot resolves the settings the closed key list declares at the root and
// in the one-setting sections.
func resolveRoot(cfg *Config, p Provenance, sources []source) {
	resolveSetting(&cfg.ContractVersion, "contract_version", p, sources,
		func(d *doc) *string { return d.ContractVersion })
	resolveSetting(&cfg.Target.Kind, "target.kind", p, sources,
		func(d *doc) *TargetKind { return d.Target.Kind })
	resolveSetting(&cfg.Consumers.Complete, "consumers.complete", p, sources,
		func(d *doc) *bool { return d.Consumers.Complete })
	resolveSetting(&cfg.Roots.Patterns, "roots.patterns", p, sources,
		func(d *doc) *[]string { return d.Roots.Patterns })
	resolveSetting(&cfg.Exemptions.Disabled, "exemptions.disabled", p, sources,
		func(d *doc) *[]string { return d.Exemptions.Disabled })
}

// resolveAnalysis resolves the analysis section.
func resolveAnalysis(cfg *Config, p Provenance, sources []source) {
	resolveSetting(&cfg.Analysis.Languages, "analysis.languages", p, sources,
		func(d *doc) *[]Language { return d.Analysis.Languages })
	resolveSetting(&cfg.Analysis.MinConfidence, "analysis.min_confidence", p, sources,
		func(d *doc) *Confidence { return d.Analysis.MinConfidence })
	resolveSetting(&cfg.Analysis.GeneratedFiles, "analysis.generated_files", p, sources,
		func(d *doc) *GeneratedFiles { return d.Analysis.GeneratedFiles })
	resolveSetting(&cfg.Analysis.ConsumerTests, "analysis.consumer_tests", p, sources,
		func(d *doc) *ConsumerTests { return d.Analysis.ConsumerTests })
	resolveSetting(&cfg.Analysis.Configurations, "analysis.configurations", p, sources,
		func(d *doc) *[]Configuration { return d.Analysis.Configurations })
	resolveSetting(&cfg.Analysis.Matrix.Complete, "analysis.matrix.complete", p, sources,
		func(d *doc) *bool { return d.Analysis.Matrix.Complete })
	resolveSetting(&cfg.Analysis.TemplateDirs, "analysis.template_dirs", p, sources,
		func(d *doc) *[]string { return d.Analysis.TemplateDirs })
	resolveSetting(&cfg.Analysis.TemplateDelimiters, "analysis.template_delimiters", p, sources,
		func(d *doc) *TemplateDelimiters { return d.Analysis.TemplateDelimiters })
	for index := range cfg.Analysis.Configurations {
		if cfg.Analysis.Configurations[index].Tags == nil {
			cfg.Analysis.Configurations[index].Tags = []string{}
		}
	}
}

// resolveReporters resolves the reporters section.
func resolveReporters(cfg *Config, p Provenance, sources []source) {
	resolveSetting(&cfg.Reporters.Formats, "reporters.formats", p, sources,
		func(d *doc) *[]Format { return d.Reporters.Formats })
	resolveSetting(&cfg.Reporters.Sort, "reporters.sort", p, sources,
		func(d *doc) *Sort { return d.Reporters.Sort })
	resolveSetting(&cfg.Reporters.Cascade, "reporters.cascade", p, sources,
		func(d *doc) *Cascade { return d.Reporters.Cascade })
	resolveSetting(&cfg.Reporters.MaxFindings, "reporters.max_findings", p, sources,
		func(d *doc) *int { return d.Reporters.MaxFindings })
	resolveSetting(&cfg.Reporters.FailOn, "reporters.fail_on", p, sources,
		func(d *doc) *Severity { return d.Reporters.FailOn })
}

// resolveTS resolves the section the TypeScript analyzer owns.
func resolveTS(cfg *Config, p Provenance, sources []source) {
	resolveSetting(&cfg.TS.TestFiles, "ts.test_files", p, sources,
		func(d *doc) *[]string { return d.TS.TestFiles })
	resolveSetting(&cfg.TS.EntryFiles, "ts.entry_files", p, sources,
		func(d *doc) *[]string { return d.TS.EntryFiles })
}

// resolveSeverity resolves the severity object one code at a time, so a code only
// the central configuration names keeps its central value. An object that resolves
// empty carries one entry for the object itself.
func resolveSeverity(cfg *Config, p Provenance, sources []source) {
	codes := make(map[string]bool)
	for _, s := range sources {
		for code := range s.document.Severity {
			codes[code] = true
		}
	}
	cfg.Severity = make(map[string]Severity, len(codes))
	for _, code := range slices.Sorted(maps.Keys(codes)) {
		path := severitySection + "." + code
		for _, s := range sources {
			value, held := s.document.Severity[code]
			if !held {
				continue
			}
			cfg.Severity[code] = value
			p[path] = s.origin(path)
			break
		}
	}
	if len(codes) > 0 {
		return
	}
	p[severitySection] = Origin{Source: SourceDefault}
	for _, s := range sources {
		if s.document.Severity != nil {
			p[severitySection] = s.origin(severitySection)
			return
		}
	}
}

// checkLabels refuses a provenance entry whose source names no file or flag,
// which would print a provenance value the closed key list refuses to read back.
func checkLabels(p Provenance) error {
	for _, path := range slices.Sorted(maps.Keys(p)) {
		if origin := p[path]; origin.Source != SourceDefault && origin.Label == "" {
			return fmt.Errorf("%w: %s came from %s", ErrNoSourceLabel, path, origin.Source)
		}
	}
	return nil
}

// missingTargetKind refuses a resolved configuration no source supplied a target
// kind for, naming the field and the two sources searched.
func missingTargetKind(in *Inputs) *Error {
	return &Error{
		Kind: KindMissingTargetKind,
		Key:  "target.kind",
		Message: fmt.Sprintf(
			"target.kind is not set, it has no default and is never inferred: searched %s and %s",
			describeSource("the repository configuration", in.Repository, in.RepositoryLabel),
			describeSource("the central configuration", in.Central, in.CentralLabel),
		),
	}
}

// describeSource names one configuration source and whether the invocation
// carried it.
func describeSource(named string, data []byte, label string) string {
	if data == nil {
		return named + " (not present)"
	}
	return named + " (" + labelOr(label, "unnamed") + ")"
}

// validate checks every constraint the closed key list declares that the decoder
// cannot express: a value outside a closed set, an array that repeats an entry or
// holds too few, a count below its minimum, and the severity keys.
func validate(d *doc, label string) *Error {
	return firstError(
		validateContractVersion(d.ContractVersion, label),
		enum(label, "target.kind", d.Target.Kind, Application, Library),
		validateAnalysis(d.Analysis, label),
		arrayOf(label, "roots.patterns", d.Roots.Patterns, 0),
		validateSeverity(d.Severity, label),
		validateExemptions(d.Exemptions, label),
		validateReporters(d.Reporters, label),
		validateTS(d.TS, label),
	)
}

// validateContractVersion refuses a contract version that is not a semantic
// version.
func validateContractVersion(version *string, label string) *Error {
	if version == nil || contractVersionPattern.MatchString(*version) {
		return nil
	}
	return malformed(label, "contract_version", "%q is not a semantic version", *version)
}

// validateAnalysis checks the analysis section.
func validateAnalysis(a *docAnalysis, label string) *Error {
	return firstError(
		arrayOf(label, "analysis.languages", a.Languages, 0, GoLanguage, TSLanguage),
		enum(label, "analysis.min_confidence", a.MinConfidence, Certain, Probable, Possible),
		enum(label, "analysis.generated_files", a.GeneratedFiles, ExcludeGenerated, IncludeGenerated),
		enum(label, "analysis.consumer_tests", a.ConsumerTests, TestReference, ProductionReference),
		validateConfigurations(a.Configurations, label),
		arrayOf(label, "analysis.template_dirs", a.TemplateDirs, 0),
		validateDelimiters(a.TemplateDelimiters, label),
	)
}

// validateDelimiters checks the action delimiter pair: once a document names the
// object both members are required, because a pair carrying one member names no
// delimiters at all, so the refusal names the member the document left out rather
// than pairing the one it named with a default.
func validateDelimiters(delimiters *TemplateDelimiters, label string) *Error {
	if delimiters == nil {
		return nil
	}
	return firstError(
		required(label, "analysis.template_delimiters.left", delimiters.Left),
		required(label, "analysis.template_delimiters.right", delimiters.Right),
	)
}

// validateConfigurations checks the build matrix: every entry names an
// identifier, an operating system and an architecture.
func validateConfigurations(configurations *[]Configuration, label string) *Error {
	if configurations == nil {
		return nil
	}
	for index, entry := range *configurations {
		at := "analysis.configurations[" + strconv.Itoa(index) + "]"
		tags := entry.Tags
		refusal := firstError(
			required(label, at+".id", entry.ID),
			required(label, at+".os", entry.OS),
			required(label, at+".arch", entry.Arch),
			arrayOf(label, at+".tags", &tags, 0),
		)
		if refusal != nil {
			return refusal
		}
	}
	return nil
}

// validateExemptions checks the exemption classes named for switching off.
func validateExemptions(e *docExemptions, label string) *Error {
	if refusal := arrayOf(label, "exemptions.disabled", e.Disabled, 0); refusal != nil {
		return refusal
	}
	if e.Disabled == nil {
		return nil
	}
	for _, class := range *e.Disabled {
		if !exemptionClassPattern.MatchString(class) {
			return malformed(label, "exemptions.disabled", "%q is not an exemption class name", class)
		}
	}
	return nil
}

// validateReporters checks the reporters section.
func validateReporters(r *docReporters, label string) *Error {
	refusal := firstError(
		arrayOf(label, "reporters.formats", r.Formats, 1, Text, JSON, GitHub, SARIF, Template),
		enum(label, "reporters.sort", r.Sort, ByPosition, BySize),
		enum(label, "reporters.cascade", r.Cascade, CascadeRoots, CascadeFull),
		enum(label, "reporters.fail_on", r.FailOn, Allow, Warn, Deny),
	)
	if refusal != nil {
		return refusal
	}
	if r.MaxFindings != nil && *r.MaxFindings < 0 {
		return malformed(label, "reporters.max_findings", "%d is below the minimum of 0", *r.MaxFindings)
	}
	return nil
}

// validateTS checks the section the TypeScript analyzer owns.
func validateTS(t *docTS, label string) *Error {
	return firstError(
		arrayOf(label, "ts.test_files", t.TestFiles, 1),
		arrayOf(label, "ts.entry_files", t.EntryFiles, 0),
	)
}

// validateSeverity checks the severity object: a key is one issue-kind code or
// one two-digit family prefix, and a key naming a kind whose severity the
// Contract fixes, or a prefix whose range holds one, is an unimplemented key
// rather than a setting.
func validateSeverity(severity map[string]Severity, label string) *Error {
	for _, code := range slices.Sorted(maps.Keys(severity)) {
		path := severitySection + "." + code
		if !severityKeyPattern.MatchString(code) {
			return unimplementedSeverityKey(label, path,
				"a severity key is one issue-kind code or one two-digit family prefix")
		}
		if fixed := fixedByContract(code); len(fixed) > 0 {
			return unimplementedSeverityKey(label, path,
				fmt.Sprintf("the Contract fixes the severity of %s, which this key names", spellCodes(fixed)))
		}
		if !namesLiveKind(code) {
			return unimplementedSeverityKey(label, path, noLiveKind(code))
		}
		value := severity[code]
		if refusal := enum(label, path, &value, Allow, Warn, Deny); refusal != nil {
			return refusal
		}
	}
	return nil
}

// noLiveKind says why a well-formed severity key that names no issue kind this
// analyzer ships is not a setting, naming the code or the family the key spells.
func noLiveKind(code string) string {
	if len(code) == familyKeyLength {
		return "no issue kind this analyzer ships carries a code of the family " + code
	}
	return "no issue kind this analyzer ships carries the code " + code
}

// unimplementedSeverityKey refuses one severity key, naming why the key is not a
// setting rather than the nearest key, which for a code is never informative.
func unimplementedSeverityKey(label, path, reason string) *Error {
	return &Error{
		Kind:    KindUnimplementedKey,
		Key:     path,
		Message: fmt.Sprintf("%s: key %q is not implemented: %s", label, path, reason),
	}
}

// fixedByContract returns every code whose severity the Contract fixes that one
// severity key names, in ascending code order: the code itself, or every code of
// the family its two-digit prefix names. It returns none when the key names none.
// A family key covers every such code, so the refusal names them all rather than
// the first one found.
func fixedByContract(code string) []string {
	var fixed []string
	for _, candidate := range fixedSeverityCodes() {
		if code == candidate || (len(code) == 4 && strings.HasPrefix(candidate, code)) {
			fixed = append(fixed, candidate)
		}
	}
	slices.Sort(fixed)
	return fixed
}

// spellCodes lists the codes a refusal names: one code on its own, and several
// separated by commas with the last joined by and.
func spellCodes(codes []string) string {
	if len(codes) < 2 {
		return strings.Join(codes, "")
	}
	return strings.Join(codes[:len(codes)-1], ", ") + " and " + codes[len(codes)-1]
}

// firstError returns the first refusal a list of checks produced.
func firstError(refusals ...*Error) *Error {
	for _, refusal := range refusals {
		if refusal != nil {
			return refusal
		}
	}
	return nil
}

// enum refuses a value outside the closed set one key declares.
func enum[T ~string](label, path string, value *T, allowed ...T) *Error {
	if value == nil || slices.Contains(allowed, *value) {
		return nil
	}
	return malformed(label, path, "%q is not one of %s", string(*value), spell(allowed))
}

// arrayOf refuses an array shorter than its minimum, one holding an empty entry,
// one naming an entry twice, and, where a closed set is given, one holding an
// entry outside it.
func arrayOf[T ~string](label, path string, values *[]T, minItems int, allowed ...T) *Error {
	if values == nil {
		return nil
	}
	if len(*values) < minItems {
		return malformed(label, path, "holds %d entries, want at least %d", len(*values), minItems)
	}
	seen := make(map[T]bool, len(*values))
	for _, value := range *values {
		switch {
		case value == "":
			return malformed(label, path, "holds an empty entry")
		case seen[value]:
			return malformed(label, path, "names %q twice", string(value))
		case len(allowed) > 0 && !slices.Contains(allowed, value):
			return malformed(label, path, "%q is not one of %s", string(value), spell(allowed))
		}
		seen[value] = true
	}
	return nil
}

// required refuses a member the closed key list declares as required and the
// document left empty or absent.
func required(label, path, value string) *Error {
	if value != "" {
		return nil
	}
	return malformed(label, path, "is required and names nothing")
}

// spell lists a closed set as a refusal names it.
func spell[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(string(value))
	}
	return strings.Join(quoted, ", ")
}

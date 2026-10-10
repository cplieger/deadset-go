package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
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
	Providers       *docProviders       `json:"providers"`
	Go              *goSettings         `json:"go"`
	TS              *docTS              `json:"ts"`
	Provenance      map[string]string   `json:"provenance"`
}

type docTarget struct {
	Kind *TargetKind `json:"kind"`
}

type docAnalysis struct {
	Languages          *[]Language         `json:"languages"`
	MinConfidence      *Confidence         `json:"min_confidence"`
	GeneratedFiles     *generatedFiles     `json:"generated_files"`
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

type docProviders struct {
	Analyzers *[]provider `json:"analyzers"`
}

type docTS struct {
	TestFiles              *[]string            `json:"test_files"`
	EntryFiles             *[]string            `json:"entry_files"`
	ComponentExtensions    *[]string            `json:"component_extensions"`
	DisabledConventions    *[]string            `json:"disabled_conventions"`
	InjectionRegistrations *[]Declaration       `json:"injection_registrations"`
	LifecycleContracts     *[]LifecycleContract `json:"lifecycle_contracts"`
	Serializers            *[]Declaration       `json:"serializers"`
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
	if d.Providers == nil {
		d.Providers = &docProviders{}
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
	resolveSetting(&cfg.Providers.Analyzers, "providers.analyzers", provenance, sources,
		func(d *doc) *[]provider { return d.Providers.Analyzers })
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

// decodeChecked decodes a document already known to hold one value whose member
// names are declared, refusing an unimplemented key under DisallowUnknownFields
// and then every constraint the decoder cannot express.
func decodeChecked(data []byte, label string) (*doc, *Error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var document doc
	if err := dec.Decode(&document); err != nil {
		var mistyped *json.UnmarshalTypeError
		if errors.As(err, &mistyped) && mistyped.Field != "" {
			return nil, malformed(label, mistyped.Field, "holds %s, want %s", mistyped.Value, jsonTypeOf(mistyped.Type))
		}
		return nil, malformed(label, "", "%s", err)
	}
	document.fill()
	if refusal := validate(&document, label); refusal != nil {
		return nil, refusal
	}
	return &document, nil
}

// jsonTypeOf names the JSON type a refusal asks for in place of the decode target's
// Go type. An integer setting is written as digits alone, so a number carrying a
// fraction or an exponent is refused even where it denotes an integer.
func jsonTypeOf(target reflect.Type) string {
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer written as an optional minus sign and digits alone"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a Boolean"
	case reflect.Slice, reflect.Array:
		return "an array"
	default:
		return "an object"
	}
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
		func(d *doc) *generatedFiles { return d.Analysis.GeneratedFiles })
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
	for _, entry := range cfg.Analysis.Configurations {
		if entry.Platform != nil && entry.Platform.Tags == nil {
			entry.Platform.Tags = []string{}
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
	resolveSetting(&cfg.TS.ComponentExtensions, "ts.component_extensions", p, sources,
		func(d *doc) *[]string { return d.TS.ComponentExtensions })
	resolveSetting(&cfg.TS.DisabledConventions, "ts.disabled_conventions", p, sources,
		func(d *doc) *[]string { return d.TS.DisabledConventions })
	resolveSetting(&cfg.TS.InjectionRegistrations, "ts.injection_registrations", p, sources,
		func(d *doc) *[]Declaration { return d.TS.InjectionRegistrations })
	resolveSetting(&cfg.TS.LifecycleContracts, "ts.lifecycle_contracts", p, sources,
		func(d *doc) *[]LifecycleContract { return d.TS.LifecycleContracts })
	resolveSetting(&cfg.TS.Serializers, "ts.serializers", p, sources,
		func(d *doc) *[]Declaration { return d.TS.Serializers })
	for index := range cfg.TS.LifecycleContracts {
		contract := &cfg.TS.LifecycleContracts[index]
		contract.Components = orEmpty(contract.Components)
		contract.Bases = orEmpty(contract.Bases)
	}
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
		validateProviders(d.Providers.Analyzers, label),
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

// matrixShapes names the two shapes an entry of the build matrix takes, as a refusal
// of an entry taking neither or both states them.
const matrixShapes = "an entry is a platform, naming id, os, arch and optionally tags, " +
	"or a project, naming id and project"

// validateConfigurations checks the build matrix: every entry takes exactly one of
// the two shapes and names every member its shape requires.
func validateConfigurations(configurations *[]Configuration, label string) *Error {
	if configurations == nil {
		return nil
	}
	for index, entry := range *configurations {
		at := "analysis.configurations[" + strconv.Itoa(index) + "]"
		if refusal := validateConfiguration(entry, at, label); refusal != nil {
			return refusal
		}
	}
	return nil
}

// validateConfiguration checks one entry of the build matrix, at naming its place in
// the matrix.
func validateConfiguration(entry Configuration, at, label string) *Error {
	switch {
	case entry.Platform != nil && entry.Project != nil:
		return malformed(label, at, "names members of both shapes; %s", matrixShapes)
	case entry.Platform != nil:
		tags := entry.Platform.Tags
		return firstError(
			required(label, at+".id", entry.Platform.ID),
			required(label, at+".os", entry.Platform.OS),
			required(label, at+".arch", entry.Platform.Arch),
			arrayOf(label, at+".tags", &tags, 0),
		)
	case entry.Project != nil:
		refusal := firstError(
			required(label, at+".id", entry.Project.ID),
			required(label, at+".project", entry.Project.Path),
		)
		if refusal != nil {
			return refusal
		}
		if !projectPathPattern.MatchString(entry.Project.Path) {
			return malformed(label, at+".project",
				"%q is not a file path below the target root, written with / between its segments",
				entry.Project.Path)
		}
		return nil
	default:
		return malformed(label, at, "names neither shape; %s", matrixShapes)
	}
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

// providerShapes names the two shapes a provider entry takes, as a refusal of an
// entry taking neither states them.
const providerShapes = "an entry names name, languages and command, " +
	"and an acquirable one also names source, version and digest"

// validateProviders checks the provider list: every entry takes one of the two
// shapes, and no two entries share a name, the later one named by the refusal.
func validateProviders(providers *[]provider, label string) *Error {
	if providers == nil {
		return nil
	}
	seen := make(map[string]bool, len(*providers))
	for index, entry := range *providers {
		at := "providers.analyzers[" + strconv.Itoa(index) + "]"
		if refusal := validateProvider(&entry, at, label); refusal != nil {
			return refusal
		}
		if seen[entry.Name] {
			return malformed(label, at+".name", "%q names an analyzer an earlier entry already names", entry.Name)
		}
		seen[entry.Name] = true
	}
	return nil
}

// validateProvider checks one provider entry, at naming its place in the list.
func validateProvider(entry *provider, at, label string) *Error {
	if !analyzerNamePattern.MatchString(entry.Name) {
		return malformed(label, at+".name", "%q is not lowercase words joined by single hyphens", entry.Name)
	}
	languages := entry.Languages
	refusal := firstError(
		arrayOf(label, at+".languages", &languages, 1, GoLanguage, TSLanguage),
		required(label, at+".command", entry.Command),
	)
	if refusal != nil {
		return refusal
	}
	if entry.Source == nil && entry.Version == nil && entry.Digest == nil {
		return nil
	}
	for _, member := range []struct {
		value   *string
		pattern *regexp.Regexp
		name    string
	}{
		{entry.Source, providerSourcePattern, "source"},
		{entry.Version, providerVersionPattern, "version"},
		{entry.Digest, providerDigestPattern, "digest"},
	} {
		if member.value == nil {
			return malformed(label, at, "names some of source, version and digest; %s", providerShapes)
		}
		if !member.pattern.MatchString(*member.value) {
			return malformed(label, at+"."+member.name, "%q is not of the form the provider list declares", *member.value)
		}
	}
	return nil
}

// validateTS checks the section the TypeScript analyzer owns.
func validateTS(t *docTS, label string) *Error {
	return firstError(
		arrayOf(label, "ts.test_files", t.TestFiles, 1),
		arrayOf(label, "ts.entry_files", t.EntryFiles, 0),
		patternedArray(label, "ts.component_extensions", t.ComponentExtensions, componentExtensionPattern,
			"is not a full stop followed by letters and digits"),
		patternedArray(label, "ts.disabled_conventions", t.DisabledConventions, conventionNamePattern,
			"is not lowercase words joined by single hyphens"),
		validateDeclarations(t.InjectionRegistrations, "ts.injection_registrations", label),
		validateLifecycleContracts(t.LifecycleContracts, label),
		validateDeclarations(t.Serializers, "ts.serializers", label),
	)
}

// declarationShapes names the three shapes a declaration entry takes, as a refusal
// of an entry taking none or several states them.
const declarationShapes = "an entry names a declaration by symbol, by module and name, or by global"

// validateDeclarations checks one list of declaration entries, at naming the list.
func validateDeclarations(declarations *[]Declaration, at, label string) *Error {
	if declarations == nil {
		return nil
	}
	for index, entry := range *declarations {
		if refusal := validateDeclaration(entry, at+"["+strconv.Itoa(index)+"]", label); refusal != nil {
			return refusal
		}
	}
	return nil
}

// validateDeclaration checks one declaration entry, at naming its place: the entry
// takes exactly one of the three shapes and names every member its shape requires.
func validateDeclaration(entry Declaration, at, label string) *Error {
	bySymbol := entry.Symbol != nil
	byExport := entry.Module != nil || entry.Name != nil
	byGlobal := entry.Global != nil
	switch shapes := countTrue(bySymbol, byExport, byGlobal); {
	case shapes == 0:
		return malformed(label, at, "names no declaration; %s", declarationShapes)
	case shapes > 1:
		return malformed(label, at, "names members of more than one shape; %s", declarationShapes)
	case bySymbol:
		if !typescriptReferencePattern.MatchString(*entry.Symbol) {
			return malformed(label, at+".symbol",
				"%q is not a stable symbol reference in the TypeScript form", *entry.Symbol)
		}
		return nil
	case byExport:
		refusal := firstError(
			required(label, at+".module", valueOf(entry.Module)),
			required(label, at+".name", valueOf(entry.Name)),
		)
		if refusal != nil {
			return refusal
		}
		if !bareSpecifierPattern.MatchString(*entry.Module) {
			return malformed(label, at+".module",
				"%q is a relative, absolute or imports-mapped specifier, which names a file of the "+
					"analyzed program. Name that declaration by symbol instead", *entry.Module)
		}
		return nil
	default:
		return required(label, at+".global", *entry.Global)
	}
}

// validateLifecycleContracts checks every lifecycle contract: each names the
// members the framework calls, and at least one declaration or base class that makes
// a class one of its components.
func validateLifecycleContracts(contracts *[]LifecycleContract, label string) *Error {
	if contracts == nil {
		return nil
	}
	for index, contract := range *contracts {
		at := "ts.lifecycle_contracts[" + strconv.Itoa(index) + "]"
		if contract.Members == nil {
			return required(label, at+".members", "")
		}
		refusal := firstError(
			validateDeclarations(&contract.Components, at+".components", label),
			validateDeclarations(&contract.Bases, at+".bases", label),
			arrayOf(label, at+".members", &contract.Members, 1),
		)
		if refusal != nil {
			return refusal
		}
		if len(contract.Components) == 0 && len(contract.Bases) == 0 {
			return malformed(label, at,
				"names no declaration in components and no class in bases, so no class is a component of its framework")
		}
	}
	return nil
}

// countTrue counts the conditions that hold.
func countTrue(conditions ...bool) int {
	held := 0
	for _, condition := range conditions {
		if condition {
			held++
		}
	}
	return held
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
	return &Error{Message: fmt.Sprintf("%s: key %q is not implemented: %s", label, path, reason)}
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
	for index, value := range *values {
		switch {
		case value == "":
			return malformed(label, entryPath(path, index), "is empty")
		case seen[value]:
			return malformed(label, entryPath(path, index), "names %q a second time", string(value))
		case len(allowed) > 0 && !slices.Contains(allowed, value):
			return malformed(label, entryPath(path, index), "%q is not one of %s", string(value), spell(allowed))
		}
		seen[value] = true
	}
	return nil
}

// patternedArray checks an array of strings whose every entry matches pattern,
// shape naming what a refused entry is not.
func patternedArray(label, path string, values *[]string, pattern *regexp.Regexp, shape string) *Error {
	if refusal := arrayOf(label, path, values, 0); refusal != nil {
		return refusal
	}
	if values == nil {
		return nil
	}
	for index, value := range *values {
		if !pattern.MatchString(value) {
			return malformed(label, entryPath(path, index), "%q %s", value, shape)
		}
	}
	return nil
}

// entryPath names one entry of the array at path, as a refusal of that entry names it.
func entryPath(path string, index int) string {
	return path + "[" + strconv.Itoa(index) + "]"
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

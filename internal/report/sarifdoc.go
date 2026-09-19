package report

// The document below is the Contract's SARIF mapping in Go: one field per property
// the mapping emits, in the order the mapping lists them, so two runs over an
// unchanged tree write the same bytes. A property the mapping states is never
// emitted has no field here, which is why the shapes are narrower than the format.

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool               sarifTool               `json:"tool"`
	AutomationDetails  sarifAutomationDetails  `json:"automationDetails"`
	ColumnKind         string                  `json:"columnKind"`
	OriginalURIBaseIDs map[string]sarifURIBase `json:"originalUriBaseIds"`
	Results            []sarifResult           `json:"results"`
	Properties         sarifRunProperties      `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	Version         string      `json:"version"`
	SemanticVersion string      `json:"semanticVersion"`
	Rules           []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	DefaultConfiguration sarifConfiguration  `json:"defaultConfiguration"`
	Properties           sarifRuleProperties `json:"properties"`
}

type sarifConfiguration struct {
	Level string `json:"level"`
}

type sarifRuleProperties struct {
	Precision string       `json:"precision"`
	Problem   sarifProblem `json:"problem"`
}

type sarifProblem struct {
	Severity string `json:"severity"`
}

type sarifAutomationDetails struct {
	ID string `json:"id"`
}

type sarifURIBase struct {
	Description sarifMessage `json:"description"`
}

type sarifRunProperties struct {
	Totals wireTotals `json:"totals"`
}

//nolint:govet // fieldalignment: the field order is the mapping's property order, which the document writes
type sarifResult struct {
	RuleID              string                 `json:"ruleId"`
	RuleIndex           int                    `json:"ruleIndex"`
	Level               string                 `json:"level"`
	Message             sarifMessage           `json:"message"`
	Locations           []sarifLocation        `json:"locations"`
	RelatedLocations    []sarifLocation        `json:"relatedLocations,omitempty"`
	PartialFingerprints sarifFingerprints      `json:"partialFingerprints"`
	Properties          *sarifResultProperties `json:"properties,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

//nolint:govet // fieldalignment: the field order is the mapping's property order, which the document writes
type sarifLocation struct {
	ID               int           `json:"id,omitempty"`
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
	Message          *sarifMessage `json:"message,omitempty"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
}

// sarifFingerprints is the two keys every result carries. The first is the one a
// code-scanning service reads; the second is the one a baseline joins on.
type sarifFingerprints struct {
	PrimaryLocationLineHash string `json:"primaryLocationLineHash"`
	DeadsetSymbolRef        string `json:"deadsetSymbolRef/v1"`
}

// sarifResultProperties is every field of a finding the mapping does not name, under
// the name the report schema gives it. A field the finding omits is absent from the
// bag rather than written as a null or as an empty value.
//
//nolint:govet // fieldalignment: the field order is the report schema's member order
type sarifResultProperties struct {
	Language          string        `json:"language"`
	Symbol            wireSubject   `json:"symbol"`
	ReachabilityClass string        `json:"reachability_class"`
	Confidence        string        `json:"confidence"`
	LivenessRelation  string        `json:"liveness_relation,omitempty"`
	TestOnly          bool          `json:"test_only"`
	Generated         bool          `json:"generated"`
	Component         wireComponent `json:"component"`
	RetainedBy        []string      `json:"retained_by"`
	Configurations    []string      `json:"configurations"`
	ConsumersLoaded   []string      `json:"consumers_loaded"`
	Fixability        string        `json:"fixability"`
	Details           wireDetails   `json:"details"`
}

package report

// The tests that read a document back live in package report_test, because the
// reader imports this package; these are the fixtures they build the document from.
var (
	FullInput    = fullInput
	MinimalInput = minimalInput
	Built        = built
	Rendered     = rendered
	FindingOf    = findingOf
	Diff         = diff
)

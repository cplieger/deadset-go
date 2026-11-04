package findings

import "github.com/cplieger/deadset-spec/v8/vocab"

// capFilled lowers one finding to the class possible where its subject is a field a
// callee outside the analysed program fills from a document, which may read it as a
// validating binder does, or one an encoder emits that nothing writes, whose key a
// reader of the output may need: in neither case can the analysis see the read.
func (in *pass) capFilled(found *Finding) {
	if in.Filled[found.id] {
		found.Class = vocab.Possible
	}
}

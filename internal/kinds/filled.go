package kinds

// capFilled lowers one finding to the class possible where its subject is a field a
// callee outside the analysed program fills from a document: whether that callee reads
// the field as well, as a validating binder does, the analysis cannot see.
func (in *Input) capFilled(found *Finding) {
	if in.Filled[found.id] {
		found.Class = Possible
	}
}

// Package consumed exports declarations another module references and one
// nothing references.
package consumed

// Used is referenced by a consumer's production file.
func Used() string { return "used" }

// UsedByConsumerTest is referenced by a consumer's test file alone.
func UsedByConsumerTest() string { return "test" }

// Unused is referenced by nothing.
func Unused() string { return "unused" }

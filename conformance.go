// Package deadsetgo carries the two documents this analyzer commits about its own
// conformance, so the command can state what a reader of the repository opens.
//
// The documents sit at the root of the repository, where a reader looks for what the
// analyzer declines and where the products that consume them read them. The command's
// own package cannot carry them from there: an embed pattern reaches no file above
// the directory of the package that declares it, and the digest a report states has
// to be taken over the bytes the binary holds, because a digest written beside the
// document it describes is a second copy of one fact and the two can disagree at any
// commit. This package exists for that one reason and holds nothing else.
package deadsetgo

import _ "embed"

// Gaps is conformance.json: the corpus version this analyzer answers and every
// capability it declines, with the reason for each.
//
//go:embed conformance.json
var Gaps []byte

// Results is conformance-results.json: the per-fixture record this analyzer's corpus
// runner wrote, the result over the whole corpus included. The digest a report states
// in its conformance block is the SHA-256 of these bytes.
//
//go:embed conformance-results.json
var Results []byte

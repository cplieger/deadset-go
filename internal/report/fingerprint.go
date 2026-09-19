package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"unicode/utf16"
)

// The line fingerprint's own constants: the sentinel unit appended after the last
// unit of a file, the number of units a line's hash covers, and the multiplier of
// the polynomial.
const (
	fingerprintSentinel = 0xffff
	fingerprintWindow   = 100
	fingerprintFactor   = 37
)

// lineHashes is the line fingerprint of every line of one file, indexed from zero,
// so the value for a finding is the entry at its line less one.
//
// The procedure is the one a code-scanning service computes when a producer leaves
// the key out, and a producer that computes it differently makes that service
// disagree with the document it uploaded: two alerts for one finding. So it is
// followed exactly. The units of the file are its UTF-16 code units with every
// space and tab dropped, every carriage return read as a line feed and a line feed
// following one dropped, and one sentinel appended; a line starts at the first unit
// and after every line feed; a line's hash is the polynomial over the hundred units
// from its start, with a position past the sentinel counting as zero; and the value
// is that hash in lowercase hexadecimal, then the number of lines so far whose hash
// renders identically, which is what tells two identical lines apart.
func lineHashes(content []byte) []string {
	units := significantUnits(content)
	hashes := make([]string, 0, len(units)/16+1)
	seen := make(map[string]int)
	for _, at := range lineStarts(units) {
		var hash uint64
		for i := at; i < at+fingerprintWindow; i++ {
			var unit uint64
			if i < len(units) {
				unit = uint64(units[i])
			}
			hash = hash*fingerprintFactor + unit
		}
		rendered := strconv.FormatUint(hash, 16)
		seen[rendered]++
		hashes = append(hashes, rendered+":"+strconv.Itoa(seen[rendered]))
	}
	return hashes
}

// significantUnits is the units of one file the procedure counts, with the sentinel
// appended.
func significantUnits(content []byte) []uint16 {
	all := utf16.Encode([]rune(string(content)))
	kept := make([]uint16, 0, len(all)+1)
	afterCarriageReturn := false
	for _, unit := range all {
		switch unit {
		case ' ', '\t':
		case '\r':
			kept = append(kept, '\n')
			afterCarriageReturn = true
			continue
		case '\n':
			if !afterCarriageReturn {
				kept = append(kept, '\n')
			}
		default:
			kept = append(kept, unit)
		}
		afterCarriageReturn = false
	}
	return append(kept, fingerprintSentinel)
}

// lineStarts is the index of the first unit of every line. The sentinel starts a
// line of its own after a file that ends in a line feed, which no finding refers to.
func lineStarts(units []uint16) []int {
	if len(units) == 0 {
		return nil
	}
	starts := []int{0}
	for i := range len(units) - 1 {
		if units[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// symbolFingerprint is the key a baseline joins on and the one a line move leaves
// unchanged: the digest of the code, one line feed and the symbol reference. Neither
// component holds a line feed, so the separator is unambiguous.
func symbolFingerprint(code, ref string) string {
	digest := sha256.Sum256([]byte(code + "\n" + ref))
	return hex.EncodeToString(digest[:])
}

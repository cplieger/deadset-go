package report

import (
	"slices"
	"strings"
	"testing"
)

// TestTheLineFingerprintMatchesThePublishedVectors pins the line fingerprint against
// every vector the Contract publishes for it, which is what makes a document this
// analyzer uploads fingerprint the way the service that receives it would.
func TestTheLineFingerprintMatchesThePublishedVectors(t *testing.T) {
	fixture := "package fixture\n\nfunc \u00dcnused() {\n    return\n}\n"
	want := []string{
		"7a0e51a45e6d7320:1",
		"3134adfd1bbad887:1",
		"247cff8f02e0b919:1",
		"58228fc5cbc49530:1",
		"32dce9ccfdbc9d3e:1",
	}
	got := lineHashes([]byte(fixture))
	if len(got) != len(want)+1 {
		t.Fatalf("lineHashes(a five-line file) returned %d lines, want %d and the sentinel's own",
			len(got), len(want))
	}
	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("lineHashes(the published fixture) = %v, want %v", got[:len(want)], want)
	}
}

// TestTheLineFingerprintDisambiguatesIdenticalLines pins the counter that tells two
// lines with the same hash apart, against the vector the Contract publishes for it.
func TestTheLineFingerprintDisambiguatesIdenticalLines(t *testing.T) {
	got := lineHashes([]byte(strings.Repeat("y\n", 200)))

	cases := map[int]string{
		1:   "43762f342805c306:1",
		2:   "43762f342805c306:2",
		151: "43762f342805c306:151",
	}
	for line, want := range cases {
		if got[line-1] != want {
			t.Errorf("lineHashes(two hundred identical lines)[%d] = %q, want %q", line, got[line-1], want)
		}
	}
}

// TestTheLineFingerprintIgnoresIndentationAndLineEndings pins the three normalizations
// the procedure makes, each of which a producer that skipped it would disagree with the
// receiving service over.
func TestTheLineFingerprintIgnoresIndentationAndLineEndings(t *testing.T) {
	spaces := "package fixture\n\nfunc \u00dcnused() {\n    return\n}\n"
	cases := map[string]string{
		"a tab instead of spaces":    strings.ReplaceAll(spaces, "    return", "\treturn"),
		"no indentation at all":      strings.ReplaceAll(spaces, "    return", "return"),
		"carriage returns and feeds": strings.ReplaceAll(spaces, "\n", "\r\n"),
		"carriage returns alone":     strings.ReplaceAll(spaces, "\n", "\r"),
	}
	want := lineHashes([]byte(spaces))
	for name, content := range cases {
		if got := lineHashes([]byte(content)); !slices.Equal(got, want) {
			t.Errorf("lineHashes(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestTheSymbolFingerprintMatchesThePublishedVectors pins the digest a baseline joins on
// against both vectors the Contract publishes.
func TestTheSymbolFingerprintMatchesThePublishedVectors(t *testing.T) {
	cases := []struct {
		code, ref, want string
	}{
		{
			"DS1001", "go://example.com/fixture#\u00dcnused",
			"d073714ada8cfcbee49bd5430446d6be7b837b6fd1fc34e6aa82be03b589c18d",
		},
		{
			"DS1001", "go://example.com/app#Catalog.ResolveAlias",
			"3969945e4504f5d8a52415a6f7b4f233d1ba4820a2fe24617a3611e2535d5e32",
		},
	}
	for _, one := range cases {
		if got := symbolFingerprint(one.code, one.ref); got != one.want {
			t.Errorf("symbolFingerprint(%s, %s) = %q, want %q", one.code, one.ref, got, one.want)
		}
	}
}

// TestTheSymbolFingerprintSeparatesItsComponents pins that the separator is unambiguous:
// two pairs whose concatenation would be equal digest differently.
func TestTheSymbolFingerprintSeparatesItsComponents(t *testing.T) {
	first := symbolFingerprint("DS1001", "go://example.com/app#F")
	second := symbolFingerprint("DS1001\ngo://example.com/app#F", "")
	if first == second {
		t.Errorf("two pairs whose components run together digest identically: %q", first)
	}
}

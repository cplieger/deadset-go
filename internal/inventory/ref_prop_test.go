package inventory

import (
	"strings"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport/draws"
	"github.com/cplieger/deadset-spec/v8/segment"
	"github.com/cplieger/deadset-spec/v8/suppress"
	"pgregory.net/rapid"
)

// pathBytes are the bytes a drawn element or file name is made of: the Go set,
// every byte a segment set treats apart, and the bytes of malformed and
// well-formed UTF-8 sequences, those of the C1 controls and the whitespace past
// ASCII among them.
var pathBytes = []byte("aZ09._~-+ \t#%*?'[]\\:@\x00\x1f\x7f\x80\x85\xa0\xa8\xbb\xbf\xc2\xc3\xa9\xe2\xe3\x82\xac\xef\xf0\x9f\x98\xed\xff")

// drawnSegment draws one element or file name. Neither is ever . or .., which
// an import path holds no element of (golang.org/x/mod/module.CheckImportPath).
func drawnSegment(t *rapid.T, label string) string {
	return string(rapid.SliceOfN(rapid.SampledFrom(pathBytes), 1, 8).
		Filter(func(b []byte) bool { return string(b) != "." && string(b) != ".." }).
		Draw(t, label))
}

// Every reference Ref writes, whatever bytes the import path and the file name
// hold, is one the published grammar accepts, and each element and the file name
// read back from it as the bytes Ref was given.
func TestRefWritesOnlyReferencesTheGrammarAcceptsAndThatReadBack(t *testing.T) {
	t.Parallel()
	draws.Check(t, 100, draws.InMemory, func(t *rapid.T) {
		elements := make([]string, rapid.IntRange(1, 4).Draw(t, "the element count"))
		for i := range elements {
			elements[i] = drawnSegment(t, "an element")
		}
		pkgPath := strings.Join(elements, "/")
		name := drawnSegment(t, "a file name") + ".go"

		for _, ref := range []string{
			Ref(KindPackage, pkgPath, nil),
			Ref(KindFunc, pkgPath, []string{"Run"}),
			Ref(KindTypeParam, pkgPath, []string{"Best", "T"}),
			Ref(KindFile, pkgPath, []string{name}),
		} {
			if !suppress.ValidReference(ref) {
				t.Fatalf("Ref over %q and %q wrote %q, which the published grammar refuses", pkgPath, name, ref)
			}
		}

		scope, fragment, _ := strings.Cut(strings.TrimPrefix(Ref(KindFile, pkgPath, []string{name}), refPrefix), "#")
		for i, spelled := range strings.Split(scope, "/") {
			if got, canonical := segment.Read(segment.GoElement, spelled); !canonical || got != elements[i] {
				t.Fatalf("element %d of %q reads as %q, %t, want %q, true", i, scope, got, canonical, elements[i])
			}
		}
		operand := strings.TrimSuffix(fragment, ":file")
		if got, canonical := segment.Read(segment.FileOperand, operand); !canonical || got != name {
			t.Fatalf("the file operand %q reads as %q, %t, want %q, true", operand, got, canonical, name)
		}
	})
}

package config

import (
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

// A member name outside a keyed section's vocabulary is refused with the
// severity reason only for the severity section; any other names its own
// vocabulary.
func TestMemberOutsideNamesTheSectionsOwnVocabulary(t *testing.T) {
	t.Parallel()

	severity, _ := vocab.ConfigKeyOf(vocab.KeySeverity)
	if got, want := memberOutside(&severity, "DS2999"), "no issue kind carries the code DS2999"; got != want {
		t.Errorf("memberOutside(severity, DS2999) = %q, want %q", got, want)
	}
	planted := vocab.ConfigKey{Key: "planted", Role: vocab.RoleKeyed, Vocabulary: "planted-names", Values: []string{"a", "b"}}
	if got, want := memberOutside(&planted, "c"), "the planted names are a, b"; got != want {
		t.Errorf("memberOutside(planted, c) = %q, want %q", got, want)
	}
}

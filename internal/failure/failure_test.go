package failure

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/deadset-spec/v8/vocab"
)

func TestWritePrintsOneClassLineThenEveryFurtherFailure(t *testing.T) {
	var out strings.Builder
	err := Write(&out, []Failure{
		{Class: vocab.SetupMissingModule, Detail: "a.go:1: the import x\n  is missing"},
		{Class: vocab.RunUnwrittenOutput, Detail: "write r.json"},
	})
	want := vocab.SetupFailurePrefix + vocab.SetupMissingModule + ": a.go:1: the import x   is missing\n" +
		vocab.RunUnwrittenOutput + ": write r.json\n"
	if err != nil || out.String() != want {
		t.Errorf("Write() = %q, %v, want %q", out.String(), err, want)
	}
}

func TestOfNamesTheClassAnErrorHolds(t *testing.T) {
	if got := Of(errors.New("boom")); len(got) != 1 || got[0].Class != vocab.RunProductFault || !strings.Contains(got[0].Detail, "boom") {
		t.Errorf("Of(an unclassed error) = %v, want one product fault naming it", got)
	}
	wrapped := errors.Join(errors.New("context"), Wrap(vocab.SetupUnreadableDocument, errors.New("deadset-ignore.json cannot be read")))
	if got := Of(wrapped); got[0].Class != vocab.SetupUnreadableDocument || !Setup(wrapped) {
		t.Errorf("Of(a wrapped setup failure) = %v, Setup = %t, want unreadable-document and true", got, Setup(wrapped))
	}
	if Setup(New(vocab.RunUnwrittenOutput, "x")) {
		t.Error("Setup(a run failure) = true, want false")
	}
}

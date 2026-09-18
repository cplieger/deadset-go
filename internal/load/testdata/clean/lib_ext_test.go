package clean_test

import (
	"testing"

	"example.com/clean"
)

func TestAnswerFromOutside(t *testing.T) {
	if got := clean.Answer(); got != 7 {
		t.Errorf("clean.Answer() = %d, want 7", got)
	}
}

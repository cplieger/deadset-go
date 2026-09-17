package clean

import "testing"

func TestAnswer(t *testing.T) {
	if got := Answer(); got != 7 {
		t.Errorf("Answer() = %d, want 7", got)
	}
}

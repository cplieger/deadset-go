package facts

import (
	"strconv"
	"testing"
)

func TestViaStringNamesEveryRule(t *testing.T) {
	for v := viaIdentifier; v <= viaConstantRepetition; v++ {
		if got := v.String(); got == "" || got == "via("+strconv.Itoa(int(v))+")" {
			t.Errorf("via(%d).String() = %q, want the rule's spelling", v, got)
		}
	}
	if got, want := via(255).String(), "via(255)"; got != want {
		t.Errorf("via(255).String() = %q, want %q", got, want)
	}
}

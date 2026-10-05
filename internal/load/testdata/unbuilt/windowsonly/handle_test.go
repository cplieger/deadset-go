package windowsonly_test

import (
	"testing"

	"example.com/unbuilt/windowsonly"
)

func TestSelected(t *testing.T) {
	if windowsonly.Selected() != 2 {
		t.Error("windowsonly.Selected() is not 2")
	}
}

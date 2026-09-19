package main

import (
	"testing"

	"example.com/consumed"
)

func TestUsedByConsumerTestReturnsText(t *testing.T) {
	if consumed.UsedByConsumerTest() == "" {
		t.Error("consumed.UsedByConsumerTest() = \"\", want text")
	}
}

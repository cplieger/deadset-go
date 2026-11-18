// Package rerun is the environment of a child that re-runs the test binary.
package rerun

import "os"

// Environment is env for a child that re-runs this test binary: GORACE
// keeps the options this process was given and adds atexit_sleep_ms=0 after
// them, so a race-built child does not sleep its default second on exit
// (https://go.dev/doc/articles/race_detector#Options). A binary built without
// the race detector ignores GORACE.
func Environment(env []string) []string {
	options := "atexit_sleep_ms=0"
	if inherited := os.Getenv("GORACE"); inherited != "" {
		options = inherited + " " + options
	}
	return append(env, "GORACE="+options)
}

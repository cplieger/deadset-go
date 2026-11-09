package testsupport

import (
	"flag"
	"runtime"
	"strconv"
)

// waitingTests is how many parallel tests run per processor in a package whose tests
// mostly wait on the go command each load starts. A start costs about 0.4 s on a
// Windows runner whatever the processor does meanwhile, so a package run at one test
// per processor spends most of its time waiting.
const waitingTests = 4

// WaitOnTheToolchain parses the test flags and, where the invocation gives no
// -test.parallel, lets [waitingTests] parallel tests per processor run at once. A
// TestMain calls it before m.Run.
func WaitOnTheToolchain() error {
	flag.Parse()
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == "test.parallel" })
	if given {
		return nil
	}
	return flag.Set("test.parallel", strconv.Itoa(waitingTests*runtime.GOMAXPROCS(0)))
}

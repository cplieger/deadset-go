package testsupport

import (
	"flag"
	"runtime"
	"strconv"
	"testing"
)

func TestWaitOnTheToolchainRunsSeveralTestsPerProcessorUnlessTheInvocationSaysHowMany(t *testing.T) {
	parallel := flag.Lookup("test.parallel")
	before := parallel.Value.String()
	t.Cleanup(func() { _ = parallel.Value.Set(before) })
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == "test.parallel" })

	if err := WaitOnTheToolchain(); err != nil {
		t.Fatalf("WaitOnTheToolchain() = %v", err)
	}
	want := strconv.Itoa(waitingTests * runtime.GOMAXPROCS(0))
	if given {
		want = before
	}
	if got := parallel.Value.String(); got != want {
		t.Errorf("-test.parallel = %s after WaitOnTheToolchain (given on the command line: %v), want %s", got, given, want)
	}
}

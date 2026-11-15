package exempt

import (
	"fmt"
	"os"
	"testing"

	"github.com/cplieger/deadset-go/v2/internal/testsupport"
)

// TestMain runs the package's tests, most of which wait on the go command a load
// starts, [testsupport.WaitOnTheToolchain] many at once.
func TestMain(m *testing.M) {
	if err := testsupport.WaitOnTheToolchain(); err != nil {
		fmt.Fprintf(os.Stderr, "set the test parallelism: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

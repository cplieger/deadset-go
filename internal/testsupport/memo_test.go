package testsupport_test

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/cplieger/deadset-go/internal/testsupport"
)

func TestMemoBuildsEachKeyOnceForEveryTestAskingForIt(t *testing.T) {
	var memo testsupport.Memo[string, int]
	var builds [2]atomic.Int32

	t.Run("askers", func(t *testing.T) {
		for i := range 8 {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()

				key := [2]string{"even", "odd"}[i%2]
				got := memo.Of(t, key, func(*testing.T) int { return int(builds[i%2].Add(1)) * 10 })
				if got != 10 {
					t.Errorf("Memo.Of(%q) = %d, want 10, the value the first build answered", key, got)
				}
			})
		}
	})

	if even, odd := builds[0].Load(), builds[1].Load(); even != 1 || odd != 1 {
		t.Errorf("Memo.Of over two keys asked by four tests each built %d and %d times, want once each", even, odd)
	}
}

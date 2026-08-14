package fleet

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// The two things `each` promises: it never runs more than maxParallel at once,
// and a failure does not cut the pass short — every worker finishes before the
// error surfaces, so none is left half-provisioned.
func TestEachCapsConcurrencyAndWaitsOutFailures(t *testing.T) {
	var (
		live, peak, done atomic.Int64
		start            sync.WaitGroup
	)
	start.Add(1)

	idx := make([]int, maxParallel*3)
	for i := range idx {
		idx[i] = i
	}

	boom := errors.New("boom")
	err := each(idx, func(i int) error {
		n := live.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		// Hold the slot long enough that everything admitted so far overlaps.
		if i == 0 {
			start.Done()
		} else {
			start.Wait()
		}
		live.Add(-1)
		done.Add(1)
		if i == 1 {
			return boom
		}
		return nil
	})

	if !errors.Is(err, boom) {
		t.Fatalf("want the failure reported, got %v", err)
	}
	if got := done.Load(); got != int64(len(idx)) {
		t.Fatalf("only %d of %d finished; an early return stranded the rest", got, len(idx))
	}
	if got := peak.Load(); got > maxParallel {
		t.Fatalf("ran %d at once, limit is %d", got, maxParallel)
	}
}

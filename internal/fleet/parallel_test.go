package fleet

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The two things `each` promises: it never runs more than maxParallel at once,
// and a failure does not cut the pass short — every worker finishes before the
// error surfaces, so none is left half-provisioned.
func TestEachCapsConcurrencyAndWaitsOutFailures(t *testing.T) {
	var (
		live, peak, done atomic.Int64
		full             = make(chan struct{})
		once             sync.Once
	)

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
		// Hold the slot until the first batch is all running, so they overlap.
		// Any index may be admitted first, so the barrier counts live callers
		// rather than waiting on a particular one — which deadlocked whenever
		// index 0 was not among the first maxParallel to be scheduled. Later
		// callers find it already open; the timeout only guards a regression.
		if n >= maxParallel {
			once.Do(func() { close(full) })
		}
		select {
		case <-full:
		case <-time.After(5 * time.Second):
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
	if got := peak.Load(); got != maxParallel {
		t.Fatalf("ran %d at once, want exactly the limit of %d", got, maxParallel)
	}
}

// Worker provisioning runs a few at a time; this is the fan-out it uses.
package fleet

import "sync"

// maxParallel caps how many workers are provisioned at once.
//
// ponytail: a fixed 4. Provisioning is mostly waiting — 125MB of tar and a
// round trip to GitHub — so serial scaling made `scale 8` take eight times
// longer than it had to, but past a handful the tar extractions just fight over
// the disk. Make it configurable only if a machine is visibly bored.
const maxParallel = 4

// each runs fn over every index, a few at a time, and reports the first error
// once they have all finished — a half-provisioned worker left behind by an
// early return would be worse than waiting for its sibling.
//
// fn (and therefore any progress callback it holds) is called from several
// goroutines at once.
func each(idx []int, fn func(int) error) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	sem := make(chan struct{}, maxParallel)
	for _, i := range idx {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := fn(i); err != nil {
				mu.Lock()
				defer mu.Unlock()
				if first == nil {
					first = err
				}
			}
		}()
	}
	wg.Wait()
	return first
}

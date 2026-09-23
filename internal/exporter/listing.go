package exporter

import "fmt"

// ObserveListing records one fleet listing's outcome, called unconditionally
// from serve's poll loop whether the supervisor answered or not. A listing
// that keeps failing would otherwise be invisible: the worker state it left
// behind keeps rendering as a healthy, static fleet forever. Render exports
// hangar_fleet_list_failures_total and hangar_fleet_list_success_timestamp_seconds
// from what this records, so a stuck supervisor shows up on its own instead
// of only freezing the per-worker series.
//
// The error is logged on the transition into failure and on recovery, not on
// every poll — a supervisor down for an hour logs twice, not seven hundred
// times.
func (c *Collector) ObserveListing(err error) {
	c.mu.Lock()
	failing := err != nil
	transitioned := failing != c.listFailing
	c.listFailing = failing
	if failing {
		c.listFailures++
	} else {
		c.listSuccessAt = c.now()
	}
	c.mu.Unlock()

	switch {
	case !transitioned:
	case failing:
		fmt.Printf("  fleet listing failing: %v\n", err)
	default:
		fmt.Println("  fleet listing recovered")
	}
}

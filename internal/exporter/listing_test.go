package exporter

import (
	"errors"
	"testing"
	"time"
)

// A fleet listing that keeps failing must show up in the metrics: the
// failure counter keeps moving, and the success timestamp holds at its last
// value rather than looking like nothing ever went wrong.
func TestObserveListing(t *testing.T) {
	c := NewCollector("v", "c")
	c.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

	mustHave(t, render(c),
		`hangar_fleet_list_failures_total 0`,
		`hangar_fleet_list_success_timestamp_seconds 0`,
	)

	c.ObserveListing(errors.New("systemctl: timeout"))
	mustHave(t, render(c),
		`hangar_fleet_list_failures_total 1`,
		`hangar_fleet_list_success_timestamp_seconds 0`,
	)

	c.ObserveListing(errors.New("systemctl: timeout"))
	mustHave(t, render(c), `hangar_fleet_list_failures_total 2`)

	c.ObserveListing(nil)
	mustHave(t, render(c),
		`hangar_fleet_list_failures_total 2`,
		`hangar_fleet_list_success_timestamp_seconds 1800000000`,
	)

	// A later failure must not erase when listing last succeeded.
	c.ObserveListing(errors.New("systemctl: timeout"))
	mustHave(t, render(c),
		`hangar_fleet_list_failures_total 3`,
		`hangar_fleet_list_success_timestamp_seconds 1800000000`,
	)
}

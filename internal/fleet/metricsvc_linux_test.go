//go:build linux

package fleet

import (
	"strings"
	"testing"
)

// The exporter's unit restarts it like a worker's does, starts at boot, and
// passes the binary as an argument so any path survives.
func TestRenderMetricsUnit(t *testing.T) {
	unit := renderMetricsUnit("127.0.0.1:9151", "/srv/hangar/hangar", `/srv/han$gar/.bin/hangar`, "/srv/hangar/hangar/logs/metrics.log")
	for _, want := range []string{
		`ExecStart=/usr/bin/env "/srv/han$$gar/.bin/hangar" serve`,
		"WorkingDirectory=/srv/hangar/hangar",
		"Restart=always",
		"WantedBy=default.target",
		"StandardOutput=append:/srv/hangar/hangar/logs/metrics.log",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q\n%s", want, unit)
		}
	}
	if _, isWorker := unitIndex(metricsUnit); isWorker {
		t.Error("the exporter's unit must never parse as a worker's")
	}
}

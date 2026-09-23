//go:build linux

package fleet

import (
	"os"
	"path/filepath"
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

// StartMetrics and StopMetrics must refuse to touch a unit that already
// serves a different checkout — its WorkingDirectory or the binary its
// ExecStart runs falls outside this checkout's Root — and leave one that is
// unwritten, or already this checkout's own, alone to proceed.
func TestForeignMetricsRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hangar-metrics.service")
	if _, ok := foreignMetricsRoot(path, "/srv/a"); ok {
		t.Error("a unit that has not been written yet must not be treated as foreign")
	}

	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(renderMetricsUnit("127.0.0.1:9151", "/srv/a", "/srv/a/.bin/hangar", "/srv/a/logs/metrics.log"))
	if _, ok := foreignMetricsRoot(path, "/srv/a"); ok {
		t.Error("this checkout's own unit must not be treated as foreign")
	}

	write(renderMetricsUnit("127.0.0.1:9151", "/srv/b", "/srv/b/.bin/hangar", "/srv/b/logs/metrics.log"))
	if root, ok := foreignMetricsRoot(path, "/srv/a"); !ok || root != "/srv/b" {
		t.Errorf("foreignMetricsRoot(WorkingDirectory=/srv/b) = %q, %v, want /srv/b, true", root, ok)
	}

	// A unit whose WorkingDirectory agrees but whose binary does not must
	// still be caught — the binary is the second signal, not a fallback only
	// used when the first is absent.
	write(renderMetricsUnit("127.0.0.1:9151", "/srv/a", "/srv/b/.bin/hangar", "/srv/a/logs/metrics.log"))
	if root, ok := foreignMetricsRoot(path, "/srv/a"); !ok || root != "/srv/b" {
		t.Errorf("foreignMetricsRoot(bin=/srv/b/.bin/hangar) = %q, %v, want /srv/b, true", root, ok)
	}
}

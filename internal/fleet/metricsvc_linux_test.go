//go:build linux

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
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

// fakeSystemctlAlwaysUp puts a script named systemctl ahead of the real one
// on PATH that reports a live pid for any `show --property=MainPID` query,
// standing in for a user manager where the queried unit is actually running
// — so a test can tell whether MetricsRunning asked at all.
func fakeSystemctlAlwaysUp(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho 4242\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// MetricsRunning must report false for a checkout that never started its own
// exporter, even though another checkout's is live under the same account —
// `make status` must not print a green metrics line, on what may even be a
// different port, for an exporter this checkout never started. The fake
// systemctl reports the unit as up unconditionally, so this only passes if
// MetricsRunning checks ownership before it ever asks.
func TestMetricsRunningIgnoresAForeignUnit(t *testing.T) {
	fakeSystemctlAlwaysUp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := renderMetricsUnit("127.0.0.1:9151", "/srv/b", "/srv/b/.bin/hangar", "/srv/b/logs/metrics.log")
	if err := os.WriteFile(filepath.Join(dir, metricsUnit), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	f := New(&config.Config{Root: "/srv/a", NamePrefix: "t-w"})
	if f.MetricsRunning() {
		t.Error("a foreign checkout's exporter must not report as this checkout's own")
	}
}

// StopMetrics's systemctl calls are best-effort and must run even when
// userUnitDir() cannot resolve the unit directory — only the
// foreign-checkout ownership check and the unit file removal, which both
// need that directory, may be skipped. fakeSystemctl is defined in
// systemd_test.go.
func TestStopMetricsRunsSystemctlWithoutAUnitDir(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	fakeSystemctl(t, log)
	t.Setenv("HOME", "") // os.UserHomeDir reads only $HOME on Linux

	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	if err := f.StopMetrics(); err != nil {
		t.Fatalf("StopMetrics() = %v, want nil", err)
	}

	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("systemctl was never invoked: %v", err)
	}
	for _, want := range []string{"disable --now", "daemon-reload", "reset-failed"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("systemctl calls = %q, missing %q", out, want)
		}
	}
}

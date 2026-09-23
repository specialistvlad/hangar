//go:build darwin

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

func TestRenderMetricsPlist(t *testing.T) {
	body, err := renderMetricsPlist("/Users/me/h & co/.bin/hangar", "/Users/me/h & co", "/l/metrics.log")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>/Users/me/h &amp; co/.bin/hangar</string><string>serve</string>",
		"<key>KeepAlive</key><true/>",
		"<key>RunAtLoad</key><true/>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q\n%s", want, body)
		}
	}
	if labelIndex.MatchString(metricsLabel) {
		t.Error("the exporter's label must never parse as a worker's")
	}
}

// StartMetrics and StopMetrics must refuse to touch a plist that already
// serves a different checkout — its WorkingDirectory or the binary its
// ProgramArguments runs falls outside this checkout's Root — and leave one
// that is unwritten, or already this checkout's own, alone to proceed.
func TestForeignMetricsRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.hangar.metrics.plist")
	if _, ok := foreignMetricsRoot(path, "/srv/a"); ok {
		t.Error("a plist that has not been written yet must not be treated as foreign")
	}

	write := func(bin, root string) {
		body, err := renderMetricsPlist(bin, root, "/l/metrics.log")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("/srv/a/.bin/hangar", "/srv/a")
	if _, ok := foreignMetricsRoot(path, "/srv/a"); ok {
		t.Error("this checkout's own plist must not be treated as foreign")
	}

	write("/srv/b/.bin/hangar", "/srv/b")
	if root, ok := foreignMetricsRoot(path, "/srv/a"); !ok || root != "/srv/b" {
		t.Errorf("foreignMetricsRoot(WorkingDirectory=/srv/b) = %q, %v, want /srv/b, true", root, ok)
	}

	// A plist whose WorkingDirectory agrees but whose binary does not must
	// still be caught — the binary is the second signal, not a fallback only
	// used when the first is absent.
	write("/srv/b/.bin/hangar", "/srv/a")
	if root, ok := foreignMetricsRoot(path, "/srv/a"); !ok || root != "/srv/b" {
		t.Errorf("foreignMetricsRoot(bin=/srv/b/.bin/hangar) = %q, %v, want /srv/b, true", root, ok)
	}
}

// fakeLaunchctlAlwaysUp puts a script named launchctl ahead of the real one
// on PATH that lists the exporter's label with a live pid for any `list`
// call, standing in for a launchd where the queried agent is actually
// running — so a test can tell whether MetricsRunning asked at all.
func fakeLaunchctlAlwaysUp(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"4242 0 " + metricsLabel + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// MetricsRunning must report false for a checkout that never started its own
// exporter, even though another checkout's is live under the same account —
// `make status` must not print a green metrics line, on what may even be a
// different port, for an exporter this checkout never started. The fake
// launchctl reports the agent as up unconditionally, so this only passes if
// MetricsRunning checks ownership before it ever asks.
func TestMetricsRunningIgnoresAForeignPlist(t *testing.T) {
	fakeLaunchctlAlwaysUp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := renderMetricsPlist("/srv/b/.bin/hangar", "/srv/b", "/l/metrics.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metricsLabel+".plist"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	f := New(&config.Config{Root: "/srv/a", NamePrefix: "t-w"})
	if f.MetricsRunning() {
		t.Error("a foreign checkout's exporter must not report as this checkout's own")
	}
}

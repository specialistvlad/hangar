//go:build darwin

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

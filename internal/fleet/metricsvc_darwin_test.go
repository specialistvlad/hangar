//go:build darwin

package fleet

import (
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

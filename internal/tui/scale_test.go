package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
	"github.com/specialistvlad/hangar/internal/fleet"
)

// model builds a dashboard over a fake fleet of n on-disk workers. The worker
// directories are the fleet's whole state, so nothing else needs faking.
func model(t *testing.T, n int) *Model {
	t.Helper()
	root := t.TempDir()
	for i := 1; i <= n; i++ {
		if err := os.MkdirAll(filepath.Join(root, "workers", "w"+string(rune('0'+i))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := New(fleet.New(&config.Config{Root: root, NamePrefix: "test-w"}))
	m.refreshWorkers()
	return m
}

func TestScaleDownRefusesBusyWorker(t *testing.T) {
	m := model(t, 2)
	m.workers[2].Busy = true

	if cmd := m.scaleBy(-1); cmd != nil {
		t.Fatal("scaling down onto a busy worker should do nothing")
	}
	if _, text := m.scale.read(); !strings.Contains(text, "w2 is busy") {
		t.Fatalf("want a busy warning, got %q", text)
	}
}

func TestScaleNeedsGitHubCredentials(t *testing.T) {
	m := model(t, 2)

	if cmd := m.scaleBy(-1); cmd != nil {
		t.Fatal("scaling without GH_TOKEN should not start")
	}
	if _, text := m.scale.read(); !strings.Contains(text, "GH_TOKEN") {
		t.Fatalf("want the missing-credentials message, got %q", text)
	}
}

func TestScaleClampsToLimits(t *testing.T) {
	if cmd := model(t, 0).scaleBy(-1); cmd != nil {
		t.Fatal("cannot scale below zero")
	}
}

package tui

import (
	"fmt"
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
		if err := os.MkdirAll(filepath.Join(root, "workers", fmt.Sprintf("w%d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := New(fleet.New(&config.Config{
		Root: root, NamePrefix: "test-w", Token: "t", Org: "o",
	}))
	m.refreshWorkers()
	return m
}

// Presses must not wait on the reconcile: the second and third + move the
// target while the first pass is still running, and only one pass runs at once.
func TestPressesQueueOntoTarget(t *testing.T) {
	m := model(t, 1)

	if cmd := m.scaleBy(1); cmd == nil {
		t.Fatal("first press should start a pass")
	}
	for i := 0; i < 2; i++ {
		if cmd := m.scaleBy(1); cmd != nil {
			t.Fatalf("press %d started a second concurrent pass", i+2)
		}
	}
	if m.want != 4 {
		t.Fatalf("want target 4, got %d", m.want)
	}

	// The pass that finishes reports the count it reached; the target moved past
	// it, so the model must chase it rather than stop.
	if cmd := m.scaleDone(scaleDoneMsg{reached: 2}); cmd == nil {
		t.Fatal("a stale target should start another pass")
	}
	if cmd := m.scaleDone(scaleDoneMsg{reached: 4}); cmd != nil {
		t.Fatal("reaching the target should stop")
	}
	if m.scaling {
		t.Fatal("still marked as scaling after the last pass")
	}
}

func TestFailedPassReaimsAtReality(t *testing.T) {
	m := model(t, 2)
	m.scaleBy(1)

	if cmd := m.scaleDone(scaleDoneMsg{reached: 3, err: fmt.Errorf("boom")}); cmd != nil {
		t.Fatal("a failed pass should not retry on its own")
	}
	if m.want != 2 {
		t.Fatalf("want target back at the real count 2, got %d", m.want)
	}
	if note := m.scaleNote(); !strings.Contains(note, "boom") {
		t.Fatalf("want the error in the footer, got %q", note)
	}
}

func TestScaleDownRefusesBusyWorker(t *testing.T) {
	m := model(t, 2)
	m.workers[2].Busy = true

	if cmd := m.scaleBy(-1); cmd != nil {
		t.Fatal("scaling down onto a busy worker should do nothing")
	}
	if note := m.scaleNote(); !strings.Contains(note, "w2 is busy") {
		t.Fatalf("want a busy warning, got %q", note)
	}
}

func TestScaleNeedsGitHubCredentials(t *testing.T) {
	m := model(t, 2)
	m.flt.Config().Token = ""

	if cmd := m.scaleBy(-1); cmd != nil {
		t.Fatal("scaling without GH_TOKEN should not start")
	}
	if note := m.scaleNote(); !strings.Contains(note, "GH_TOKEN") {
		t.Fatalf("want the missing-credentials message, got %q", note)
	}
}

func TestScaleClampsToLimits(t *testing.T) {
	if cmd := model(t, 0).scaleBy(-1); cmd != nil {
		t.Fatal("cannot scale below zero")
	}
	m := model(t, 0)
	m.want, m.scaling = config.MaxWorkers, true
	if cmd := m.scaleBy(1); cmd != nil || m.want != config.MaxWorkers {
		t.Fatal("cannot queue past the worker limit")
	}
}

package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/fleet"
)

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// While a pass runs, the first q waits for it — its tar and config.sh would
// outlive the dashboard — and says so; the second quits at once.
func TestQuitWaitsForARunningPass(t *testing.T) {
	m := model(t, 1)
	m.scaling = true
	if cmd := m.quit(); cmd != nil || !m.quitting {
		t.Fatal("the first q during a pass must wait, not quit")
	}
	if note := m.scaleNote(); !strings.Contains(note, "q again to quit now") {
		t.Errorf("footer should say what a second q does, got %q", note)
	}
	if !isQuit(m.quit()) {
		t.Error("the second q must quit")
	}

	m = model(t, 1)
	if !isQuit(m.quit()) {
		t.Error("with no pass running, q quits at once")
	}
}

// A pass landing after a q quits the dashboard — unless it failed, when the
// error stays on screen instead of vanishing with it.
func TestPassLandingAfterQuit(t *testing.T) {
	m := model(t, 1)
	m.scaling, m.quitting = true, true
	if !isQuit(m.scaleDone(scaleDoneMsg{reached: 1, fleet: m.flt.List()})) {
		t.Error("a pass that lands after a q should quit")
	}

	m = model(t, 1)
	m.scaling, m.quitting = true, true
	if cmd := m.scaleDone(scaleDoneMsg{reached: 1, fleet: m.flt.List(), err: errors.New("boom")}); isQuit(cmd) {
		t.Error("a failed pass must keep the dashboard open")
	}
	if m.quitting || !strings.Contains(m.scaleNote(), "boom") {
		t.Errorf("the failure should be on screen, got %q", m.scaleNote())
	}
}

// Chasing a lower target re-checks the workers it would remove: one may have
// picked up a job since the keypress.
func TestChaseStopsAtABusyWorker(t *testing.T) {
	m := model(t, 3)
	m.scaling, m.want = true, 1
	m.workers[3].Busy = true
	if cmd := m.scaleDone(scaleDoneMsg{reached: 2, fleet: m.flt.List()}); cmd != nil {
		t.Fatal("the chase must not remove a busy worker")
	}
	if !strings.Contains(m.scaleNote(), "w3 is busy") {
		t.Errorf("expected the busy worker named, got %q", m.scaleNote())
	}
}

// Once q has committed to quitting after the in-flight pass, a further + must
// not move the target the header promises: the dashboard is not going to
// chase it.
func TestScaleIgnoredAfterQuitRequested(t *testing.T) {
	m := model(t, 1)
	m.scaling, m.want = true, 1
	if cmd := m.quit(); cmd != nil || !m.quitting {
		t.Fatal("q during a running pass should wait, not quit")
	}

	if cmd := m.scaleBy(1); cmd != nil {
		t.Fatal("+ after q must not start a pass")
	}
	if m.want != 1 {
		t.Errorf("want target unchanged at 1, got %d", m.want)
	}
}

// A keypress never queues behind a scale another process is running.
func TestKeypressDoesNotWaitForAnotherScale(t *testing.T) {
	m := model(t, 1)
	// Held the way another hangar process holds it: a flock on the lock file.
	fh, err := os.OpenFile(filepath.Join(m.flt.Config().WorkersDir(), ".scale.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	msg, ok := m.startScale()().(scaleDoneMsg)
	if !ok || !errors.Is(msg.err, fleet.ErrScaleBusy) {
		t.Fatalf("got %+v, want ErrScaleBusy at once", msg)
	}
}

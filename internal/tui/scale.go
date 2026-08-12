package tui

import (
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/config"
)

// Scaling from the dashboard runs the same reconcile `make N` runs, and it is
// slow — a new worker unpacks a ~125MB tarball and registers with GitHub. So a
// keypress never waits for it. It moves a target (m.want) and returns; one pass
// runs at a time, and when that pass lands on a stale target another starts.
// Holding + therefore counts up immediately and the fleet catches up after.
//
// Everything that decides is in Update, which bubbletea runs single-threaded.
// The only thing the scaling goroutine touches is the progress line, which
// carries its own lock for that reason.
type progress struct {
	mu   sync.Mutex
	text string
}

func (p *progress) set(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text = text
}

func (p *progress) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text
}

// scaleDoneMsg reports one finished pass, and the count it reconciled to — the
// target may have moved on while it ran.
type scaleDoneMsg struct {
	reached int
	err     error
}

// scaleBy nudges the desired worker count by delta.
func (m *Model) scaleBy(delta int) tea.Cmd {
	have := len(m.flt.List())
	base := have
	if m.scaling {
		base = m.want // stack presses onto the pending target, not onto the past
	}
	n := base + delta
	if n < 0 || n > config.MaxWorkers {
		return nil
	}
	// A single keypress must not be able to kill a running build. `make N` still
	// can, because there it is what the operator explicitly typed.
	for _, w := range m.sorted() {
		if w.Index > n && w.Busy {
			m.status.set(fmt.Sprintf("w%d is busy — not removing it (use `make %d` to force)", w.Index, n))
			return nil
		}
	}
	if err := m.flt.Config().RequireGitHub(); err != nil {
		m.status.set(err.Error())
		return nil
	}

	m.want = n
	if m.scaling || n == have {
		return nil // the running pass will pick the new target up when it lands
	}
	return m.startScale()
}

// startScale launches one reconcile pass toward the current target.
func (m *Model) startScale() tea.Cmd {
	m.scaling = true
	n := m.want
	m.status.set("")
	return func() tea.Msg { return scaleDoneMsg{n, m.flt.Scale(n, m.status.set)} }
}

// scaleNote is the footer's one line about scaling: the target while a pass is
// running, with whatever step it is on, or the last error once it stopped.
func (m *Model) scaleNote() string {
	text := m.status.get()
	if !m.scaling {
		return text
	}
	note := fmt.Sprintf("→ %d workers", m.want)
	if text != "" {
		note += ": " + text
	}
	return note
}

// scaleDone handles a finished pass and chases the target if it moved.
func (m *Model) scaleDone(msg scaleDoneMsg) tea.Cmd {
	m.scaling = false
	m.refreshWorkers()
	m.layout() // the worker table just changed height

	if msg.err != nil {
		// Re-aim at reality: a failed pass leaves the fleet wherever it got to,
		// and the next keypress should count from there rather than from a target
		// that was never reached.
		m.want = len(m.flt.List())
		m.status.set(msg.err.Error())
		return nil
	}
	m.status.set("")
	if m.want != msg.reached {
		return m.startScale()
	}
	return nil
}

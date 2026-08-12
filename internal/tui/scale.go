package tui

import (
	"fmt"
	"time"

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
// The running pass only writes to a channel, which arrives as a message like
// any log line does — so its progress reaches the screen the moment it happens
// rather than on the next tick.
type scaleMsg string

// scaleDoneMsg reports one finished pass, and the count it reconciled to — the
// target may have moved on while it ran.
type scaleDoneMsg struct {
	reached int
	err     error
}

func waitProgress(ch <-chan string) tea.Cmd {
	return func() tea.Msg { return scaleMsg(<-ch) }
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
			m.scaleErr = fmt.Sprintf("w%d is busy — not removing it (use `make %d` to force)", w.Index, n)
			return nil
		}
	}
	if err := m.flt.Config().RequireGitHub(); err != nil {
		m.scaleErr = err.Error()
		return nil
	}

	m.want, m.scaleErr = n, ""
	if m.scaling || n == have {
		m.layout() // the pending rows changed; show them now, not after the pass
		return nil
	}
	return m.startScale()
}

// startScale launches one reconcile pass toward the current target.
func (m *Model) startScale() tea.Cmd {
	m.scaling, m.scaleSince, m.note = true, time.Now(), ""
	m.layout()

	n, ch := m.want, m.progress
	return func() tea.Msg {
		err := m.flt.Scale(n, func(line string) {
			// Dropping a stale line beats blocking the reconcile on a screen that
			// is not draining, e.g. after the dashboard has quit.
			select {
			case ch <- line:
			default:
			}
		})
		return scaleDoneMsg{n, err}
	}
}

// scaleDone handles a finished pass and chases the target if it moved.
func (m *Model) scaleDone(msg scaleDoneMsg) tea.Cmd {
	m.scaling, m.note = false, ""
	m.refreshWorkers()

	if msg.err != nil {
		// Re-aim at reality: a failed pass leaves the fleet wherever it got to,
		// and the next keypress should count from there rather than from a target
		// that was never reached.
		m.want, m.scaleErr = len(m.flt.List()), msg.err.Error()
		m.layout()
		return nil
	}
	if m.want != msg.reached {
		return m.startScale()
	}
	m.layout() // the worker table just changed height
	return nil
}

// pendingLines renders the workers a keypress has asked for but the reconcile
// has not created yet, so a press shows up in the table immediately instead of
// a minute later when the runner finishes registering.
func (m *Model) pendingLines() []string {
	var out []string
	for i := 1; i <= m.want; i++ {
		if m.workers[i] != nil {
			continue
		}
		// Workers are provisioned in ascending order, so the first missing one is
		// the one being worked on and the rest are still waiting their turn.
		state := stDim.Render("queued")
		if len(out) == 0 && m.scaling {
			state = stWarn.Render("provisioning…")
		}
		out = append(out, fmt.Sprintf(" %s %s  %s", stDim.Render("◌"),
			prefixStyle(i).Render(fmt.Sprintf("w%-2d", i)), state))
	}
	return out
}

// scaleNote is the footer's line about scaling: the target and the step it is
// on while a pass runs, or the last error once one stopped.
func (m *Model) scaleNote() string {
	if !m.scaling {
		return m.scaleErr
	}
	note := fmt.Sprintf("→ %d workers", m.want)
	if m.note != "" {
		note += " · " + m.note
	}
	return note + " · " + dur(time.Since(m.scaleSince))
}

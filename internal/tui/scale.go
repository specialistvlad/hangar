package tui

import (
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/config"
)

// Scaling from the dashboard runs exactly the reconcile `make N` runs, just
// started by a keypress. It is slow — a new worker unpacks a ~125MB tarball and
// registers with GitHub — so it runs as a bubbletea command rather than inline
// in Update, and reports through a status line the render pass reads.
type scaleStatus struct {
	mu   sync.Mutex
	busy bool
	text string
}

// set is handed to Fleet.Scale as its progress callback, so it is called from
// the scaling goroutine while View reads text — hence the lock.
func (s *scaleStatus) set(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = text
}

func (s *scaleStatus) start(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.text = true, text
}

// finish clears the line on success and leaves the error up on failure, since
// the failure is the only thing the operator still needs to see.
func (s *scaleStatus) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.text = false, ""
	if err != nil {
		s.text = err.Error()
	}
}

func (s *scaleStatus) read() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy, s.text
}

type scaleDoneMsg struct{ err error }

// scaleBy reconciles the fleet to its current size plus delta.
func (m *Model) scaleBy(delta int) tea.Cmd {
	if busy, _ := m.scale.read(); busy {
		return nil
	}
	n := len(m.flt.List()) + delta
	if n < 0 || n > config.MaxWorkers {
		return nil
	}
	// A single keypress must not be able to kill a running build. `make N` still
	// can, because there it is what the operator explicitly typed.
	for _, w := range m.sorted() {
		if w.Index > n && w.Busy {
			m.scale.set(fmt.Sprintf("w%d is busy — not removing it (use `make %d` to force)", w.Index, n))
			return nil
		}
	}
	if err := m.flt.Config().RequireGitHub(); err != nil {
		m.scale.set(err.Error())
		return nil
	}

	m.scale.start(fmt.Sprintf("scaling to %d…", n))
	return func() tea.Msg { return scaleDoneMsg{m.flt.Scale(n, m.scale.set)} }
}

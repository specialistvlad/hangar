// Worker bookkeeping: the model's view of who exists, what they are doing, and
// which log watchers are running for them.
package tui

import (
	"context"
	"sort"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/logs"
)

func (m *Model) apply(e logs.Event) {
	w := m.workers[e.Worker]
	if w == nil {
		w = &workerState{Index: e.Worker, Running: true}
		m.workers[e.Worker] = w
	}
	switch e.Kind {
	case logs.KindJobStart:
		w.Job, w.Busy, w.Since, w.LastResult = e.Text, true, time.Now(), ""
	case logs.KindJobEnd:
		w.Busy, w.Job, w.LastResult = false, "", e.Result
	case logs.KindLine:
		if collapseInto(m.lines, e.Worker, e.Text) {
			m.render()
			return
		}
		m.lines = append(m.lines, logLine{e.Worker, e.Text})
		if len(m.lines) > maxLines {
			m.lines = m.lines[len(m.lines)-maxLines:]
		}
		m.render()
	}
}

// refreshWorkers applies a fleet listing, so a fleet scaled from here or from
// another terminal shows up without restarting the dashboard. Watchers are
// started and stopped from the same place, since a worker appearing at any
// point after Init needs one just as much.
func (m *Model) refreshWorkers(ws []fleet.Worker) {
	seen := map[int]bool{}
	for _, w := range ws {
		seen[w.Index] = true
		st := m.workers[w.Index]
		if st == nil {
			st = &workerState{Index: w.Index, Name: w.Name}
			m.workers[w.Index] = st
		}
		st.Running = w.Running
		if m.watching[w.Index] == nil && m.ctx != nil {
			ctx, cancel := context.WithCancel(m.ctx)
			m.watching[w.Index] = cancel
			go logs.Watch(ctx, w.Index, w.Dir, m.events)
		}
	}
	for i := range m.workers {
		if seen[i] {
			continue
		}
		if cancel := m.watching[i]; cancel != nil {
			cancel()
			delete(m.watching, i)
		}
		delete(m.workers, i)
	}
}

func (m *Model) sorted() []*workerState {
	out := make([]*workerState, 0, len(m.workers))
	for _, w := range m.workers {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

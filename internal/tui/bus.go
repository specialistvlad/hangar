package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/metrics"
)

// Nothing expensive may run inside Update. Listing the fleet shells out to
// launchctl and sampling the host runs ps, vm_stat and sysctl — a keypress
// arriving during one of those used to wait behind it, which is what made
// scaling feel like it had frozen the dashboard.
//
// So every source of state is a goroutine on the far side of a channel, and
// Update does nothing but consume messages: log lines, scale progress, fleet
// listings and metrics all arrive the same way.
const pollInterval = time.Second

// fleetMsg is the fleet as it exists on disk. It doubles as the dashboard's
// heartbeat — the elapsed times redraw when it lands, so there is no separate
// ticker.
type fleetMsg []fleet.Worker

type sampleMsg metrics.Frame

// poll sends fn's result on ch every interval until ctx ends.
func poll[T any](ctx context.Context, ch chan<- T, fn func() T) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case ch <- fn():
		case <-ctx.Done():
			return
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// recv turns a channel into a bubbletea command. It is re-issued after every
// message so the stream keeps flowing without a second event loop.
func recv[T tea.Msg](ch <-chan T) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

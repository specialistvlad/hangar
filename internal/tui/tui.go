// Package tui renders the live fleet dashboard.
//
// The model never owns a runner process, so quitting it cannot stop a build —
// that is why the footer can promise runners keep running. The one thing it
// writes is a +/- scale, which hands off to the same fleet reconcile `make N`
// uses and then goes back to reading.
package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/logs"
	"github.com/specialistvlad/hangar/internal/metrics"
)

const (
	maxLines   = 5000
	sparkWidth = 24
)

type tickMsg time.Time

type workerState struct {
	Index      int
	Name       string
	Job        string
	Since      time.Time
	Busy       bool
	Running    bool
	LastResult string
}

type logLine struct {
	worker int
	text   string
}

// Model is the bubbletea model backing the dashboard.
type Model struct {
	flt     *fleet.Fleet
	sampler *metrics.Sampler
	events  chan logs.Event
	ctx     context.Context
	cancel  context.CancelFunc

	workers  map[int]*workerState
	watching map[int]context.CancelFunc
	lines    []logLine
	snap     metrics.Snapshot

	// want is the worker count the operator has asked for; scaling says whether
	// a reconcile pass is chasing it right now, note is the step it is on and
	// scaleErr the last failure, kept on screen until the next press.
	want       int
	scaling    bool
	scaleSince time.Time
	note       string
	scaleErr   string
	progress   chan string

	focus     int // 0 shows every worker
	follow    bool
	filter    textinput.Model
	filtering bool
	vp        viewport.Model
	ready     bool
	w, h      int
}

// New builds a dashboard for the given fleet.
func New(f *fleet.Fleet) *Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 64

	return &Model{
		flt:      f,
		sampler:  metrics.NewSampler(),
		events:   make(chan logs.Event, 1024),
		progress: make(chan string, 16),
		workers:  map[int]*workerState{},
		watching: map[int]context.CancelFunc{},
		follow:   true,
		filter:   ti,
	}
}

// Init starts the log watchers and the metrics ticker.
func (m *Model) Init() tea.Cmd {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.refreshWorkers()
	return tea.Batch(tick(), waitFor(m.events), waitProgress(m.progress))
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// waitFor turns the log channel into a bubbletea command; it is re-issued after
// every event so the stream keeps flowing without a second event loop.
func waitFor(ch <-chan logs.Event) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		m.snap = m.sampler.Sample()
		m.refreshWorkers()
		return m, tick()

	case logs.Event:
		m.apply(msg)
		return m, waitFor(m.events)

	case scaleMsg:
		m.note = string(msg)
		return m, waitProgress(m.progress)

	case scaleDoneMsg:
		return m, m.scaleDone(msg)

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		return m.filterKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c", "esc":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "a", "0":
		m.focus = 0
		m.render()
		return m, nil
	case "+", "=":
		return m, m.scaleBy(1)
	case "-", "_":
		return m, m.scaleBy(-1)
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.vp.GotoBottom()
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.focus = int(msg.String()[0] - '0')
		m.render()
		return m, nil
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	// Scrolling away from the bottom means the reader wants to stay put;
	// scrolling back means they want to follow again.
	m.follow = m.vp.AtBottom()
	return m, cmd
}

func (m *Model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter, tea.KeyEscape:
		m.filtering = false
		m.filter.Blur()
		if msg.Type == tea.KeyEscape {
			m.filter.SetValue("")
		}
		m.render()
		return m, nil
	default:
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.render()
		return m, cmd
	}
}

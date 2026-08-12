package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/specialistvlad/hangar/internal/metrics"
)

// Adaptive colors so the dashboard stays readable in a light terminal too.
var (
	cBase   = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	cDim    = lipgloss.AdaptiveColor{Light: "244", Dark: "243"}
	cAccent = lipgloss.AdaptiveColor{Light: "26", Dark: "39"}
	cOK     = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	cWarn   = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	cErr    = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}

	stTitle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stDim   = lipgloss.NewStyle().Foreground(cDim)
	stBase  = lipgloss.NewStyle().Foreground(cBase)
	stRule  = lipgloss.NewStyle().Foreground(cDim)
	stBusy  = lipgloss.NewStyle().Foreground(cOK)
	stIdle  = lipgloss.NewStyle().Foreground(cDim)
	stErr   = lipgloss.NewStyle().Foreground(cErr)
	stWarn  = lipgloss.NewStyle().Foreground(cWarn)

	// Distinct prefix colors so interleaved output stays readable.
	prefixColors = []lipgloss.AdaptiveColor{
		{Light: "26", Dark: "39"}, {Light: "90", Dark: "141"},
		{Light: "28", Dark: "42"}, {Light: "130", Dark: "214"},
		{Light: "31", Dark: "80"}, {Light: "125", Dark: "212"},
	}
)

func prefixStyle(n int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(prefixColors[(n-1)%len(prefixColors)]).Bold(true)
}

// layout resizes the log viewport to whatever the header and worker table left.
func (m *Model) layout() {
	head := len(m.headerLines()) + len(m.workerLines()) + 3
	h := m.h - head
	if h < 3 {
		h = 3
	}
	if !m.ready {
		m.vp = viewport.New(m.w, h)
		m.ready = true
	} else {
		m.vp.Width, m.vp.Height = m.w, h
	}
	m.render()
}

// render rebuilds the log pane from the ring buffer, applying focus and filter.
func (m *Model) render() {
	if !m.ready {
		return
	}
	needle := strings.ToLower(m.filter.Value())
	var b strings.Builder
	last := -1
	for _, l := range m.lines {
		if m.focus != 0 && l.worker != m.focus {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(l.text), needle) {
			continue
		}
		// Label a run of lines once. Repeating the tag on every line of a long
		// build turns the left edge into noise and steals width from the output.
		if l.worker != last {
			b.WriteString(prefixStyle(l.worker).Render(fmt.Sprintf("w%-2d│ ", l.worker)))
			last = l.worker
		} else {
			b.WriteString("    │ ")
		}
		b.WriteString(stBase.Render(l.text))
		b.WriteByte('\n')
	}
	m.vp.SetContent(b.String())
	if m.follow {
		m.vp.GotoBottom()
	}
}

// headerLines renders the title and the two resource rows. The docker VM gets
// its own row because that is where build CPU actually lands — the runner
// processes themselves sit near idle while BuildKit does the work.
func (m *Model) headerLines() []string {
	cfg := m.flt.Config()
	busy := 0
	for _, w := range m.workers {
		if w.Busy {
			busy++
		}
	}

	title := stTitle.Render("hangar") + "  " +
		stDim.Render(fmt.Sprintf("%d workers · %d busy · %s/%s",
			len(m.workers), busy, cfg.Org, orDash(cfg.Group)))

	s := m.snap
	host := fmt.Sprintf("%s  load %s %-5.2f   mem %s %s/%s   free %s",
		stDim.Render("host  "),
		metrics.Sparkline(m.sampler.Load.Values(), sparkWidth, float64(s.NCPU)),
		s.Load1,
		metrics.Sparkline(m.sampler.Mem.Values(), sparkWidth, 100),
		gib(s.MemUsed), gib(s.MemTotal),
		diskStyle(s.DiskFree).Render(gib(s.DiskFree)),
	)

	vm := stDim.Render("docker") + "  " + stDim.Render("not running")
	if s.VMFound {
		vm = fmt.Sprintf("%s  cpu  %s %-5.0f%%  mem %s",
			stDim.Render("docker"),
			metrics.Sparkline(m.sampler.VM.Values(), sparkWidth, float64(s.NCPU)*100),
			s.VMCPU, gib(s.VMMem))
	}
	return []string{title, host, vm}
}

// workerLines renders one row per worker: state, current job, elapsed time.
func (m *Model) workerLines() []string {
	var out []string
	for _, w := range m.sorted() {
		var dot, detail string
		switch {
		case !w.Running:
			dot, detail = stErr.Render("✗"), stErr.Render("stopped")
		case w.Busy:
			dot = stBusy.Render("●")
			detail = fmt.Sprintf("%-38s %s", trunc(w.Job, 38), stDim.Render(dur(time.Since(w.Since))))
		default:
			dot, detail = stIdle.Render("○"), stDim.Render("idle")
			if w.LastResult != "" {
				st := stBusy
				if w.LastResult != "Succeeded" {
					st = stWarn
				}
				detail += "  " + st.Render("last: "+w.LastResult)
			}
		}
		out = append(out, fmt.Sprintf(" %s %s  %s", dot,
			prefixStyle(w.Index).Render(fmt.Sprintf("w%-2d", w.Index)), detail))
	}
	if len(out) == 0 {
		out = append(out, stDim.Render(" no workers — run `make 4` to create some"))
	}
	return out
}

// View renders the whole dashboard.
func (m *Model) View() string {
	if !m.ready {
		return "starting…"
	}
	rule := stRule.Render(strings.Repeat("─", maxInt(m.w, 1)))

	footer := stDim.Render(" +/- scale · 1-9 focus · a all · f follow · / filter · q quit (runners keep running)")
	if m.filtering {
		footer = " " + m.filter.View()
	} else if m.focus != 0 {
		footer = stDim.Render(fmt.Sprintf(" focus w%d ·", m.focus)) + footer
	}
	if _, text := m.scale.read(); text != "" && !m.filtering {
		footer = stWarn.Render(" "+text+" ·") + footer
	}
	if !m.follow {
		footer = stWarn.Render(" [paused]") + footer
	}

	parts := append(m.headerLines(), rule)
	parts = append(parts, m.workerLines()...)
	parts = append(parts, rule, m.vp.View(), rule, footer)
	return strings.Join(parts, "\n")
}

// diskStyle turns free space into a warning color before a build hits ENOSPC.
func diskStyle(free uint64) lipgloss.Style {
	switch {
	case free < 30<<30:
		return stErr
	case free < 80<<30:
		return stWarn
	}
	return stBase
}

func gib(b uint64) string { return fmt.Sprintf("%.0fG", float64(b)/(1<<30)) }

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

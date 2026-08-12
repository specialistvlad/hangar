// Package logs turns the runner's own diagnostic files into an event stream.
//
// No log shipping or stdout capture is involved: the runner already writes
// everything the dashboard needs. _diag/Runner_*.log carries the job state
// transitions, and _diag/pages/*.log carries the live console output of the
// running step, BuildKit lines included. Tailing those two is the whole job.
package logs

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Kind int

const (
	KindLine Kind = iota
	KindJobStart
	KindJobEnd
)

type Event struct {
	Worker int
	Kind   Kind
	Text   string // console line, or job name for start/end
	Result string // "Succeeded" / "Failed" / ... on KindJobEnd
}

var (
	reJobStart = regexp.MustCompile(`Running job: (.+?)\s*$`)
	reJobEnd   = regexp.MustCompile(`Job (.+?) completed with result: (\w+)`)
	// The runner stamps every console line with an RFC3339Nano prefix. It is
	// noise in a dashboard that already shows elapsed time per worker.
	reStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[\d:.]+Z\s?`)
	reANSI  = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	// ##[group] and friends are workflow-command markers meant for the web UI.
	reWorkflowCmd = regexp.MustCompile(`##\[(group|endgroup|command|debug|section)\]`)
)

// Watch follows one worker's logs until ctx is canceled. It starts at the end
// of the current files, so attaching mid-build shows what happens next rather
// than replaying the whole job from the beginning.
func Watch(ctx context.Context, worker int, workerDir string, out chan<- Event) {
	diag := filepath.Join(workerDir, "_diag")
	runner := &tailer{glob: filepath.Join(diag, "Runner_*.log")}
	pages := &tailer{glob: filepath.Join(diag, "pages", "*.log")}

	// Recover state before seeking past it. A dashboard opened mid-build would
	// otherwise never see the "Running job" line that already scrolled by, and
	// would report a busy worker as idle for the entire job.
	recoverState(ctx, worker, runner.newest(), out)
	runner.seekEnd()
	pages.seekEnd()

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, line := range runner.read() {
				if m := reJobStart.FindStringSubmatch(line); m != nil {
					send(ctx, out, Event{Worker: worker, Kind: KindJobStart, Text: m[1]})
				} else if m := reJobEnd.FindStringSubmatch(line); m != nil {
					send(ctx, out, Event{Worker: worker, Kind: KindJobEnd, Text: m[1], Result: m[2]})
				}
			}
			for _, line := range pages.read() {
				if line = clean(line); line != "" {
					send(ctx, out, Event{Worker: worker, Kind: KindLine, Text: line})
				}
			}
		}
	}
}

// recoverState finds the most recent job transition already in the log and
// replays it, so attaching mid-job shows what is actually running.
func recoverState(ctx context.Context, worker int, path string, out chan<- Event) {
	if path == "" {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(body), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := reJobEnd.FindStringSubmatch(lines[i]); m != nil {
			send(ctx, out, Event{Worker: worker, Kind: KindJobEnd, Text: m[1], Result: m[2]})
			return
		}
		if m := reJobStart.FindStringSubmatch(lines[i]); m != nil {
			send(ctx, out, Event{Worker: worker, Kind: KindJobStart, Text: m[1]})
			return
		}
	}
}

func send(ctx context.Context, out chan<- Event, e Event) {
	select {
	case out <- e:
	case <-ctx.Done():
	}
}

func clean(line string) string {
	line = reStamp.ReplaceAllString(line, "")
	line = reANSI.ReplaceAllString(line, "")
	line = reWorkflowCmd.ReplaceAllString(line, "")
	line = strings.ReplaceAll(line, "##[error]", "error: ")
	line = strings.ReplaceAll(line, "##[warning]", "warn: ")
	return strings.TrimRight(line, " \t\r")
}

// tailer follows whichever file matching glob was modified most recently, so
// job and step rotation is handled without tracking the runner's file naming.
type tailer struct {
	glob string
	path string
	off  int64
}

func (t *tailer) newest() string {
	matches, _ := filepath.Glob(t.glob)
	if len(matches) == 0 {
		return ""
	}
	type entry struct {
		path string
		mod  time.Time
	}
	var es []entry
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			es = append(es, entry{m, fi.ModTime()})
		}
	}
	if len(es) == 0 {
		return ""
	}
	sort.Slice(es, func(i, j int) bool { return es[i].mod.After(es[j].mod) })
	return es[0].path
}

func (t *tailer) seekEnd() {
	t.path = t.newest()
	if t.path == "" {
		return
	}
	if fi, err := os.Stat(t.path); err == nil {
		t.off = fi.Size()
	}
}

func (t *tailer) read() []string {
	newest := t.newest()
	if newest == "" {
		return nil
	}
	if newest != t.path {
		// A new job or step began; read the replacement from its start.
		t.path, t.off = newest, 0
	}

	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if fi.Size() < t.off { // truncated underneath us
		t.off = 0
	}
	if fi.Size() == t.off {
		return nil
	}
	if _, err := f.Seek(t.off, 0); err != nil {
		return nil
	}

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// The runner writes these files with a UTF-8 BOM.
		lines = append(lines, strings.TrimPrefix(sc.Text(), "\ufeff"))
	}
	t.off = fi.Size()
	return lines
}

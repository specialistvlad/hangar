// Package logs turns the runner's own diagnostic files into an event stream.
//
// No log shipping or stdout capture is involved: the runner already writes
// everything the dashboard needs. _diag/Runner_*.log carries the job state
// transitions, and _diag/pages/*.log carries the live console output of the
// running step, BuildKit lines included. Tailing those two is the whole job.
package logs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Kind int

const (
	KindLine Kind = iota
	KindJobStart
	KindJobEnd
	// KindListening is the runner's listener (re)starting — "Listening for
	// Jobs". It never happens while a job runs, so it means idle, and after a
	// listener that died mid-job it is the only sign the job is over.
	KindListening
)

type Event struct {
	Worker int
	Kind   Kind
	Text   string // console line, or job name for start/end
	Result string // "Succeeded" / "Failed" / ... on KindJobEnd
	// At is when the runner logged a job start or end, from the line's own
	// timestamp; zero when the line carried none.
	At time.Time
	// Recovered marks a transition replayed from the log on attach rather than
	// seen as it happened. A counter must not count it again.
	Recovered bool
}

var (
	reJobStart = regexp.MustCompile(`Running job: (.+?)\s*$`)
	reJobEnd   = regexp.MustCompile(`Job (.+?) completed with result: (\w+)`)
	reListen   = regexp.MustCompile(`: Listening for Jobs\s*$`)
	// The runner stamps every console line with an RFC3339Nano prefix. It is
	// noise in a dashboard that already shows elapsed time per worker.
	reStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[\d:.]+Z\s?`)
	reANSI  = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	// ##[group] and friends are workflow-command markers meant for the web UI.
	reWorkflowCmd = regexp.MustCompile(`##\[(group|endgroup|command|debug|section)\]`)
	// Every _diag line opens with the time the runner wrote it, in UTC.
	reDiagStamp = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})Z `)
)

// stampOf reads a _diag line's leading timestamp.
func stampOf(line string) time.Time {
	m := reDiagStamp.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// jobEvent turns a Runner_*.log line into a job transition, if it is one.
func jobEvent(worker int, line string) (Event, bool) {
	if m := reJobStart.FindStringSubmatch(line); m != nil {
		return Event{Worker: worker, Kind: KindJobStart, Text: m[1], At: stampOf(line)}, true
	}
	if m := reJobEnd.FindStringSubmatch(line); m != nil {
		return Event{Worker: worker, Kind: KindJobEnd, Text: m[1], Result: m[2], At: stampOf(line)}, true
	}
	if reListen.MatchString(line) {
		return Event{Worker: worker, Kind: KindListening, At: stampOf(line)}, true
	}
	return Event{}, false
}

// Watch follows one worker's logs until ctx is canceled. It starts at the end
// of the current files, so attaching mid-build shows what happens next rather
// than replaying the whole job from the beginning.
func Watch(ctx context.Context, worker int, workerDir string, out chan<- Event) {
	watch(ctx, worker, workerDir, out, true)
}

// WatchJobs is Watch without the console output: only job starts and ends, for
// a consumer that counts jobs rather than showing them.
func WatchJobs(ctx context.Context, worker int, workerDir string, out chan<- Event) {
	watch(ctx, worker, workerDir, out, false)
}

func watch(ctx context.Context, worker int, workerDir string, out chan<- Event, withPages bool) {
	diag := filepath.Join(workerDir, "_diag")
	runner := &tailer{glob: filepath.Join(diag, "Runner_*.log")}
	pages := &tailer{glob: filepath.Join(diag, "pages", "*.log")}

	// Recover state before following. A dashboard opened mid-build would
	// otherwise never see the "Running job" line that already scrolled by, and
	// would report a busy worker as idle for the entire job. Following resumes
	// exactly where recovery stopped reading, so a line written in between is
	// neither lost nor replayed twice.
	runner.path, runner.off = recoverState(ctx, worker, runner.byAge(), out)
	pages.seekEnd()

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, line := range runner.read() {
				if e, ok := jobEvent(worker, line); ok {
					send(ctx, out, e)
				}
			}
			if !withPages {
				continue
			}
			for _, line := range pages.read() {
				if line = clean(line); line != "" {
					send(ctx, out, Event{Worker: worker, Kind: KindLine, Text: line})
				}
			}
		}
	}
}

// recoverState replays, marked Recovered, what a watcher attaching now needs:
// the last job end — for the time it happened — and, if a job is running, its
// start after it. files come newest first. A newest log with no transition yet
// belongs to a listener that just started, which is idle. It returns the
// newest log and the offset just past its last complete line, where following
// continues.
func recoverState(ctx context.Context, worker int, files []string, out chan<- Event) (string, int64) {
	if len(files) == 0 {
		return "", 0
	}
	var running *Event
	var lastEnd *Event
	var path string
	var off int64
	for i, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			if i == 0 {
				// Follow from its end: reading it from the start later would replay
				// its transitions as if they were new.
				path = f
				if fi, statErr := os.Stat(f); statErr == nil {
					off = fi.Size()
				}
			}
			continue
		}
		if i == 0 {
			path, off = f, int64(strings.LastIndexByte(string(body), '\n')+1)
		}
		lines := strings.Split(string(body), "\n")
		for j := len(lines) - 1; j >= 0 && lastEnd == nil; j-- {
			e, ok := jobEvent(worker, strings.TrimPrefix(lines[j], "\ufeff"))
			if !ok {
				continue
			}
			e.Recovered = true
			switch {
			case e.Kind == KindJobEnd:
				lastEnd = &e
			case e.Kind == KindJobStart && i == 0 && running == nil && lastEnd == nil:
				running = &e
			case e.Kind == KindListening && i == 0 && running == nil:
				running = &Event{} // idle: stop looking for a running job
			}
		}
		if lastEnd != nil {
			break
		}
	}
	if lastEnd != nil {
		send(ctx, out, *lastEnd)
	}
	if running != nil && running.Kind == KindJobStart {
		send(ctx, out, *running)
	}
	return path, off
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

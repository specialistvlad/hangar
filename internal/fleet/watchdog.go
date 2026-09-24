package fleet

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// expiredMarker is written into the job's state directory when the limit
// passes. The job-completed hook reads it back, with the limit it held, to
// tell the job why it failed — the limit in .env may have changed since.
const expiredMarker = "expired"

// Watchdog timings. watchPoll is how often it looks; it also bounds how late
// after the deadline the step is stopped. stepGrace is how long a stopped
// step has between SIGTERM and SIGKILL — the runner gives a canceled step
// about ten seconds too. cleanupWindow is how long the rest of the job — the
// post steps, the container clean-up, the completed hook — may take once the
// step is stopped. A job still running after it has something hung in its
// clean-up too, and then Runner.Worker itself is signaled: the runner fails
// the job at once, skipping whatever is left.
const (
	watchPoll     = 5 * time.Second
	stepGrace     = 10 * time.Second
	cleanupWindow = 5 * time.Minute
)

// watchdog is one job's time limit. The procs and kill fields are the seams
// its tests replace; everything else is fixed when the job starts.
type watchdog struct {
	worker   int       // the job's Runner.Worker
	deadline time.Time // when the job's time is up
	limit    int       // minutes, for the expired marker
	state    string    // the job's state directory
	log      io.Writer

	now   func() time.Time
	sleep func(time.Duration)
	procs func() (procTable, error)
	kill  func(pid int, sig syscall.Signal) error
}

// Watch runs the watchdog for worker n's job until that job's Runner.Worker
// exits. `hangar job-watch` is this; the job-started hook starts it.
func (f *Fleet) Watch(n, worker int, deadline time.Time, limit int, log io.Writer) error {
	w := &watchdog{
		worker: worker, deadline: deadline, limit: limit, state: f.jobStateDir(n), log: log,
		now: time.Now, sleep: time.Sleep, procs: readProcTable, kill: syscall.Kill,
	}
	return w.run()
}

// run waits for the job to end or its deadline to pass. It stops the running
// step once, at the deadline, then leaves the job to finish, stepping in again
// only if the job outlives cleanupWindow as well.
func (w *watchdog) run() error {
	var stoppedAt time.Time
	for {
		table, err := w.procs()
		if err != nil {
			w.logf("reading processes: %v", err)
			w.sleep(watchPoll)
			continue
		}
		if !table.is(w.worker, runnerWorkerName) {
			return nil // the job is over
		}
		now := w.now()
		switch {
		case stoppedAt.IsZero() && !now.Before(w.deadline):
			if err := os.WriteFile(filepath.Join(w.state, expiredMarker), []byte(strconv.Itoa(w.limit)+"\n"), 0o600); err != nil {
				w.logf("writing the expired marker: %v", err)
			}
			w.logf("the %d-minute limit passed; stopping the running step", w.limit)
			w.stopTree(table)
			stoppedAt = now
		case !stoppedAt.IsZero() && now.Sub(stoppedAt) >= cleanupWindow:
			w.logf("the job is still running %s after its step was stopped; signaling %s %d",
				cleanupWindow, runnerWorkerName, w.worker)
			_ = w.kill(w.worker, syscall.SIGTERM)
			return nil
		}
		w.sleep(watchPoll)
	}
}

// stopTree stops every process under the job's Runner.Worker: SIGTERM, then
// SIGKILL for whichever of them is still there after stepGrace. The second
// signal goes only to that first set: the moment the step dies the runner
// starts the post steps, which must be left to run. A survivor is known by
// its name and start time rather than by its place in the tree — one whose
// parent died has been reparented away from the worker by then, and is still
// the job's — and the start time is also what keeps a pid reused in the
// meantime from being mistaken for it.
func (w *watchdog) stopTree(table procTable) {
	taken := w.now()
	pids := table.descendants(w.worker)
	for _, pid := range pids {
		w.logf("SIGTERM %d (%s)", pid, table[pid].Name)
		_ = w.kill(pid, syscall.SIGTERM)
	}
	survivors := func() ([]int, bool) {
		now, err := w.procs()
		if err != nil {
			w.logf("reading processes: %v", err)
			return nil, false
		}
		at := w.now()
		var left []int
		for _, pid := range pids {
			if sameProcess(table[pid], taken, now[pid], at) {
				left = append(left, pid)
			}
		}
		return left, true
	}
	for waited := time.Duration(0); waited < stepGrace; waited += time.Second {
		if left, ok := survivors(); ok && len(left) == 0 {
			return
		}
		w.sleep(time.Second)
	}
	// Without a fresh listing there is no telling a survivor from a reused
	// pid, so nothing gets SIGKILL; cleanupWindow still bounds the job.
	left, _ := survivors()
	for _, pid := range left {
		w.logf("SIGKILL %d (%s)", pid, table[pid].Name)
		_ = w.kill(pid, syscall.SIGKILL)
	}
}

// sameProcess reports whether b, listed at bAt, is the process a was, listed
// at aAt: same pid and name, started at the same moment. ps reports elapsed
// time in whole seconds, so the start times may differ by one either way.
func sameProcess(a proc, aAt time.Time, b proc, bAt time.Time) bool {
	if a.PID == 0 || a.PID != b.PID || a.Name != b.Name {
		return false
	}
	gap := aAt.Add(-a.Elapsed).Sub(bAt.Add(-b.Elapsed))
	return gap >= -2*time.Second && gap <= 2*time.Second
}

func (w *watchdog) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.log, "%s %s\n", w.now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

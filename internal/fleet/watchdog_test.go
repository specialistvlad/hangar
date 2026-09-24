package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeJob drives a watchdog against a scripted process table and clock.
type fakeJob struct {
	t       *testing.T
	now     time.Time
	table   procTable
	signals map[int][]syscall.Signal
	// onSignal lets a test decide how the job reacts to a signal.
	onSignal func(pid int, sig syscall.Signal)
	// births records when each pid appeared; the ones jobTree starts with
	// began an hour before the test's clock.
	births map[int]time.Time
}

// born is when pid started, as ps would report it through its elapsed time.
func (j *fakeJob) born(pid int) time.Time {
	if b, ok := j.births[pid]; ok {
		return b
	}
	return time.Unix(1_000_000, 0).Add(-time.Hour)
}

// spawn adds a process to the table, started now.
func (j *fakeJob) spawn(p proc) {
	j.table[p.PID] = p
	j.births[p.PID] = j.now
}

func newFakeJob(t *testing.T) *fakeJob {
	return &fakeJob{t: t, now: time.Unix(1_000_000, 0), table: jobTree(), signals: map[int][]syscall.Signal{}, births: map[int]time.Time{}}
}

func (j *fakeJob) watchdog(deadline time.Time) *watchdog {
	state := j.t.TempDir()
	return &watchdog{
		worker: 300, deadline: deadline, limit: 30, state: state, log: &strings.Builder{},
		now:   func() time.Time { return j.now },
		sleep: func(d time.Duration) { j.now = j.now.Add(d) },
		procs: func() (procTable, error) {
			cp := procTable{}
			for k, v := range j.table {
				v.Elapsed = j.now.Sub(j.born(k))
				cp[k] = v
			}
			return cp, nil
		},
		kill: func(pid int, sig syscall.Signal) error {
			j.signals[pid] = append(j.signals[pid], sig)
			if j.onSignal != nil {
				j.onSignal(pid, sig)
			}
			return nil
		},
	}
}

// A job that finishes in time is never touched, and leaves no marker.
func TestWatchdogLeavesAJobThatFinishesInTime(t *testing.T) {
	j := newFakeJob(t)
	w := j.watchdog(j.now.Add(30 * time.Minute))
	calls := 0
	w.procs = func() (procTable, error) {
		calls++
		if calls > 3 {
			delete(j.table, 300)
		}
		return j.table, nil
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if len(j.signals) != 0 {
		t.Errorf("signals = %v, want none", j.signals)
	}
	if _, err := os.Stat(filepath.Join(w.state, expiredMarker)); err == nil {
		t.Error("a job that finished in time must not be marked expired")
	}
}

// At the deadline the step's whole tree gets SIGTERM and the job is marked;
// the Runner.Worker and its listener do not, so the runner can go on to the
// post steps and the completed hook. A process that ignores SIGTERM gets
// SIGKILL after the grace; a post step started meanwhile gets nothing.
func TestWatchdogStopsTheStepAtTheDeadline(t *testing.T) {
	j := newFakeJob(t)
	deadline := j.now.Add(time.Minute)
	j.onSignal = func(pid int, sig syscall.Signal) {
		switch {
		case pid == 400 && sig == syscall.SIGTERM:
			delete(j.table, 400)
			// The runner sees the step end and starts a post step; its
			// orphaned child is reparented to pid 1 and ignores SIGTERM.
			j.spawn(proc{PID: 500, PPID: 300, Name: "node"})
			j.table[420] = proc{PID: 420, PPID: 1, Name: "stubborn"}
		case pid == 410 && sig == syscall.SIGTERM:
			delete(j.table, 410)
		case pid == 411 && sig == syscall.SIGTERM:
			// Exits, and its pid is reused at once by something else.
			delete(j.table, 411)
			j.spawn(proc{PID: 411, PPID: 1, Name: "docker-buildx"})
		case sig == syscall.SIGKILL:
			delete(j.table, pid)
		case pid == 300:
			delete(j.table, 300)
		}
	}
	w := j.watchdog(deadline)
	// End the job once the post step has run for a while.
	procs := w.procs
	w.procs = func() (procTable, error) {
		if j.now.Sub(deadline) > 2*time.Minute {
			delete(j.table, 300)
		}
		return procs()
	}
	j.table[420] = proc{PID: 420, PPID: 400, Name: "stubborn"} // ignores SIGTERM
	if err := w.run(); err != nil {
		t.Fatal(err)
	}

	for _, pid := range []int{400, 410, 411} {
		if !reflect.DeepEqual(j.signals[pid], []syscall.Signal{syscall.SIGTERM}) {
			t.Errorf("pid %d got %v, want one SIGTERM", pid, j.signals[pid])
		}
	}
	if !reflect.DeepEqual(j.signals[420], []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Errorf("a process ignoring SIGTERM got %v, want SIGTERM then SIGKILL", j.signals[420])
	}
	for _, pid := range []int{100, 200, 300, 500, 900} {
		if len(j.signals[pid]) != 0 {
			t.Errorf("pid %d (%s) got %v, want nothing", pid, jobTree()[pid].Name, j.signals[pid])
		}
	}
	body, err := os.ReadFile(filepath.Join(w.state, expiredMarker))
	if err != nil || strings.TrimSpace(string(body)) != "30" {
		t.Errorf("expired marker = %q, %v; want the limit, 30", body, err)
	}
}

// A job whose clean-up hangs too is ended through the runner itself.
func TestWatchdogSignalsTheWorkerWhenCleanupHangs(t *testing.T) {
	j := newFakeJob(t)
	j.onSignal = func(pid int, sig syscall.Signal) {
		if pid != 300 {
			delete(j.table, pid)
		}
	}
	w := j.watchdog(j.now)
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.signals[300], []syscall.Signal{syscall.SIGTERM}) {
		t.Errorf("Runner.Worker got %v, want one SIGTERM once the clean-up window passed", j.signals[300])
	}
	var stopped []int
	for pid := range j.signals {
		stopped = append(stopped, pid)
	}
	sort.Ints(stopped)
	if want := []int{300, 400, 410, 411}; !reflect.DeepEqual(stopped, want) {
		t.Errorf("signaled %v, want %v", stopped, want)
	}
}

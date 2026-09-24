package fleet

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// ps prints elapsed time as [[dd-]hh:]mm:ss on both platforms; the watchdog's
// deadline counts from it, so every form must read back exactly.
func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:07":       7 * time.Second,
		"12:34":       12*time.Minute + 34*time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-03:04:05":  2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second,
		"10-00:00:00": 10 * 24 * time.Hour,
	} {
		got, ok := parseEtime(in)
		if !ok || got != want {
			t.Errorf("parseEtime(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "7", "a:b", "1:2:3:4", "x-01:02", "-1:00"} {
		if _, ok := parseEtime(bad); ok {
			t.Errorf("parseEtime(%q) accepted", bad)
		}
	}
}

// Linux prints a bare name, macOS the executable's path — spaces and all.
// Both must come out as the same name, or a Mac watchdog would never find
// its Runner.Worker.
func TestParseProcTable(t *testing.T) {
	out := `    1     0 10-01:00:00 /sbin/launchd
  400     1    01:00:00 /Users/me/My Runners/w1/bin/Runner.Listener
  500   400       05:00 /Users/me/My Runners/w1/bin/Runner.Worker
  600   500       04:59 bash
garbage line
  700   600 bad         sleep
`
	got := parseProcTable(out)
	want := procTable{
		1:   {PID: 1, PPID: 0, Elapsed: 10*24*time.Hour + time.Hour, Name: "launchd"},
		400: {PID: 400, PPID: 1, Elapsed: time.Hour, Name: "Runner.Listener"},
		500: {PID: 500, PPID: 400, Elapsed: 5 * time.Minute, Name: "Runner.Worker"},
		600: {PID: 600, PPID: 500, Elapsed: 4*time.Minute + 59*time.Second, Name: "bash"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseProcTable:\n got %#v\nwant %#v", got, want)
	}
}

// jobTree is a runner service mid-job: runsvc → listener → worker → a step
// (bash running docker), plus an unrelated process and a pid-1 loop guard.
func jobTree() procTable {
	return procTable{
		1:   {PID: 1, PPID: 1, Name: "init"},
		100: {PID: 100, PPID: 1, Name: "bash"},
		200: {PID: 200, PPID: 100, Name: "Runner.Listener"},
		300: {PID: 300, PPID: 200, Name: runnerWorkerName},
		400: {PID: 400, PPID: 300, Name: "bash"},
		410: {PID: 410, PPID: 400, Name: "docker"},
		411: {PID: 411, PPID: 410, Name: "docker-buildx"},
		900: {PID: 900, PPID: 1, Name: "sshd"},
	}
}

func TestProcTableWalks(t *testing.T) {
	tb := jobTree()
	if pid, ok := tb.ancestor(411, runnerWorkerName); !ok || pid != 300 {
		t.Errorf("ancestor(411) = %d, %v; want the Runner.Worker, 300", pid, ok)
	}
	if _, ok := tb.ancestor(900, runnerWorkerName); ok {
		t.Error("a process outside the job has no Runner.Worker above it")
	}
	got := tb.descendants(300)
	sort.Ints(got)
	if want := []int{400, 410, 411}; !reflect.DeepEqual(got, want) {
		t.Errorf("descendants(300) = %v, want %v — the step's whole tree, the worker itself excluded", got, want)
	}
	if !tb.runsJob(100) {
		t.Error("a service with a Runner.Worker below it is running a job")
	}
	delete(tb, 300)
	if tb.runsJob(100) {
		t.Error("a service with no Runner.Worker below it is idle")
	}
}

// A pid alone is not a process: once a job's worker exits its number can be
// reused, and the watchdog must not take the newcomer for the job.
func TestProcTableIsChecksTheName(t *testing.T) {
	tb := jobTree()
	if !tb.is(300, runnerWorkerName) {
		t.Error("pid 300 is the Runner.Worker")
	}
	if tb.is(400, runnerWorkerName) || tb.is(12345, runnerWorkerName) {
		t.Error("a different process, or none, under that pid is not the Runner.Worker")
	}
}

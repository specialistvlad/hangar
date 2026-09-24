package fleet

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runnerWorkerName is the process the runner starts for each job. The
// listener stays up between jobs; a Runner.Worker exists only while a job
// runs, and every step of that job is a descendant of it.
const runnerWorkerName = "Runner.Worker"

// psTimeout bounds one process listing.
const psTimeout = 10 * time.Second

// proc is one row of the process table.
type proc struct {
	PID, PPID int
	Elapsed   time.Duration // how long ago it started
	Name      string        // executable name, without its directory
}

// procTable is a snapshot of the machine's processes, keyed by pid.
type procTable map[int]proc

// readProcTable lists every process through ps, which both platforms have and
// which reads the same with the same flags on each: one -o per column, so no
// header, and the name last because a macOS name can hold spaces. /proc would
// serve Linux alone, and the job watchdog must behave identically on a Mac.
func readProcTable() (procTable, error) {
	out, err := runTimeout(psTimeout, "", "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "etime=", "-o", "comm=")
	if err != nil {
		return nil, fmt.Errorf("ps: %v: %s", err, strings.TrimSpace(out))
	}
	return parseProcTable(out), nil
}

// parseProcTable reads readProcTable's output. Linux prints the bare name;
// macOS prints the executable's path, so the name is taken after the last /.
func parseProcTable(out string) procTable {
	t := procTable{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		elapsed, ok := parseEtime(f[2])
		if err1 != nil || err2 != nil || !ok {
			continue
		}
		name := strings.Join(f[3:], " ")
		t[pid] = proc{PID: pid, PPID: ppid, Elapsed: elapsed, Name: filepath.Base(name)}
	}
	return t
}

// parseEtime reads ps's elapsed time, [[dd-]hh:]mm:ss, the one format both
// platforms print. Linux's etimes would be simpler, but macOS has no such key.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second, true
}

// is reports whether pid is running and is the named executable. Checking the
// name as well as the pid is what keeps a stale pid — a job's worker that has
// exited, its number since reused — from being mistaken for the process it
// once was.
func (t procTable) is(pid int, name string) bool {
	p, ok := t[pid]
	return ok && p.Name == name
}

// ancestor walks up from pid to the nearest process with the given name.
func (t procTable) ancestor(pid int, name string) (int, bool) {
	seen := map[int]bool{}
	for p, ok := t[pid]; ok && !seen[p.PID]; p, ok = t[p.PPID] {
		if p.Name == name {
			return p.PID, true
		}
		seen[p.PID] = true
	}
	return 0, false
}

// descendants lists every process below root, root itself excluded.
func (t procTable) descendants(root int) []int {
	children := map[int][]int{}
	for _, p := range t {
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	var out []int
	queue := append([]int(nil), children[root]...)
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	return out
}

// runsJob reports whether the service whose main process is pid is running a
// job right now: a Runner.Worker exists below it only while one does.
func (t procTable) runsJob(pid int) bool {
	for _, d := range t.descendants(pid) {
		if t[d].Name == runnerWorkerName {
			return true
		}
	}
	return false
}

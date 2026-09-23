//go:build darwin

package metrics

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dockerState tracks Docker Desktop's VM process between samples.
type dockerState struct {
	pid     int
	lastCPU time.Duration
	lastAt  time.Time
}

// dockerUsage derives an instantaneous CPU percentage by differencing the VM
// process's cumulative CPU time. ps reports a since-launch average, which for a
// long-lived VM would flatline and hide exactly the spikes worth watching.
func (s *Sampler) dockerUsage(now time.Time) (cpu float64, mem uint64, found bool) {
	st := &s.docker
	if st.pid == 0 {
		st.pid = findVMPID()
	}
	if st.pid == 0 {
		return 0, 0, false
	}

	out, err := output("ps", "-o", "time=,rss=", "-p", strconv.Itoa(st.pid))
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		st.pid = 0 // VM went away; rediscover on the next tick
		return 0, 0, false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, 0, false
	}

	total := parseCPUTime(fields[0])
	rssKB, _ := strconv.ParseUint(fields[1], 10, 64)

	if !st.lastAt.IsZero() && total >= st.lastCPU {
		if wall := now.Sub(st.lastAt); wall > 0 {
			cpu = float64(total-st.lastCPU) / float64(wall) * 100
		}
	}
	st.lastCPU, st.lastAt = total, now
	return cpu, rssKB * 1024, true
}

// ponytail: largest Virtualization.framework VM is assumed to be Docker's.
// Walk the parent chain to com.docker.backend if a second VM ever shows up.
func findVMPID() int {
	out, err := output("ps", "-A", "-o", "pid=,rss=,comm=")
	if err != nil {
		return 0
	}
	best, bestRSS := 0, uint64(0)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "Virtualization.VirtualMachine") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		rss, _ := strconv.ParseUint(f[1], 10, 64)
		if rss > bestRSS {
			best, bestRSS = pid, rss
		}
	}
	return best
}

// parseCPUTime handles ps's [DD-]HH:MM:SS and MM:SS.ss shapes.
func parseCPUTime(s string) time.Duration {
	var days float64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.ParseFloat(d, 64)
		s = rest
	}
	parts := strings.Split(s, ":")
	var secs float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		secs = secs*60 + v
	}
	secs += days * 86400
	return time.Duration(secs * float64(time.Second))
}

func loadAvg() float64 {
	out, err := output("sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{} "))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

var reVMStat = regexp.MustCompile(`^Pages (.+?):\s+(\d+)\.`)

// memUsed mirrors Activity Monitor's "used": active + wired + compressed.
// Free and inactive pages are reclaimable and counting them as used would make
// every Mac look permanently out of memory.
func memUsed() uint64 {
	out, err := output("vm_stat")
	if err != nil {
		return 0
	}
	pageSize := uint64(16384)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindSubmatch(out); m != nil {
		if v, err := strconv.ParseUint(string(m[1]), 10, 64); err == nil {
			pageSize = v
		}
	}
	var pages uint64
	for _, line := range strings.Split(string(out), "\n") {
		m := reVMStat.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, _ := strconv.ParseUint(m[2], 10, 64)
		switch m[1] {
		case "active", "wired down", "occupied by compressor":
			pages += v
		}
	}
	return pages * pageSize
}

func memTotal() uint64 { return sysctlUint("hw.memsize") }

// dockerUnavailable is always "": Docker Desktop's VM can be found whenever it
// is running.
func dockerUnavailable() string { return "" }

// platformDisks watches the home volume on macOS: Docker Desktop keeps its
// whole disk image there, so that is where builds run out of space.
func platformDisks(string) ([]Disk, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, true
	}
	return []Disk{{Label: "home", Path: home}}, true
}

func sysctlUint(key string) uint64 {
	out, err := output("sysctl", "-n", key)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return v
}

// sampleTimeout bounds every shell-out. The dashboard samples once a second, so
// a wedged ps must degrade to a missing reading rather than a frozen UI.
const sampleTimeout = 5 * time.Second

// output runs a command with a deadline and returns its stdout.
func output(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sampleTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

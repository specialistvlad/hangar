//go:build linux

package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sampling needs no shell-outs: everything is read from /proc and the cgroup
// filesystem, so a sample cannot hang on a wedged process. The one subprocess
// is asking docker for its data root, which happens off the sampling path's
// hot loop and is bounded by a timeout.

// cgroupRoot is where the unified (v2) hierarchy is mounted. A var so tests can
// point it at a fake tree.
var cgroupRoot = "/sys/fs/cgroup"

// dockerState remembers each cgroup's CPU counter from the previous sample.
type dockerState struct {
	usage  map[string]uint64 // cgroup dir -> cumulative usage_usec
	lastAt time.Time
}

// dockerCgroupPatterns are where docker's work runs on a systemd host, relative
// to the cgroup root. The daemon and containerd are services; each container
// is a docker-<id>.scope; each BuildKit build step gets a transient cgroup
// named system.slice:docker:<id> for as long as the step runs. The last entry
// covers the cgroupfs driver, which nests everything under one parent whose
// counters already include its children.
var dockerCgroupPatterns = []string{
	"system.slice/docker.service",
	"system.slice/snap.docker.dockerd.service",
	"system.slice/containerd.service",
	"system.slice/docker-*.scope",
	"system.slice/system.slice:docker:*",
	"docker",
}

// dockerCgroups lists the cgroups docker's work currently runs in, and whether
// a docker daemon is there at all — containerd alone does not count. Under the
// systemd driver the daemon is docker.service, or snap.docker.dockerd.service
// for Ubuntu's snap; a container scope or a build step's cgroup exists only
// while a daemon has work, so either proves one too. Under the cgroupfs driver
// its work sits under a bare docker cgroup that outlives the daemon, so that
// one counts only while something is still running in it.
func dockerCgroups(root string) (dirs []string, found bool) {
	for _, pat := range dockerCgroupPatterns {
		matches, _ := filepath.Glob(filepath.Join(root, pat))
		for _, m := range matches {
			if fi, err := os.Stat(m); err != nil || !fi.IsDir() {
				continue
			}
			dirs = append(dirs, m)
			switch pat {
			case "system.slice/containerd.service":
			case "docker":
				found = found || populated(m)
			default:
				found = true
			}
		}
	}
	return dirs, found
}

// populated reads a cgroup's cgroup.events: "populated 1" while any process
// runs in it or below it.
func populated(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if err != nil {
		return false
	}
	v, ok := statField(string(b), "populated")
	return ok && v == 1
}

// dockerUnavailable says why docker cannot be sampled on this host, or "" when
// it can. The sampling reads the unified (v2) hierarchy; a host still on v1 or
// the hybrid layout has none of those files, and would otherwise read as
// "docker not running" while builds hold every core.
func dockerUnavailable() string {
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return "needs cgroup v2"
	}
	return ""
}

// dockerUsage sums CPU and memory across docker's cgroups. CPU is the rate
// since the previous sample, as a percentage where 100 is one core.
func (s *Sampler) dockerUsage(now time.Time) (cpu float64, mem uint64, found bool) {
	dirs, found := dockerCgroups(cgroupRoot)
	if !found {
		s.docker = dockerState{}
		return 0, 0, false
	}
	prev := s.docker.usage
	usage := make(map[string]uint64, len(dirs))
	for _, d := range dirs {
		u, m, ok := readCgroup(d)
		if !ok {
			// A read racing a cgroup's removal. Carry the old counter so a
			// long-lived cgroup that misses one read is not later counted as
			// new — which would book its whole lifetime into a single second.
			if p, seen := prev[d]; seen {
				usage[d] = p
			}
			continue
		}
		usage[d] = u
		mem += m
	}
	if !s.docker.lastAt.IsZero() {
		if wall := now.Sub(s.docker.lastAt).Microseconds(); wall > 0 {
			cpu = float64(cpuDelta(prev, usage)) / float64(wall) * 100
		}
	}
	s.docker = dockerState{usage: usage, lastAt: now}
	return cpu, mem, true
}

// cpuDelta is the CPU time, in microseconds, spent between two samples. Build
// steps come and go between samples: a cgroup that is new has spent all of its
// time inside the window, and one whose counter went backwards was removed and
// recreated under the same name, so it counts from zero. A cgroup that vanished
// takes its last fraction of a second with it, which undercounts slightly
// rather than never.
func cpuDelta(prev, now map[string]uint64) uint64 {
	var total uint64
	for dir, u := range now {
		p, seen := prev[dir]
		switch {
		case !seen, u < p:
			total += u
		default:
			total += u - p
		}
	}
	return total
}

// readCgroup returns a cgroup's cumulative CPU time and its working-set memory.
// Memory excludes inactive file cache, the same subtraction `docker stats`
// makes: image layers sitting in page cache are reclaimable and not a build's
// footprint.
func readCgroup(dir string) (usageUsec, memBytes uint64, ok bool) {
	stat, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0, 0, false
	}
	usageUsec, ok = statField(string(stat), "usage_usec")
	if !ok {
		return 0, 0, false
	}
	if cur, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
		memBytes, _ = strconv.ParseUint(strings.TrimSpace(string(cur)), 10, 64)
		if ms, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
			if inactive, found := statField(string(ms), "inactive_file"); found && inactive < memBytes {
				memBytes -= inactive
			}
		}
	}
	return usageUsec, memBytes, true
}

// statField reads one "key value" line from a cgroup stat file.
func statField(body, key string) (uint64, bool) {
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(line, " ")
		if ok && k == key {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

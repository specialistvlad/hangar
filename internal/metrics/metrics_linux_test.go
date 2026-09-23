//go:build linux

package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	body := "MemTotal:       65536000 kB\nMemFree:         1000 kB\nMemAvailable:   60000000 kB\n"
	total, avail := parseMeminfo(body)
	if total != 65536000*1024 || avail != 60000000*1024 {
		t.Errorf("parseMeminfo = %d, %d", total, avail)
	}
}

func TestParseLoadavg(t *testing.T) {
	if got := parseLoadavg("3.25 2.10 1.00 4/812 12345\n"); got != 3.25 {
		t.Errorf("parseLoadavg = %v, want 3.25", got)
	}
	if got := parseLoadavg(""); got != 0 {
		t.Errorf("empty loadavg = %v, want 0", got)
	}
}

// Build steps get a cgroup each that lives only as long as the step. A new one
// has spent its whole life inside the window; one whose counter went backwards
// was recreated under the same name; one that vanished contributes nothing
// rather than a negative number that would wrap around.
func TestCPUDelta(t *testing.T) {
	prev := map[string]uint64{"daemon": 1000, "step-old": 500, "gone": 9000}
	now := map[string]uint64{"daemon": 1600, "step-old": 100, "step-new": 250}
	if got := cpuDelta(prev, now); got != 600+100+250 {
		t.Errorf("cpuDelta = %d, want 950", got)
	}
}

func writeCgroup(t *testing.T, dir, cpuStat, memCurrent, memStat string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"cpu.stat": cpuStat, "memory.current": memCurrent, "memory.stat": memStat} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The docker row sums the daemon, containerd, containers and build steps, and
// reports memory net of reclaimable file cache the way `docker stats` does.
func TestDockerUsageSumsDockersCgroups(t *testing.T) {
	root := t.TempDir()
	prev := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = prev })

	s := &Sampler{}
	if _, _, found := s.dockerUsage(time.Unix(0, 0)); found {
		t.Fatal("no docker.service cgroup: docker must read as not running")
	}

	sys := filepath.Join(root, "system.slice")
	writeCgroup(t, filepath.Join(sys, "docker.service"), "usage_usec 1000000\n", "300", "inactive_file 100\n")
	writeCgroup(t, filepath.Join(sys, "containerd.service"), "usage_usec 0\n", "50", "")
	writeCgroup(t, filepath.Join(sys, "sshd.service"), "usage_usec 99999999\n", "999999", "")

	t0 := time.Unix(100, 0)
	cpu, mem, found := s.dockerUsage(t0)
	if !found || cpu != 0 {
		t.Fatalf("first sample: found=%v cpu=%v, want found and no rate yet", found, cpu)
	}
	if mem != 200+50 {
		t.Errorf("mem = %d, want 250 (sshd excluded, inactive file cache subtracted)", mem)
	}

	// One second later: the daemon used half a core, and a build step that
	// appeared inside the window used a full one.
	writeCgroup(t, filepath.Join(sys, "docker.service"), "usage_usec 1500000\n", "300", "")
	writeCgroup(t, filepath.Join(sys, "system.slice:docker:abc123"), "usage_usec 1000000\n", "10", "")
	cpu, _, _ = s.dockerUsage(t0.Add(time.Second))
	if cpu != 150 {
		t.Errorf("cpu = %v, want 150 (%% of one core)", cpu)
	}
}

// Containerd runs on hosts with no docker at all; on its own it is not docker.
func TestDockerCgroupsNeedTheDaemon(t *testing.T) {
	root := t.TempDir()
	writeCgroup(t, filepath.Join(root, "system.slice", "containerd.service"), "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); found {
		t.Error("containerd alone should not count as a docker daemon")
	}
}

// Under the cgroupfs driver the bare docker cgroup outlives the daemon, so it
// proves a daemon only while something still runs in it.
func TestCgroupfsDockerCountsOnlyWhilePopulated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docker")
	writeCgroup(t, dir, "usage_usec 5\n", "1", "")
	if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte("populated 0\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found := dockerCgroups(root); found {
		t.Error("an empty leftover docker cgroup must read as no daemon")
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found := dockerCgroups(root); !found {
		t.Error("a populated docker cgroup is a running daemon")
	}
}

// A host on cgroup v1 or the hybrid layout cannot be sampled, and must say so
// rather than report docker as stopped.
func TestDockerUnavailableWithoutCgroupV2(t *testing.T) {
	root := t.TempDir()
	prev := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = prev })

	if got := dockerUnavailable(); got == "" {
		t.Error("no cgroup.controllers: docker must be reported unavailable")
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dockerUnavailable(); got != "" {
		t.Errorf("unified hierarchy present, got %q", got)
	}
}

// Ubuntu's snap runs the daemon as snap.docker.dockerd.service, and a running
// container's scope proves a daemon whatever its unit is called.
func TestDockerCgroupsFindSnapAndScopes(t *testing.T) {
	root := t.TempDir()
	writeCgroup(t, filepath.Join(root, "system.slice", "snap.docker.dockerd.service"), "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); !found {
		t.Error("the snap daemon unit should count as docker")
	}
	root = t.TempDir()
	writeCgroup(t, filepath.Join(root, "system.slice", "docker-abc.scope"), "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); !found {
		t.Error("a container scope proves a running daemon")
	}
}

// Rootless docker runs its daemon under the invoking user's own systemd
// instance, one level below system.slice, so it needs its own glob to be
// found at all.
func TestDockerCgroupsFindRootless(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "user.slice", "user-1000.slice", "user@1000.service", "app.slice", "docker.service")
	writeCgroup(t, dir, "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); !found {
		t.Error("rootless docker.service should count as a running daemon")
	}
}

// A rootless container's scope proves a daemon the same as a rootful one's.
func TestDockerCgroupsFindRootlessScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "user.slice", "user-1000.slice", "user@1000.service", "app.slice", "docker-abc.scope")
	writeCgroup(t, dir, "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); !found {
		t.Error("a rootless container scope should count as a running daemon")
	}
}

// Rootless containerd alone, same as the system instance, is not docker.
func TestDockerCgroupsRootlessContainerdAlone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "user.slice", "user-1000.slice", "user@1000.service", "app.slice", "containerd.service")
	writeCgroup(t, dir, "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); found {
		t.Error("rootless containerd alone should not count as a docker daemon")
	}
}

// A host with neither the rootful nor the rootless layout — docker not
// installed, or Docker Desktop for Linux running its daemon in a VM — reads
// as no daemon running rather than as "cannot tell".
func TestDockerCgroupsNoKnownLayout(t *testing.T) {
	root := t.TempDir()
	writeCgroup(t, filepath.Join(root, "system.slice", "sshd.service"), "usage_usec 1\n", "1", "")
	if _, found := dockerCgroups(root); found {
		t.Error("a host with no known docker cgroup layout should read as not running")
	}
}

func TestParseDockerInfo(t *testing.T) {
	root, snap := parseDockerInfo(`/lex/docker|[["driver-type","io.containerd.snapshotter.v1"]]` + "\n")
	if root != "/lex/docker" || !snap {
		t.Errorf("containerd store: got %q, %v", root, snap)
	}
	root, snap = parseDockerInfo(`/var/lib/docker|[["Backing Filesystem","extfs"],["Supports d_type","true"]]`)
	if root != "/var/lib/docker" || snap {
		t.Errorf("classic overlay2: got %q, %v", root, snap)
	}
}

func TestContainerdRoot(t *testing.T) {
	for toml, want := range map[string]string{
		"version = 3\nroot = '/data/containerd'\nstate = '/run/containerd'\n": "/data/containerd",
		"version = 2\nroot = \"/srv/ctr\"\n":                                  "/srv/ctr",
		"version = 3\n[plugins]\n  root = '/not/this'\n":                      "/var/lib/containerd",
		"root = \"/data/containerd\"  # moved off /\n":                        "/data/containerd",
		"root = '/data/ctr' # comment\n":                                      "/data/ctr",
		"root = /bare/path # comment\n":                                       "/bare/path",
		"":                                                                    "/var/lib/containerd",
	} {
		if got := containerdRoot(toml); got != want {
			t.Errorf("containerdRoot(%q) = %q, want %q", toml, got, want)
		}
	}
}

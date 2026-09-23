// Package metrics samples host and docker resource usage.
//
// The numbers that matter for a docker-based build fleet are not the runner
// processes — those sit near idle while BuildKit does the work inside Docker.
// So docker is sampled as a first-class row alongside the host: on macOS that
// is Docker Desktop's virtual machine, on Linux the cgroups the daemon, its
// containers and its build steps run in.
package metrics

import (
	"runtime"
	"time"
)

type Snapshot struct {
	NCPU      int
	Load1     float64
	MemUsed   uint64
	MemTotal  uint64
	DiskFree  uint64
	DiskTotal uint64
	DiskLabel string  // which watched filesystem is tightest, e.g. "docker"
	DiskLevel int     // DiskOK, DiskLow or DiskCritical, judged against that disk's size
	DockerCPU float64 // percent, 100 == one core saturated
	DockerMem uint64
	// DockerFound is false when no docker daemon is there to sample, and
	// DockerUnavailable, when set, says why this host cannot be sampled at all —
	// the difference between "docker is off" and "hangar cannot tell".
	DockerFound       bool
	DockerUnavailable string
}

// Disk is a filesystem worth watching for free space, named for the dashboard.
type Disk struct {
	Label string
	Path  string
}

// Options says where the fleet lives. The sampler adds the platform's own
// places on top: the home volume on macOS, docker's storage on Linux.
type Options struct {
	Disks      []Disk
	DockerHost string // DOCKER_HOST as configured, used to ask docker where it stores data
}

// Sampler holds the state needed to turn cumulative counters into rates.
type Sampler struct {
	docker   dockerState // platform-specific: see metrics_darwin.go / metrics_linux.go
	memTotal uint64

	opts       Options
	disks      []Disk
	disksAt    time.Time
	disksFinal bool

	Load   *Ring
	Mem    *Ring
	Docker *Ring
}

func NewSampler(o Options) *Sampler {
	return &Sampler{
		memTotal: memTotal(),
		opts:     o,
		Load:     NewRing(60),
		Mem:      NewRing(60),
		Docker:   NewRing(60),
	}
}

// diskRetry is how often the platform's disks are looked up again while that
// lookup is incomplete — docker not answering when the dashboard opened.
const diskRetry = 30 * time.Second

// watchedDisks is resolved on the sampling goroutine, never at construction:
// asking docker where it keeps its data is a subprocess, and the dashboard must
// open without waiting on it.
func (s *Sampler) watchedDisks(now time.Time) []Disk {
	if !s.disksFinal && (s.disksAt.IsZero() || now.Sub(s.disksAt) >= diskRetry) {
		extra, final := platformDisks(s.opts.DockerHost)
		s.disks = append(append([]Disk{}, s.opts.Disks...), extra...)
		s.disksFinal, s.disksAt = final, now
	}
	return s.disks
}

// Frame is one sample together with the history behind it. Sampling happens on
// its own goroutine, so the renderer is handed the series rather than reaching
// back into the Sampler's rings while they are being written.
type Frame struct {
	Snapshot
	Load   []float64
	Mem    []float64
	Docker []float64
}

// Frame samples and returns everything a dashboard needs to draw one row set.
func (s *Sampler) Frame() Frame {
	return Frame{
		Snapshot: s.Sample(),
		Load:     s.Load.Values(),
		Mem:      s.Mem.Values(),
		Docker:   s.Docker.Values(),
	}
}

func (s *Sampler) Sample() Snapshot {
	now := time.Now()
	snap := Snapshot{NCPU: runtime.NumCPU(), MemTotal: s.memTotal}
	snap.Load1 = loadAvg()
	snap.MemUsed = memUsed()
	snap.DiskFree, snap.DiskTotal, snap.DiskLabel, snap.DiskLevel = tightest(s.watchedDisks(now))
	if snap.DockerUnavailable = dockerUnavailable(); snap.DockerUnavailable == "" {
		// Timed where the counters are read, not at the top: the disk lookup
		// can take seconds, and a stale timestamp turns into a false CPU spike.
		snap.DockerCPU, snap.DockerMem, snap.DockerFound = s.dockerUsage(time.Now())
	}

	s.Load.Push(snap.Load1)
	if snap.MemTotal > 0 {
		s.Mem.Push(float64(snap.MemUsed) / float64(snap.MemTotal) * 100)
	}
	s.Docker.Push(snap.DockerCPU)
	return snap
}

// Package exporter serves the fleet's state in the Prometheus text format: which
// workers are up, which are busy and on what, and how jobs end and how long
// they take. It reads only what hangar already reads — the supervisor's view of
// the fleet and the runner's own _diag logs — and needs no GitHub access.
package exporter

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/logs"
)

// results are the outcomes jobs are counted under; resultOf maps the runner's
// own results onto them.
var results = []string{"succeeded", "failed", "canceled"}

// durationBuckets span a lint job to a cold image build, in seconds.
var durationBuckets = []float64{30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600, 5400, 7200}

// jobLabelMax caps the job_name label: display names embed matrix values and
// can run to hundreds of characters. The label is job_name, not job: job and
// instance are the labels Prometheus attaches to every scraped series, and an
// exported one is renamed to exported_job on ingestion.
const jobLabelMax = 80

// infoAttempts bounds the lookups of a job's repository and run: the worker
// log appears within seconds of the job starting, so a job whose log never
// does keeps the runner's display name alone.
const infoAttempts = 24

type workerState struct {
	runner  string
	up      bool
	busy    bool
	job     string // display name from the runner log
	start   time.Time
	lastEnd time.Time
	info    logs.JobInfo
	infoOK  bool
	tries   int
	logPath string // the pending job's Worker_*.log, once resolved; skips re-globbing _diag on later polls
	jobs    map[string]uint64 // result -> count since the exporter started
}

// Collector holds the fleet's state between scrapes. Every method is safe to
// call from several goroutines.
type Collector struct {
	mu            sync.Mutex
	workers       map[int]*workerState
	durations     *histogram
	version       string
	commit        string
	now           func() time.Time
	listFailures  uint64    // fleet listings that failed, since the exporter started
	listFailing   bool      // whether the most recent listing failed, to log only on change
	listSuccessAt time.Time // when a listing last succeeded; zero until the first one does
}

func NewCollector(version, commit string) *Collector {
	return &Collector{
		workers:   map[int]*workerState{},
		durations: newHistogram(durationBuckets, results...),
		version:   version,
		commit:    commit,
		now:       time.Now,
	}
}

func (c *Collector) worker(n int) *workerState {
	w := c.workers[n]
	if w == nil {
		w = &workerState{jobs: map[string]uint64{}}
		c.workers[n] = w
	}
	return w
}

// SetFleet applies a fleet listing the supervisor answered. A worker is up
// while its runner is registered and its listener process runs; one that is
// not up runs no job either; a worker no longer on disk drops out, with its
// series.
func (c *Collector) SetFleet(ws []fleet.Worker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[int]bool{}
	for _, fw := range ws {
		seen[fw.Index] = true
		w := c.worker(fw.Index)
		w.runner = fw.Name
		w.up = fw.Registered && fw.Running
		if !w.up {
			w.notUp()
		}
	}
	for n := range c.workers {
		if !seen[n] {
			delete(c.workers, n)
		}
	}
}

// Apply takes one job transition from a worker's runner log. A transition
// replayed on attach restores state but is not counted: it was counted, if at
// all, by the exporter that saw it happen.
func (c *Collector) Apply(e logs.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.worker(e.Worker)
	at := e.At
	if at.IsZero() {
		at = c.now()
	}
	switch e.Kind {
	case logs.KindJobStart:
		w.busy, w.job, w.start = true, e.Text, at
		w.info, w.infoOK, w.tries, w.logPath = logs.JobInfo{}, false, 0, ""
	case logs.KindJobEnd:
		if !e.Recovered {
			r := resultOf(e.Result)
			w.jobs[r]++
			if !w.start.IsZero() && !at.Before(w.start) {
				c.durations.observe(r, at.Sub(w.start).Seconds())
			}
		}
		w.busy, w.job, w.start, w.lastEnd = false, "", time.Time{}, at
		w.info, w.infoOK, w.logPath = logs.JobInfo{}, false, ""
	case logs.KindListening:
		w.idle()
	case logs.KindLine:
	}
}

// idle clears a job without counting it: a listener that restarted, or one
// that is no longer running, has no job, whether or not it logged an end.
func (w *workerState) idle() {
	w.busy, w.job, w.start = false, "", time.Time{}
	w.info, w.infoOK, w.tries, w.logPath = logs.JobInfo{}, false, 0, ""
}

// notUp clears a worker's dashboard-facing busy state when the supervisor
// reports it as not running. The job's start stays in place: a worker can go
// briefly "not up" mid-job (a restart under Restart=always or KeepAlive)
// without losing the timestamp its eventual KindJobEnd needs, so
// hangar_jobs_total and hangar_job_duration_seconds keep counting the same
// set of live job ends. Only that job's own end, a new start, or a
// KindListening transition clears it.
func (w *workerState) notUp() {
	w.busy, w.job = false, ""
	w.info, w.infoOK, w.tries = logs.JobInfo{}, false, 0
}

// PendingJob is a busy worker whose job's repository and run are not known
// yet: its start, for the lookup, and the Worker_*.log a previous lookup
// resolved for it, if any, so the caller can skip resolving it again while
// no fuller message has appeared there.
type PendingJob struct {
	Start   time.Time
	LogPath string
}

// PendingInfo lists the busy workers whose job's repository and run are not
// known yet.
func (c *Collector) PendingInfo() map[int]PendingJob {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[int]PendingJob{}
	for n, w := range c.workers {
		if w.busy && !w.infoOK && w.tries < infoAttempts {
			out[n] = PendingJob{Start: w.start, LogPath: w.logPath}
		}
	}
	return out
}

// SetJobInfo records a lookup for the job that started on worker n at start —
// the result if found, the worker log resolved for it either way so the next
// lookup does not glob _diag again, one more attempt if not found. A job that
// has meanwhile ended or been replaced is left alone.
func (c *Collector) SetJobInfo(n int, start time.Time, info logs.JobInfo, path string, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.workers[n]
	if w == nil || !w.busy || !w.start.Equal(start) {
		return
	}
	w.logPath = path
	if found {
		w.info, w.infoOK = info, true
		return
	}
	w.tries++
}

// resultOf maps the runner's job result onto the three counted outcomes.
func resultOf(r string) string {
	switch strings.ToLower(r) {
	case "succeeded", "succeededwithissues":
		return "succeeded"
	case "canceled", "abandoned":
		return "canceled"
	}
	return "failed"
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

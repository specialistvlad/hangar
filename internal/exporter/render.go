package exporter

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"time"
)

// Render writes every family in the text exposition format. The exposition
// is built into a buffer under c.mu and copied to dst only once the lock is
// released, so a slow or stalled scrape client blocked on dst.Write cannot
// hold c.mu and, through it, stall Apply and SetFleet — which would otherwise
// back up the events channel and freeze every worker's job tracking.
func (c *Collector) Render(dst io.Writer) {
	var buf bytes.Buffer
	out := &buf

	c.mu.Lock()
	idx := make([]int, 0, len(c.workers))
	for n := range c.workers {
		idx = append(idx, n)
	}
	sort.Ints(idx)
	ids := func(n int) []label {
		return []label{{"worker", fmt.Sprintf("w%d", n)}, {"runner", c.workers[n].runner}}
	}
	gauge := func(name, help string, value func(*workerState) (float64, bool)) {
		family(out, name, "gauge", help)
		for _, n := range idx {
			if v, ok := value(c.workers[n]); ok {
				sample(out, name, ids(n), v)
			}
		}
	}
	bit := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	stamp := func(t time.Time) (float64, bool) { return float64(t.Unix()), !t.IsZero() }

	family(out, "hangar_build_info", "gauge", "The hangar build serving these metrics.")
	sample(out, "hangar_build_info", []label{{"version", c.version}, {"commit", c.commit}}, 1)
	family(out, "hangar_workers_configured", "gauge", "Workers hangar manages on this machine.")
	sample(out, "hangar_workers_configured", nil, float64(len(idx)))

	gauge("hangar_worker_up", "1 while the worker's runner is registered and its listener process runs.",
		func(w *workerState) (float64, bool) { return bit(w.up), true })
	gauge("hangar_worker_busy", "1 while the worker is running a job.",
		func(w *workerState) (float64, bool) { return bit(w.busy), true })

	family(out, "hangar_worker_job_info", "gauge", "The job a busy worker is running; present only while busy.")
	for _, n := range idx {
		w := c.workers[n]
		if !w.busy {
			continue
		}
		name := w.job
		if w.infoOK && w.info.Name != "" {
			name = w.info.Name
		}
		sample(out, "hangar_worker_job_info", append(ids(n),
			label{"job_name", truncate(name, jobLabelMax)}, label{"workflow", w.info.Workflow},
			label{"repo", w.info.Repo}, label{"run_id", w.info.RunID}), 1)
	}
	gauge("hangar_worker_job_start_timestamp_seconds", "Unix time the current job started; absent while idle.",
		func(w *workerState) (float64, bool) {
			if !w.busy {
				return 0, false
			}
			return stamp(w.start)
		})
	gauge("hangar_worker_last_job_end_timestamp_seconds", "Unix time the worker's last job ended.",
		func(w *workerState) (float64, bool) { return stamp(w.lastEnd) })

	family(out, "hangar_jobs_total", "counter", "Jobs finished since the exporter started, by result.")
	for _, n := range idx {
		for _, r := range results {
			sample(out, "hangar_jobs_total", append(ids(n), label{"result", r}), float64(c.workers[n].jobs[r]))
		}
	}
	family(out, "hangar_job_duration_seconds", "histogram", "Job duration, start to end, by result.")
	c.durations.write(out, "hangar_job_duration_seconds", "result")

	family(out, "hangar_fleet_list_failures_total", "counter",
		"Fleet listings that failed to reach the supervisor, since the exporter started.")
	sample(out, "hangar_fleet_list_failures_total", nil, float64(c.listFailures))
	family(out, "hangar_fleet_list_success_timestamp_seconds", "gauge",
		"Unix time of the fleet listing that last reached the supervisor; 0 before the exporter's first one succeeds.")
	successAt := 0.0
	if !c.listSuccessAt.IsZero() {
		successAt = float64(c.listSuccessAt.Unix())
	}
	sample(out, "hangar_fleet_list_success_timestamp_seconds", nil, successAt)

	c.mu.Unlock()
	_, _ = buf.WriteTo(dst)
}

package exporter

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/logs"
)

func render(c *Collector) string {
	var b strings.Builder
	c.Render(&b)
	return b.String()
}

func mustHave(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l+"\n") {
			t.Errorf("missing line %q in:\n%s", l, out)
		}
	}
}

func mustNotHave(t *testing.T, out string, frags ...string) {
	t.Helper()
	for _, f := range frags {
		if strings.Contains(out, f) {
			t.Errorf("unexpected %q in:\n%s", f, out)
		}
	}
}

// The contract the dashboard is built against: every worker has up and busy,
// the job's details only while it runs, and finished jobs counted by result
// with their duration.
func TestCollectorLifecycle(t *testing.T) {
	c := NewCollector("v1", "abc")
	c.SetFleet([]fleet.Worker{
		{Index: 1, Name: "box-w1", Registered: true, Running: true},
		{Index: 2, Name: "box-w2", Registered: true, Running: false},
	})
	idle := render(c)
	mustHave(t, idle,
		`hangar_build_info{version="v1",commit="abc"} 1`,
		`hangar_workers_configured 2`,
		`hangar_worker_up{worker="w1",runner="box-w1"} 1`,
		`hangar_worker_up{worker="w2",runner="box-w2"} 0`,
		`hangar_worker_busy{worker="w1",runner="box-w1"} 0`,
		`hangar_jobs_total{worker="w1",runner="box-w1",result="succeeded"} 0`,
		`hangar_job_duration_seconds_count{result="failed"} 0`,
	)
	mustNotHave(t, idle, "hangar_worker_job_info{", "hangar_worker_job_start_timestamp_seconds{")

	start := time.Date(2026, 9, 23, 3, 12, 55, 0, time.UTC)
	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: `build (x, ["a", "b"])`, At: start})
	busy := render(c)
	mustHave(t, busy,
		`hangar_worker_busy{worker="w1",runner="box-w1"} 1`,
		`hangar_worker_job_info{worker="w1",runner="box-w1",job_name="build (x, [\"a\", \"b\"])",workflow="",repo="",run_id=""} 1`,
		`hangar_worker_job_start_timestamp_seconds{worker="w1",runner="box-w1"} 1790133175`,
	)

	c.SetJobInfo(1, start, logs.JobInfo{Name: "build (x)", Repo: "Acme/app", Workflow: "All", RunID: "42"}, "/diag/Worker_x.log", true)
	mustHave(t, render(c),
		`hangar_worker_job_info{worker="w1",runner="box-w1",job_name="build (x)",workflow="All",repo="Acme/app",run_id="42"} 1`)

	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobEnd, Text: "build (x)", Result: "Succeeded", At: start.Add(90 * time.Second)})
	done := render(c)
	mustHave(t, done,
		`hangar_worker_busy{worker="w1",runner="box-w1"} 0`,
		`hangar_jobs_total{worker="w1",runner="box-w1",result="succeeded"} 1`,
		`hangar_worker_last_job_end_timestamp_seconds{worker="w1",runner="box-w1"} 1790133265`,
		`hangar_job_duration_seconds_bucket{result="succeeded",le="60"} 0`,
		`hangar_job_duration_seconds_bucket{result="succeeded",le="120"} 1`,
		`hangar_job_duration_seconds_bucket{result="succeeded",le="+Inf"} 1`,
		`hangar_job_duration_seconds_sum{result="succeeded"} 90`,
	)
	mustNotHave(t, done, "hangar_worker_job_info{")

	// A worker that is gone drops out, series and all.
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "box-w1", Registered: true, Running: true}})
	mustNotHave(t, render(c), `worker="w2"`)
}

// A transition replayed when the exporter attaches restores state but is not
// counted: counting it would add the same job again on every restart.
func TestRecoveredTransitionsAreNotCounted(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 3, Name: "w", Registered: true, Running: true}})
	end := time.Unix(1_790_000_000, 0)
	c.Apply(logs.Event{Worker: 3, Kind: logs.KindJobEnd, Result: "Failed", At: end, Recovered: true})
	out := render(c)
	mustHave(t, out,
		`hangar_jobs_total{worker="w3",runner="w",result="failed"} 0`,
		`hangar_worker_last_job_end_timestamp_seconds{worker="w3",runner="w"} 1790000000`)

	c.Apply(logs.Event{Worker: 3, Kind: logs.KindJobStart, Text: "j", At: end, Recovered: true})
	mustHave(t, render(c), `hangar_worker_busy{worker="w3",runner="w"} 1`)
}

// A worker can go briefly "not up" mid-job — a restart under Restart=always
// or KeepAlive — while the job it started keeps running. Once it is up again
// it reports that job as busy, and hangar_jobs_total and
// hangar_job_duration_seconds_count move together for the job's real end.
func TestJobEndAfterTransientDown(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
	start := time.Unix(1000, 0)
	c.Apply(logsStart(1, start))

	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: false, Running: false}})
	down := render(c)
	mustHave(t, down, `hangar_worker_busy{worker="w1",runner="w"} 0`)
	mustNotHave(t, down, "hangar_worker_job_info{")

	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
	mustHave(t, render(c),
		`hangar_worker_busy{worker="w1",runner="w"} 1`,
		`hangar_worker_job_start_timestamp_seconds{worker="w1",runner="w"} 1000`)

	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobEnd, Result: "Succeeded", At: start.Add(30 * time.Second)})
	mustHave(t, render(c),
		`hangar_jobs_total{worker="w1",runner="w",result="succeeded"} 1`,
		`hangar_job_duration_seconds_count{result="succeeded"} 1`,
		`hangar_job_duration_seconds_sum{result="succeeded"} 30`,
	)
}

func TestResultOf(t *testing.T) {
	for in, want := range map[string]string{
		"Succeeded": "succeeded", "SucceededWithIssues": "succeeded",
		"Failed": "failed", "Canceled": "canceled", "Abandoned": "canceled", "": "failed",
	} {
		if got := resultOf(in); got != want {
			t.Errorf("resultOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// Job details for a job that ended or was replaced are dropped; lookups give
// up after a bounded number of misses.
func TestSetJobInfoGuards(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
	first := time.Unix(1000, 0)
	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: "a", At: first})
	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: "b", At: first.Add(time.Minute)})
	c.SetJobInfo(1, first, logs.JobInfo{Repo: "stale/repo"}, "/diag/Worker_a.log", true)
	mustNotHave(t, render(c), "stale/repo")

	for i := 0; i < infoAttempts; i++ {
		if _, pending := c.PendingInfo()[1]; !pending {
			t.Fatalf("gave up after %d attempts, want %d", i, infoAttempts)
		}
		c.SetJobInfo(1, first.Add(time.Minute), logs.JobInfo{}, "/diag/Worker_b.log", false)
	}
	if _, pending := c.PendingInfo()[1]; pending {
		t.Error("lookups should stop after infoAttempts misses")
	}
}

// PendingInfo hands back the worker log a previous SetJobInfo resolved, so a
// caller can skip resolving it again — globbing _diag once per job rather
// than once per unresolved poll — while the job's message is still being
// written.
func TestPendingInfoCachesLogPath(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
	start := time.Unix(1000, 0)
	c.Apply(logsStart(1, start))

	if p := c.PendingInfo()[1]; p.LogPath != "" {
		t.Fatalf("no lookup made yet: got LogPath %q", p.LogPath)
	}
	c.SetJobInfo(1, start, logs.JobInfo{}, "/diag/Worker_x.log", false)
	p, pending := c.PendingInfo()[1]
	if !pending || p.LogPath != "/diag/Worker_x.log" {
		t.Fatalf("got %+v, want the resolved path carried forward", p)
	}

	// A new job must not inherit the previous one's path.
	c.Apply(logsStart(1, start.Add(time.Minute)))
	if p := c.PendingInfo()[1]; p.LogPath != "" {
		t.Fatalf("a new job must start without a cached path, got %q", p.LogPath)
	}
}

func TestTruncateAndEscape(t *testing.T) {
	long := strings.Repeat("é", 100)
	if got := truncate(long, jobLabelMax); len([]rune(got)) != jobLabelMax {
		t.Errorf("truncate gave %d runes, want %d", len([]rune(got)), jobLabelMax)
	}
	if got := escapeValue("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Errorf("escapeValue = %q", got)
	}
	if got := formatValue(1790046775); got != "1790046775" {
		t.Errorf("a timestamp must print without an exponent, got %q", got)
	}
}

// job and instance are the labels Prometheus attaches to every scraped series;
// one exported under either name is renamed on ingestion and lost to queries.
func TestNoReservedLabels(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
	c.Apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: "j", At: time.Unix(1, 0)})
	reserved := regexp.MustCompile(`[{,](job|instance)="`)
	for _, line := range strings.Split(render(c), "\n") {
		if reserved.MatchString(line) {
			t.Errorf("reserved label in %q", line)
		}
	}
}

func logsStart(n int, at time.Time) logs.Event {
	return logs.Event{Worker: n, Kind: logs.KindJobStart, Text: "j", At: at}
}

func logsListening(n int) logs.Event { return logs.Event{Worker: n, Kind: logs.KindListening} }

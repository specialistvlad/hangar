package logs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Shaped after a real worker log: the job message follows "Job message:" as a
// JSON document, and the next log line ends it. Only the display name and
// three github context values are read.
const workerLog = "\ufeff[2026-09-23 03:12:55Z INFO Worker] Version: 2.337.0\n" +
	"[2026-09-23 03:12:55Z INFO Worker] Job message:\n" +
	`{
  "jobDisplayName": "some (ml_vision_pp) / build",
  "variables": {"system.github.token": {"value": "***", "isSecret": true}},
  "contextData": {
    "github": {
      "t": 2,
      "d": [
        {"k": "repository", "v": "LexSelect/lexselect"},
        {"k": "workflow", "v": "All"},
        {"k": "run_id", "v": "35813233707"},
        {"k": "event", "v": {"t": 2, "d": []}}
      ]
    }
  }
}
` + "[2026-09-23 03:12:55Z INFO JobRunner] Job ID 31562067\n"

func TestFindJobInfo(t *testing.T) {
	dir := t.TempDir()
	diag := filepath.Join(dir, "_diag")
	if err := os.MkdirAll(diag, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 23, 3, 12, 55, 0, time.UTC)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(diag, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, path, ok := FindJobInfo(dir, start, ""); ok || path != "" {
		t.Fatal("no worker log yet: must report not found, with no path to reuse")
	}
	// An older job's log must not be picked for this one.
	write("Worker_20260923-020000-utc.log", workerLog)
	if _, path, ok := FindJobInfo(dir, start, ""); ok || path != "" {
		t.Fatal("a worker log from an hour earlier belongs to another job")
	}
	// Half-written: the JSON has not been closed off by the next log line,
	// but the path is resolved and handed back for the caller to reuse.
	write("Worker_20260923-031255-utc.log", workerLog[:300])
	_, path, ok := FindJobInfo(dir, start, "")
	if ok || path == "" {
		t.Fatal("a partly written job message must be retried, with its path kept")
	}
	// A log that started even closer to start appears afterwards. Passed the
	// path resolved above, FindJobInfo reuses it instead of globbing _diag
	// again, so the new arrival does not change which file this job reads.
	write("Worker_20260923-031254-utc.log", workerLog)
	write("Worker_20260923-031255-utc.log", workerLog)
	info, path2, ok := FindJobInfo(dir, start, path)
	if !ok || path2 != path {
		t.Fatalf("got path %q ok %v, want the resolved path %q reused", path2, ok, path)
	}
	want := JobInfo{Name: "some (ml_vision_pp) / build", Repo: "LexSelect/lexselect", Workflow: "All", RunID: "35813233707"}
	if info != want {
		t.Errorf("got %+v, want %+v", info, want)
	}
}

// Job starts and ends carry the time the runner logged them.
func TestJobEventTimestamps(t *testing.T) {
	e, ok := jobEvent(4, "[2026-09-23 03:11:46Z INFO Terminal] WRITE LINE: 2026-09-23 03:11:46Z: Running job: some (x) / build")
	if !ok || e.Kind != KindJobStart || e.Text != "some (x) / build" {
		t.Fatalf("got %+v, %v", e, ok)
	}
	if want := time.Date(2026, 9, 23, 3, 11, 46, 0, time.UTC); !e.At.Equal(want) {
		t.Errorf("At = %v, want %v", e.At, want)
	}
	e, ok = jobEvent(4, "Job some (x) / build completed with result: Canceled")
	if !ok || e.Kind != KindJobEnd || e.Result != "Canceled" || !e.At.IsZero() {
		t.Errorf("a line without a stamp: got %+v, %v", e, ok)
	}
}

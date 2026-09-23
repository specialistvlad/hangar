package logs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	start1 = "[2026-09-23 03:00:00Z INFO Terminal] WRITE LINE: 2026-09-23 03:00:00Z: Running job: a / build\n"
	end1   = "[2026-09-23 03:05:00Z INFO Terminal] WRITE LINE: 2026-09-23 03:05:00Z: Job a / build completed with result: Succeeded\n"
	start2 = "[2026-09-23 03:06:00Z INFO Terminal] WRITE LINE: 2026-09-23 03:06:00Z: Running job: b / build\n"
	listen = "[2026-09-23 03:07:00Z INFO Terminal] WRITE LINE: 2026-09-23 03:07:00Z: Listening for Jobs\n"
)

func writeLog(t *testing.T, dir, name, body string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func recovered(t *testing.T, files []string) ([]Event, string, int64) {
	t.Helper()
	out := make(chan Event, 10)
	path, off := recoverState(context.Background(), 1, files, out)
	close(out)
	var es []Event
	for e := range out {
		if !e.Recovered {
			t.Errorf("replayed %v not marked Recovered: a counter would count it again", e.Kind)
		}
		es = append(es, e)
	}
	return es, path, off
}

// Attaching mid-job replays the last end — for when it happened — and then the
// running start, in that order, and following resumes right after them.
func TestRecoverRunningJob(t *testing.T) {
	dir := t.TempDir()
	body := "\ufeff" + start1 + end1 + start2
	p := writeLog(t, dir, "Runner_1.log", body, 0)
	es, path, off := recovered(t, []string{p})
	if len(es) != 2 || es[0].Kind != KindJobEnd || es[1].Kind != KindJobStart || es[1].Text != "b / build" {
		t.Fatalf("got %+v, want the end of a then the start of b", es)
	}
	if path != p || off != int64(len(body)) {
		t.Errorf("follow from %s@%d, want %s@%d", path, off, p, len(body))
	}
}

// A listener that restarted is idle, whatever the log before it says: a job
// whose listener died left no end line, and must not stay busy.
func TestRecoverAfterListenerRestart(t *testing.T) {
	dir := t.TempDir()
	old := writeLog(t, dir, "Runner_1.log", start1+end1+start2, time.Hour)
	fresh := writeLog(t, dir, "Runner_2.log", listen, 0)
	es, _, _ := recovered(t, []string{fresh, old})
	if len(es) != 1 || es[0].Kind != KindJobEnd {
		t.Fatalf("got %+v, want only the last end, from the older log", es)
	}
	// A brand-new log with nothing in it yet is the same: a listener starting.
	empty := writeLog(t, dir, "Runner_3.log", "", 0)
	if es, _, _ := recovered(t, []string{empty, old}); len(es) != 1 || es[0].Kind != KindJobEnd {
		t.Fatalf("got %+v, want only the last end", es)
	}
}

// When the runner rotates to a new log, whatever the old one gained since the
// last read — an end, say — is still delivered. A line still being written is
// held back until it is complete.
func TestTailerDrainsRotationAndHoldsPartialLines(t *testing.T) {
	dir := t.TempDir()
	old := writeLog(t, dir, "Runner_1.log", start1, time.Hour)
	tl := &tailer{glob: filepath.Join(dir, "Runner_*.log"), path: old, off: int64(len(start1))}

	f, err := os.OpenFile(old, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(end1)
	_ = f.Close()
	_ = os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	writeLog(t, dir, "Runner_2.log", listen+"[2026-09-23 03:08:00Z INFO Ter", 0)

	lines := tl.read()
	if len(lines) != 2 {
		t.Fatalf("got %q, want the old log's end, then the new log's first complete line", lines)
	}
	if e, ok := jobEvent(1, lines[0]); !ok || e.Kind != KindJobEnd {
		t.Errorf("first line %q should be the end from the rotated-away log", lines[0])
	}
	if e, ok := jobEvent(1, lines[1]); !ok || e.Kind != KindListening {
		t.Errorf("second line %q should be the listener start", lines[1])
	}
	f, _ = os.OpenFile(filepath.Join(dir, "Runner_2.log"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("minal] done\n")
	_ = f.Close()
	if lines := tl.read(); len(lines) != 1 || lines[0] != "[2026-09-23 03:08:00Z INFO Terminal] done" {
		t.Errorf("the completed line should arrive whole, got %q", lines)
	}
}

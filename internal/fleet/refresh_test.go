package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// A worker provisioned before the job hooks existed has a .env without them.
// Scale is a diff that never touches a worker it keeps, so refresh is what
// brings it up to date — and a stopped worker gets its files without being
// started, since its next start reads them anyway.
func TestRefreshRewritesAStaleStoppedWorker(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, true)
	mkWorker(t, f, 2, true, true)
	mkWorker(t, f, 3, true, true) // above the target: left alone
	if err := f.writeWorkerFiles(2); err != nil {
		t.Fatal(err)
	}
	old := "HOME=/old\n"
	for _, n := range []int{1, 3} {
		if err := os.WriteFile(filepath.Join(f.cfg.WorkerDir(n), ".env"), []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var log []string
	ws := listOK(t, f, nil)
	if err := f.refresh(2, ws, nil, func(s string) { log = append(log, s) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(log, "|") != "refreshed t-w1" {
		t.Errorf("progress = %q, want only w1 refreshed", log)
	}
	for n, want := range map[int]bool{1: false, 2: false, 3: true} {
		isStale, err := f.stale(n)
		if err != nil {
			t.Fatal(err)
		}
		if isStale != want {
			t.Errorf("w%d stale = %v, want %v", n, isStale, want)
		}
	}
	env, _ := os.ReadFile(filepath.Join(f.cfg.WorkerDir(1), ".env"))
	if !strings.Contains(string(env), "ACTIONS_RUNNER_HOOK_JOB_STARTED=") {
		t.Errorf("refreshed .env has no job hooks:\n%s", env)
	}
}

// Restarting a worker hangar cannot see is the one thing refresh must not
// guess at: a running worker read as stopped would get new files it never
// loads, and look up to date from then on.
func TestRefreshStopsWhenTheSupervisorDidNotAnswer(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, true)
	err := f.refresh(1, listOK(t, f, nil), errors.New("launchctl list: timed out"), func(string) {})
	if err == nil {
		t.Fatal("a stale worker and no answer from the supervisor must stop the refresh")
	}
	if _, statErr := os.Stat(filepath.Join(f.cfg.WorkerDir(1), ".env")); statErr == nil {
		t.Error("no files may be written when the refresh stops")
	}

	// Up to date, the same failure does not matter: there is nothing to do.
	if err := f.writeWorkerFiles(1); err != nil {
		t.Fatal(err)
	}
	if err := f.refresh(1, listOK(t, f, nil), errors.New("launchctl list: timed out"), func(string) {}); err != nil {
		t.Errorf("nothing stale, yet refresh failed: %v", err)
	}
}

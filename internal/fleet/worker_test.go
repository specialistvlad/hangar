package fleet

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/specialistvlad/hangar/internal/config"
)

// mkWorker lays out worker n on disk: registered writes .runner, ready writes
// the marker provisioning ends with.
func mkWorker(t *testing.T, f *Fleet, n int, registered, ready bool) {
	t.Helper()
	dir := f.cfg.WorkerDir(n)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]bool{".runner": registered, readyMarker: ready} {
		if want {
			if err := os.WriteFile(filepath.Join(dir, file), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// A worker counts as present only once provisioning finished, which its
// marker records. One left behind by a failed or interrupted provision is
// completed rather than reported as done, and one above the target is still
// removed. The supervisor's view plays no part, so this holds on any machine.
func TestPlanCompletesUnfinishedWorkers(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, true)
	mkWorker(t, f, 2, false, false) // failed before registering
	mkWorker(t, f, 3, true, false)  // registered, then interrupted
	mkWorker(t, f, 5, false, false)
	mkWorker(t, f, 6, false, true) // a marker without .runner is not ready either

	add, drop := plan(4, f.list(map[int]int{1: 100, 3: 0}))
	if !reflect.DeepEqual(add, []int{2, 3, 4}) {
		t.Errorf("add = %v, want [2 3 4]", add)
	}
	if !reflect.DeepEqual(drop, []int{6, 5}) {
		t.Errorf("drop = %v, want [6 5]", drop)
	}
}

// Workers from before the marker get it once, if their service runs — that
// meant every provisioning step had finished. A registered one that is not
// running is left to be completed. And a supervisor that could not be asked
// stops the scale: read as "nothing runs", it would rebuild and restart every
// such worker, canceling their jobs.
func TestMigrateMarkers(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, false)
	mkWorker(t, f, 2, true, false)
	mkWorker(t, f, 3, true, true)

	if err := f.migrateMarkers(f.list(map[int]int{1: 100}), errors.New("launchctl list: timeout")); err == nil {
		t.Fatal("a failed listing with unmarked workers must stop the scale")
	}
	if exists(filepath.Join(f.cfg.WorkerDir(1), readyMarker)) {
		t.Fatal("nothing may be marked when the listing failed")
	}

	ws := f.list(map[int]int{1: 100, 2: 0})
	if err := f.migrateMarkers(ws, nil); err != nil {
		t.Fatal(err)
	}
	if add, _ := plan(3, ws); !reflect.DeepEqual(add, []int{2}) {
		t.Errorf("after migration add = %v, want [2]: w1 was running, w2 was not", add)
	}
	if !exists(filepath.Join(f.cfg.WorkerDir(1), readyMarker)) {
		t.Error("the running pre-marker worker should now carry the marker")
	}

	// Once every worker has its marker, a failed listing no longer matters.
	if err := f.migrateMarkers(f.list(nil), errors.New("launchctl list: timeout")); err == nil {
		t.Error("w2 is still unmarked, so the failure must still stop the scale")
	}
}

// The scale lock is held per open file: a second scale waits, or with TryScale
// is told to try again.
func TestLockScale(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scale.lock")
	unlock, err := lockScale(path, func(string) {}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockScale(path, func(string) {}, false); !errors.Is(err, ErrScaleBusy) {
		t.Errorf("a second non-waiting lock should report ErrScaleBusy, got %v", err)
	}
	unlock()
	again, err := lockScale(path, func(string) {}, false)
	if err != nil {
		t.Fatalf("after unlock the lock should be free: %v", err)
	}
	again()
}

// A TMPDIR under a shared WORKER_TMP_ROOT may already exist. Only a real
// directory owned by this account is taken over, and the bits asked for are
// cleared.
func TestClaimDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "w1")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := claimDir(dir, 0o077); err != nil {
		t.Fatalf("own directory refused: %v", err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
	}

	link := filepath.Join(t.TempDir(), "w2")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := claimDir(link, 0o077); err == nil {
		t.Error("a symlink must not be accepted as a worker TMPDIR")
	}
}

// The start-time guard must accept a fresh or existing TMPDIR it owns, and stop
// the worker for a symlinked TMPDIR, a symlinked root, or a root owned by
// someone else.
func TestTmpGuard(t *testing.T) {
	sh := func(tmp string) error {
		return exec.CommandContext(t.Context(), "/bin/sh", "-c", tmpGuard, "sh", tmp).Run()
	}

	root := filepath.Join(t.TempDir(), "root")
	if err := sh(filepath.Join(root, "w1")); err != nil {
		t.Fatalf("fresh TMPDIR refused: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "w1")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("TMPDIR not created 0700: %v %v", fi, err)
	}
	if err := sh(filepath.Join(root, "w1")); err != nil {
		t.Errorf("existing TMPDIR refused: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "w2")); err != nil {
		t.Fatal(err)
	}
	if err := sh(filepath.Join(root, "w2")); err == nil {
		t.Error("a symlinked TMPDIR must stop the worker")
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := sh(filepath.Join(link, "w3")); err == nil {
		t.Error("a symlinked root must stop the worker")
	}
	if exists(filepath.Join(target, "w3")) {
		t.Error("the guard created a directory through the symlinked root")
	}

	// A root this account does not own: /tmp itself is root's. The directory
	// can be created there, so this reaches the parent-owner check itself.
	if os.Getuid() != 0 {
		foreign := filepath.Join("/tmp", fmt.Sprintf("hangar-guard-%d-%d", os.Getpid(), time.Now().UnixNano()))
		t.Cleanup(func() { _ = os.Remove(foreign) })
		if err := sh(foreign); err == nil {
			t.Error("a TMPDIR in a directory owned by someone else must stop the worker")
		}
	}
}

// A WORKER_TMP_ROOT under a directory every account can write to is refused
// before anything is registered: the tmp cleaner sweeps it, and anyone can
// recreate it.
func TestCheckTmpRootRefusesSharedParents(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := checkTmpRoot(filepath.Join(shared, "hangar-tmp")); err == nil {
		t.Error("a root inside a world-writable directory must be refused")
	}
	own := filepath.Join(t.TempDir(), "own")
	if err := os.Mkdir(own, 0o700); err != nil {
		t.Fatal(err)
	}
	// t.TempDir() itself lives under a world-writable /tmp on Linux, so only
	// macOS, whose per-user temp directory is private, exercises the pass case.
	if runtime.GOOS == "darwin" {
		if err := checkTmpRoot(filepath.Join(own, "hangar-tmp")); err != nil {
			t.Errorf("a root in a private directory was refused: %v", err)
		}
	}
}

// Removing a worker deletes its TMPDIR under WORKER_TMP_ROOT, but never follows
// a symlink planted there into somewhere else.
func TestRemoveWorkerLeavesForeignTmpAlone(t *testing.T) {
	root, tmpRoot, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	f := New(&config.Config{Root: root, TmpRoot: tmpRoot})
	if err := os.MkdirAll(f.cfg.WorkerDir(1), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(elsewhere, "keep")
	if err := os.WriteFile(keep, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, f.workerTmp(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.removeWorker(1); err == nil {
		t.Error("a symlinked TMPDIR should be reported, not silently skipped")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("removal reached through the symlink: %v", err)
	}
	if _, err := os.Stat(f.cfg.WorkerDir(1)); !os.IsNotExist(err) {
		t.Error("the worker directory itself should still be removed")
	}
}

// Without a shared plugin directory in the real home — the usual Linux case —
// a worker gets a private, writable one instead of a link to nothing, so a job
// can install a plugin.
func TestFixupDockerPluginsDirectory(t *testing.T) {
	real := t.TempDir()
	t.Setenv("HOME", real)
	f := New(&config.Config{Root: t.TempDir()})

	cfg := filepath.Join(t.TempDir(), ".docker")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.fixupDocker(cfg); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(cfg, "cli-plugins"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("want a private plugins directory, got %v, %v", fi, err)
	}

	shared := filepath.Join(real, ".docker", "cli-plugins")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.fixupDocker(cfg); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(cfg, "cli-plugins")); err != nil || target != shared {
		t.Errorf("with shared plugins present, want a link to %s, got %q, %v", shared, target, err)
	}
}

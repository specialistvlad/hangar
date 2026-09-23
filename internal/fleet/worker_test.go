package fleet

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

// listOK is f.list for a test that has already laid out a readable
// WorkersDir and cares only about the workers, not a ReadDir error.
func listOK(t *testing.T, f *Fleet, loaded map[int]int) []Worker {
	t.Helper()
	ws, err := f.list(loaded)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// WorkersDir is created by scale()'s first MkdirAll, not by StartMetrics, so
// list() must read one that has never existed yet as a genuinely empty fleet
// — not as the same ReadDir failure a permission problem or a stale NFS
// handle would be, which a caller like the exporter must keep telling apart.
func TestListReadsAMissingWorkersDirAsEmpty(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	ws, err := f.list(nil)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if ws != nil {
		t.Errorf("workers = %v, want nil", ws)
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

	add, drop := plan(4, listOK(t, f, map[int]int{1: 100, 3: 0}))
	if !reflect.DeepEqual(add, []int{2, 3, 4}) {
		t.Errorf("add = %v, want [2 3 4]", add)
	}
	if !reflect.DeepEqual(drop, []int{6, 5}) {
		t.Errorf("drop = %v, want [6 5]", drop)
	}
}

// Workers from before the marker get it once, if their service runs — that
// meant every provisioning step had finished. A registered one that is not
// running is left to be completed. A supervisor that could not be asked stops
// any scale that keeps an unmarked worker: read as "nothing runs", it would
// rebuild and restart such workers, canceling their jobs. A scale that removes
// every unmarked worker, and one where all are marked, go ahead regardless.
func TestMigrateMarkers(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, false) // pre-marker, running
	mkWorker(t, f, 2, true, false) // pre-marker, not running
	mkWorker(t, f, 3, true, true)
	mkWorker(t, f, 5, true, false) // pre-marker, above the target below
	failed := errors.New("launchctl list: timeout")

	if err := f.migrateMarkers(3, listOK(t, f, nil), failed); err == nil {
		t.Fatal("a failed listing with an unmarked worker the scale keeps must stop it")
	}
	if exists(filepath.Join(f.cfg.WorkerDir(1), readyMarker)) {
		t.Fatal("nothing may be marked when the listing failed")
	}
	if err := f.migrateMarkers(0, listOK(t, f, nil), failed); err != nil {
		t.Errorf("a scale to 0 keeps no worker, so a failed listing must not stop it: %v", err)
	}

	ws := listOK(t, f, map[int]int{1: 100, 2: 0})
	if err := f.migrateMarkers(3, ws, nil); err != nil {
		t.Fatal(err)
	}
	if add, _ := plan(3, ws); !reflect.DeepEqual(add, []int{2}) {
		t.Errorf("after migration add = %v, want [2]: w1 was running, w2 was not", add)
	}
	if !exists(filepath.Join(f.cfg.WorkerDir(1), readyMarker)) {
		t.Error("the running pre-marker worker should now carry the marker")
	}
	if err := f.migrateMarkers(3, listOK(t, f, nil), failed); err == nil {
		t.Error("w2 is still unmarked, so a failed listing must still stop a scale that keeps it")
	}

	mkWorker(t, f, 2, true, true)
	if err := f.migrateMarkers(3, listOK(t, f, nil), failed); err != nil {
		t.Errorf("with every kept worker marked, a failed listing no longer matters: %v", err)
	}
}

// checkedList must stop a scale outright when WorkersDir itself cannot be
// read, not fold that failure into an already-empty ws the way handing a
// bare listErr to migrateMarkers alone would: with no entries to inspect,
// migrateMarkers's loop never runs and returns nil regardless of the error,
// so a ReadDir failure would look exactly like a genuinely empty fleet and
// plan() would add back — provision restart — every worker already running.
func TestCheckedListStopsOnAnUnreadableWorkersDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads through any permission bits")
	}
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, true) // a healthy, already-running worker
	if err := os.Chmod(f.cfg.WorkersDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.cfg.WorkersDir(), 0o755) })

	ws, err := f.checkedList(3)
	if err == nil {
		t.Fatal("an unreadable WorkersDir must stop the scale, not read as an empty fleet")
	}
	if ws != nil {
		t.Errorf("workers = %v, want nil alongside the error", ws)
	}
}

// ListChecked exists so a caller can tell "stopped" from "could not ask the
// supervisor" — and a ReadDir failure on WorkersDir itself must report the
// same way, not come back as a clean, empty fleet.
func TestListCheckedReportsAnUnreadableWorkersDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads through any permission bits")
	}
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	if err := os.MkdirAll(f.cfg.WorkersDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.cfg.WorkersDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.cfg.WorkersDir(), 0o755) })

	ws, err := f.ListChecked()
	if err == nil {
		t.Fatal("an unreadable WorkersDir must be reported, not read as an empty fleet")
	}
	if ws != nil {
		t.Errorf("workers = %v, want nil alongside the error", ws)
	}
}

// provision reports whether it started the worker's service, not merely
// whether it returned without error, so a caller whose only failure came
// after that — the marker write — knows the worker is already running and
// must not be stopped. A failure before startService, such as a tarball that
// does not exist, never reaches the real supervisor and must report
// started=false.
func TestProvisionReportsNotStartedBeforeTheServiceStarts(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	started, err := f.provision(1, filepath.Join(t.TempDir(), "missing.tar.gz"), "tok", func(string) {})
	if err == nil {
		t.Fatal("a missing tarball must fail provision")
	}
	if started {
		t.Error("started = true, want false: the failure was before startService ever ran")
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

// The waiting variant — what `make N` uses — must block while another
// process holds the lock, report that it is waiting, and acquire it once the
// holder releases. TestLockScale exercises only the non-waiting branch a
// dashboard keypress takes.
func TestLockScaleWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scale.lock")
	unlock, err := lockScale(path, func(string) {}, false)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		unlock func()
		err    error
	}
	waiting := make(chan string, 1)
	done := make(chan result, 1)
	go func() {
		u, err := lockScale(path, func(s string) { waiting <- s }, true)
		done <- result{u, err}
	}()

	select {
	case msg := <-waiting:
		if !strings.Contains(msg, "another scale is running") {
			t.Errorf("progress = %q, want it to mention a running scale", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the blocking lock never reported that it was waiting")
	}

	select {
	case r := <-done:
		t.Fatalf("the blocking lock returned before the holder released it: %v", r.err)
	case <-time.After(200 * time.Millisecond):
	}

	unlock()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the blocking lock did not acquire after release: %v", r.err)
		}
		// Acquiring again while it is still held proves the returned unlock
		// really holds the flock, not just that lockScale returned.
		if _, err := lockScale(path, func(string) {}, false); !errors.Is(err, ErrScaleBusy) {
			t.Errorf("lock still held by the waiter should report ErrScaleBusy, got %v", err)
		}
		r.unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("the blocking lock did not acquire within the timeout after release")
	}
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
	// The pass case needs a private chain of ancestors. t.TempDir() is under a
	// world-writable /tmp on Linux, and on macOS too when TMPDIR points there,
	// so the case runs only when the temp dir really is private.
	own := filepath.Join(t.TempDir(), "own")
	if err := os.Mkdir(own, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(own)
	if err != nil {
		t.Fatal(err)
	}
	for dir := filepath.Dir(real); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm()&0o002 != 0 {
			t.Skipf("temp dir %s is under a world-writable %s; no private place to test the pass case", own, dir)
		}
	}
	if err := checkTmpRoot(filepath.Join(own, "hangar-tmp")); err != nil {
		t.Errorf("a root in a private directory was refused: %v", err)
	}
}

// checkTmpRoot must refuse a WORKER_TMP_ROOT that is itself writable by every
// account, not only one nested inside such a directory — a tmpfs mounted
// mode=1777, or /dev/shm handed straight to WORKER_TMP_ROOT.
func TestCheckTmpRootRefusesWorldWritableRootItself(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared-root")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	err = checkTmpRoot(root)
	if err == nil {
		t.Fatal("a world-writable root itself must be refused")
	}
	// t.TempDir() can itself sit under a world-writable ancestor (a
	// container's /tmp is commonly mode 1777), and checkTmpRoot always
	// echoes the original root as the error's first %s regardless of which
	// directory the walk actually flagged — so a plain Contains(err, real)
	// would pass even if the walk flagged /tmp two levels up instead of
	// root itself. The second %s is the flagged directory; only that one
	// tells the two apart, so a loop that starts one level too high — at
	// filepath.Dir(real) instead of real — cannot pass here by coincidence.
	flagged := "is, or is inside, " + real + ","
	if !strings.Contains(err.Error(), flagged) {
		t.Errorf("error %q does not flag the root itself (%s):\nwant to contain %q", err, real, flagged)
	}
}

// The guard locks the parent down — chmod go-w — before it trusts $1, so a
// TMPDIR whose parent starts out group/other-writable ends up private rather
// than only ever having $1 itself locked down. Checking $1 before closing the
// parent would leave a window for whoever still had write access to the
// parent to swap $1 out from under the check.
func TestTmpGuardPrivatesParentBeforeTrustingTmp(t *testing.T) {
	sh := func(tmp string) error {
		return exec.CommandContext(t.Context(), "/bin/sh", "-c", tmpGuard, "sh", tmp).Run()
	}

	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, "w1")
	if err := sh(tmp); err != nil {
		t.Fatalf("fresh TMPDIR under a permissive parent refused: %v", err)
	}
	if fi, err := os.Stat(root); err != nil || fi.Mode().Perm()&0o022 != 0 {
		t.Errorf("parent mode = %v, %v, want go-w cleared", fi, err)
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("TMPDIR mode = %v, %v, want 0700", fi, err)
	}

	// A symlink planted at $1 while the parent was still writable must still
	// be refused, even though the parent itself now passes every check.
	link := filepath.Join(root, "w2")
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := sh(link); err == nil {
		t.Error("a symlinked TMPDIR under a permissive parent must stop the worker")
	}

	// A parent this account does not own, even one it can create $1 under —
	// /tmp is world-writable but root's — must be refused rather than have
	// its mode changed by the chmod that now runs before $1 is checked.
	if os.Getuid() != 0 {
		foreignParent := filepath.Join("/tmp", fmt.Sprintf("hangar-guard-%d-%d", os.Getpid(), time.Now().UnixNano()))
		t.Cleanup(func() { _ = os.Remove(foreignParent) })
		if err := sh(foreignParent); err == nil {
			t.Error("a TMPDIR in a directory owned by someone else must stop the worker")
		}
		if fi, err := os.Stat("/tmp"); err != nil || fi.Mode().Perm()&0o022 == 0 {
			t.Errorf("/tmp mode = %v, %v, must not have been chmod'd by a check that should have stopped first", fi, err)
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

// Kill takes no lock, but warns that a scale running elsewhere may recreate
// what it removes.
func TestKillWarnsAboutARunningScale(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	if err := os.MkdirAll(f.cfg.WorkersDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockScale(f.lockPath(), func(string) {}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var said []string
	f.Kill(func(s string) { said = append(said, s) })
	for _, s := range said {
		if strings.Contains(s, "a scale is running") {
			return
		}
	}
	t.Errorf("no warning about the running scale in %q", said)
}

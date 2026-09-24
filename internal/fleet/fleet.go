// Package fleet provisions, registers and supervises the runner workers.
//
// hangar deliberately owns no long-running process of its own. The operating
// system's service manager is the supervisor — launchd on macOS, the user's
// systemd manager on Linux: it starts workers at login or boot, restarts them
// if they die, and keeps them alive after the TUI exits. That is why `make
// watch` can be a pure reader and why quitting it never touches a running
// build.
package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/specialistvlad/hangar/internal/config"
)

// hangarLabel is carried by every worker hangar registers. Workflows target it
// to reach the fleet and nothing else; changing it strands every workflow that
// already says `runs-on: [self-hosted, hangar]`.
const hangarLabel = "hangar"

// Fleet manages the set of runner workers described by a Config.
type Fleet struct {
	cfg *config.Config
}

// New returns a Fleet bound to cfg.
func New(cfg *config.Config) *Fleet { return &Fleet{cfg: cfg} }

// Config exposes the loaded settings for callers that need to display them.
func (f *Fleet) Config() *config.Config { return f.cfg }

// Scale reconciles the fleet to exactly n workers. It is a diff, not a rebuild:
// running `scale 4` twice leaves the second run with nothing to do, and workers
// that already exist are never torn down and recreated.
//
// Workers are provisioned in parallel, so progress may be called from several
// goroutines at once.
func (f *Fleet) Scale(n int, progress func(string)) error { return f.scale(n, progress, true) }

// ErrScaleBusy is TryScale's answer while another process is scaling.
var ErrScaleBusy = errors.New("another scale is running — try again when it finishes")

// TryScale is Scale without waiting for a scale already running elsewhere. A
// dashboard keypress must not queue behind another process: by the time it ran,
// the checks that allowed it — no busy worker above the target — would be stale.
func (f *Fleet) TryScale(n int, progress func(string)) error { return f.scale(n, progress, false) }

func (f *Fleet) scale(n int, progress func(string), wait bool) error {
	if n < 0 || n > config.MaxWorkers {
		return fmt.Errorf("worker count must be 0-%d", config.MaxWorkers)
	}
	for _, d := range []string{f.cfg.WorkersDir(), f.cfg.LogsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// One scale at a time across processes. A dashboard's +/- and a `make N`
	// in another terminal are both expected; two passes planning against the
	// same half-built worker would provision it twice and each tear down the
	// other's. Kill takes no lock — it is the escape hatch when this one hangs.
	unlock, err := lockScale(f.lockPath(), progress, wait)
	if err != nil {
		return err
	}
	defer unlock()

	ws, supervisorErr, err := f.checkedList(n)
	if err != nil {
		return err
	}
	add, drop := plan(n, ws)
	if err := f.refresh(n, ws, supervisorErr, progress); err != nil {
		return err
	}
	if len(add) == 0 && len(drop) == 0 {
		progress(fmt.Sprintf("already at %d worker(s)", n))
		return nil
	}

	// One token covers the whole operation — GitHub's runner tokens are valid
	// for an hour and reusable, so there is no reason to mint one per worker.
	if len(drop) > 0 {
		token, err := f.runnerToken("remove")
		if err != nil {
			return err
		}
		err = each(drop, func(i int) error {
			if err := f.deprovision(i, token, progress); err != nil {
				return fmt.Errorf("removing w%d: %w", i, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(add) == 0 {
		return nil
	}

	// Checked before anything is registered: a worker the supervisor cannot keep
	// alive would otherwise show up on GitHub and then quietly go offline.
	if err := f.preflight(); err != nil {
		return err
	}
	tarball, err := f.CachedTarball()
	if err != nil {
		return err
	}
	token, err := f.runnerToken("registration")
	if err != nil {
		return err
	}
	return each(add, func(i int) error {
		started, err := f.provision(i, tarball, token, progress)
		if err == nil {
			return nil
		}
		// A worker whose service already started is left running: migrateMarkers
		// backfills its marker on the next scale, the same trust it already gives
		// a pre-existing worker in this state. Stopping it here over a failure as
		// late as the marker write would kill an otherwise healthy worker.
		if !started {
			f.stopService(i)
		}
		// A worker GitHub already accepted is left on disk, so `make 0` can still
		// unregister it and the next scale completes it in place. One that never
		// registered holds nothing worth keeping.
		if exists(filepath.Join(f.cfg.WorkerDir(i), ".runner")) {
			return fmt.Errorf("creating w%d: %w (left registered: the next scale finishes it, `make 0` removes it)", i, err)
		}
		if rmErr := f.removeWorker(i); rmErr != nil {
			return fmt.Errorf("creating w%d: %w (and cleaning up: %v)", i, err, rmErr)
		}
		return fmt.Errorf("creating w%d: %w", i, err)
	})
}

// provision unpacks, registers and starts one worker — or, for a worker an
// earlier pass registered but did not finish, just the steps after
// registration: running config.sh again would fail with "already configured".
// It reports whether startService succeeded, so a caller that sees a later
// error — the marker write is the only step left after that — knows the
// worker is already live and must not be stopped.
func (f *Fleet) provision(n int, tarball, token string, progress func(string)) (started bool, err error) {
	dir := f.cfg.WorkerDir(n)
	progress(fmt.Sprintf("creating %s", f.cfg.WorkerName(n)))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	// Anything that can fail about the worker's TMPDIR fails here, before
	// GitHub knows the worker exists.
	if err := f.prepareTmp(n); err != nil {
		return false, err
	}
	if !exists(filepath.Join(dir, ".runner")) {
		// The system tar rather than archive/tar: the runner ships symlinks and
		// execute bits that the stdlib reader would need hand-rolled handling for.
		if out, err := run(dir, "tar", "xzf", tarball); err != nil {
			return false, fmt.Errorf("extract: %v: %s", err, out)
		}
		if out, err := runSecret(dir, tokenEnv(token), "./config.sh", f.registerArgs(n)...); err != nil {
			return false, fmt.Errorf("register: %v: %s", err, out)
		}
	}
	// This must follow registration, not precede it. config.sh runs the
	// runner's env.sh, which appends whatever LANG/NVM_BIN/JAVA_HOME happen to be
	// in the calling shell into .env, and rewrites .path from that same
	// environment. Writing ours afterwards is what keeps a worker deterministic
	// rather than a snapshot of whoever ran `make`.
	if err := f.writeIsolation(n); err != nil {
		return false, err
	}
	if err := f.startService(n); err != nil {
		return false, err
	}
	return true, os.WriteFile(filepath.Join(dir, readyMarker), nil, 0o644)
}

// tokenEnv passes a short-lived runner token to config.sh out of band. See
// runSecret: argv is world-readable through `ps`, the environment is not.
func tokenEnv(token string) []string { return []string{"ACTIONS_RUNNER_INPUT_TOKEN=" + token} }

// registerArgs builds the config.sh invocation for worker n. The token is
// deliberately absent — it arrives via tokenEnv.
func (f *Fleet) registerArgs(n int) []string {
	args := []string{
		"--unattended", "--replace",
		"--url", "https://github.com/" + f.cfg.Org,
		"--name", f.cfg.WorkerName(n),
		"--work", "_work",
	}
	if f.cfg.Group != "" {
		args = append(args, "--runnergroup", f.cfg.Group)
	}
	args = append(args, "--labels", labels(f.cfg.Labels))
	return args
}

// labels is what a workflow targets with `runs-on: [self-hosted, hangar]`. The
// runner's own defaults — self-hosted plus the OS and architecture, such as
// macOS and ARM64 — describe the machine, not who manages it, so in an org
// where machines are registered by hand as well as by hangar they cannot pick
// out the fleet. Applying it here rather than through
// RUNNER_LABELS means the fleet is addressable on a stock install, and stays so
// when an operator sets labels of their own.
func labels(extra string) string {
	out := []string{hangarLabel}
	for _, l := range strings.Split(extra, ",") {
		if l = strings.TrimSpace(l); l != "" && !strings.EqualFold(l, hangarLabel) {
			out = append(out, l)
		}
	}
	return strings.Join(out, ",")
}

// deprovision stops, unregisters and deletes one worker.
func (f *Fleet) deprovision(n int, token string, progress func(string)) error {
	dir := f.cfg.WorkerDir(n)
	progress(fmt.Sprintf("removing %s", f.cfg.WorkerName(n)))

	f.stopService(n)
	// Best-effort: a worker whose registration already vanished server-side
	// must still get cleaned up locally rather than wedging the scale-down.
	if _, err := os.Stat(filepath.Join(dir, "config.sh")); err == nil {
		_, _ = runSecret(dir, tokenEnv(token), "./config.sh", "remove")
	}
	return f.removeWorker(n)
}

func (f *Fleet) lockPath() string { return filepath.Join(f.cfg.WorkersDir(), ".scale.lock") }

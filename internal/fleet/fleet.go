// Package fleet provisions, registers and supervises the runner workers.
//
// hangar deliberately owns no long-running process of its own. launchd is the
// supervisor: it starts workers at login, restarts them if they die, and keeps
// them alive after the TUI exits. That is why `make watch` can be a pure reader
// and why quitting it never touches a running build.
package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/specialistvlad/hangar/internal/config"
)

// Fleet manages the set of runner workers described by a Config.
type Fleet struct {
	cfg *config.Config
}

// New returns a Fleet bound to cfg.
func New(cfg *config.Config) *Fleet { return &Fleet{cfg: cfg} }

// Config exposes the loaded settings for callers that need to display them.
func (f *Fleet) Config() *config.Config { return f.cfg }

// Worker is one provisioned runner.
type Worker struct {
	Index   int
	Name    string
	Dir     string
	Running bool
	PID     int
}

// List reports the workers that exist on disk. The directories are the state:
// there is no separate registry file that could drift out of sync with them.
func (f *Fleet) List() []Worker {
	entries, err := os.ReadDir(f.cfg.WorkersDir())
	if err != nil {
		return nil
	}
	loaded := launchctlList()

	var ws []Worker
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "w") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "w"))
		if err != nil {
			continue
		}
		pid := loaded[f.cfg.Label(n)]
		ws = append(ws, Worker{
			Index:   n,
			Name:    f.cfg.WorkerName(n),
			Dir:     f.cfg.WorkerDir(n),
			Running: pid > 0,
			PID:     pid,
		})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Index < ws[j].Index })
	return ws
}

// plan returns the worker indexes to create and to remove to reach n. Removals
// are ordered highest-first so the fleet never has a gap mid-operation.
func (f *Fleet) plan(n int) (add, drop []int) {
	have := map[int]bool{}
	for _, w := range f.List() {
		have[w.Index] = true
	}
	for i := 1; i <= n; i++ {
		if !have[i] {
			add = append(add, i)
		}
	}
	for i := range have {
		if i > n {
			drop = append(drop, i)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(drop)))
	return add, drop
}

// Scale reconciles the fleet to exactly n workers. It is a diff, not a rebuild:
// running `scale 4` twice leaves the second run with nothing to do, and workers
// that already exist are never torn down and recreated.
func (f *Fleet) Scale(n int, progress func(string)) error {
	if n < 0 || n > config.MaxWorkers {
		return fmt.Errorf("worker count must be 0-%d", config.MaxWorkers)
	}
	for _, d := range []string{f.cfg.WorkersDir(), f.cfg.LogsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	add, drop := f.plan(n)
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
		for _, i := range drop {
			if err := f.deprovision(i, token, progress); err != nil {
				return fmt.Errorf("removing w%d: %w", i, err)
			}
		}
	}
	if len(add) == 0 {
		return nil
	}

	tarball, err := f.CachedTarball()
	if err != nil {
		return err
	}
	token, err := f.runnerToken("registration")
	if err != nil {
		return err
	}
	for _, i := range add {
		if err := f.provision(i, tarball, token, progress); err != nil {
			return fmt.Errorf("creating w%d: %w", i, err)
		}
	}
	return nil
}

// provision unpacks, registers and starts one worker.
func (f *Fleet) provision(n int, tarball, token string, progress func(string)) error {
	dir := f.cfg.WorkerDir(n)
	progress(fmt.Sprintf("creating %s", f.cfg.WorkerName(n)))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// bsdtar rather than archive/tar: the runner ships symlinks and execute
	// bits that the stdlib reader would need hand-rolled handling for.
	if out, err := run(dir, "tar", "xzf", tarball); err != nil {
		return fmt.Errorf("extract: %v: %s", err, out)
	}
	if out, err := runSecret(dir, tokenEnv(token), "./config.sh", f.registerArgs(n)...); err != nil {
		return fmt.Errorf("register: %v: %s", err, out)
	}
	// Both of these must follow registration, not precede it. config.sh runs the
	// runner's env.sh, which appends whatever LANG/NVM_BIN/JAVA_HOME happen to be
	// in the calling shell into .env, and rewrites .path from that same
	// environment. Writing ours afterwards is what keeps a worker deterministic
	// rather than a snapshot of whoever ran `make`.
	if err := f.writeIsolation(n); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".path"), []byte(f.runnerPath(n)+"\n"), 0o644); err != nil {
		return err
	}
	return f.startService(n)
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
	if f.cfg.Labels != "" {
		args = append(args, "--labels", f.cfg.Labels)
	}
	return args
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
	return os.RemoveAll(dir)
}

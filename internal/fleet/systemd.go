//go:build linux

package fleet

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// systemd is hangar's supervisor on Linux, in the form of per-user units run by
// the user's own service manager — never root, and nothing outside the user's
// home. Units are enabled into default.target, and with lingering on the user
// manager starts at boot and outlives every login session: the Linux
// equivalent of a launchd agent that also survives logout.
const (
	// systemctlTimeout bounds every call that does not wait on a runner. The
	// dashboard lists units once a second, so this must never wedge it.
	systemctlTimeout = 30 * time.Second
	// stopTimeout outlasts the unit's own TimeoutStopSec, so a stop waiting on a
	// busy runner is ended by systemd rather than by hangar giving up first.
	stopTimeout = 6 * time.Minute
	lingerDir   = "/var/lib/systemd/linger"
)

// reloadMu serializes daemon-reload with the enable and restart that depend on
// it. Workers are provisioned in parallel, and a dozen overlapping reloads buy
// nothing but a slower scale.
var reloadMu sync.Mutex

// preflight refuses to provision workers that would register on GitHub and
// then fail — the fleet that looks healthy while every job breaks.
func (f *Fleet) preflight() error {
	if err := f.supervisorReady(); err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	if err := checkUnitPaths(f.cfg.Root, f.cfg.TmpRoot, f.cfg.NamePrefix); err != nil {
		return err
	}
	return f.checkDockerAccess(u.Username)
}

// userUnitDir is where hangar writes units outside its own folder on Linux:
// the user manager loads them from there and nowhere under the repo. It holds
// the workers' units, which `make 0` removes, and the exporter's, which
// `make metrics-stop` does. The only other place is an opt-in WORKER_TMP_ROOT.
func userUnitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// startService writes worker n's unit, enables it for boot and starts it. A
// unit already at that name belonging to a different checkout of this
// repository sharing the account is left alone: see foreignUnitRoot.
func (f *Fleet) startService(n int) error {
	if err := installRunsvc(f.cfg.WorkerDir(n)); err != nil {
		return err
	}
	dir, err := userUnitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, serviceName(n))
	if other, ok := foreignUnitRoot(path, f.cfg.WorkersDir()); ok {
		return fmt.Errorf("%s already belongs to the checkout at %s — stop it there (`make kill` or "+
			"`make 0`) before this checkout uses worker %d", serviceName(n), other, n)
	}
	body := renderUnit(unitSpec{
		Worker: n,
		Name:   f.cfg.WorkerName(n),
		Dir:    f.cfg.WorkerDir(n),
		Tmp:    f.workerTmp(n),
		Stdout: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.out", n)),
		Stderr: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.err", n)),
	})
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}

	reloadMu.Lock()
	defer reloadMu.Unlock()
	// restart rather than start: a unit left running under the same name from
	// an earlier provision must pick up the file just written.
	for _, args := range [][]string{{"daemon-reload"}, {"enable", serviceName(n)}, {"restart", serviceName(n)}} {
		if out, err := systemctl(systemctlTimeout, args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
		}
	}
	return nil
}

// stopService stops and disables worker n's unit and removes its file. Every
// systemctl step is unconditional and best-effort, so a kill still clears
// what it can when the user manager is unreachable — only the
// foreign-checkout ownership check and the file removal, which both need the
// unit directory, are skipped when userUnitDir() cannot resolve it, the same
// tolerance loadedServices already gives that failure. A unit belonging to a
// different checkout of this repository sharing the account is left alone —
// not stopped, not disabled, not removed: see foreignUnitRoot.
func (f *Fleet) stopService(n int) {
	name := serviceName(n)
	dir, dirErr := userUnitDir()
	var path string
	if dirErr == nil {
		path = filepath.Join(dir, name)
		if _, ok := foreignUnitRoot(path, f.cfg.WorkersDir()); ok {
			return
		}
	}
	_, _ = systemctl(stopTimeout, "disable", "--now", name)
	// KillMode=process — the vendor's choice, kept so a runner self-update can
	// outlive the listener — makes a stop signal only the main process. Whatever
	// a job left behind stays in the unit's cgroup; reap it before the worker
	// directory is deleted from under it.
	_, _ = systemctl(systemctlTimeout, "kill", "--signal=SIGKILL", name)

	if dirErr == nil {
		_ = os.Remove(path)
		// disable removes this link itself; clearing it by hand covers the case
		// where it could not run, so the next boot has no dangling want.
		_ = os.Remove(filepath.Join(dir, "default.target.wants", name))
	}
	reloadMu.Lock()
	_, _ = systemctl(systemctlTimeout, "daemon-reload")
	reloadMu.Unlock()
	_, _ = systemctl(systemctlTimeout, "reset-failed", name)
}

// loadedServices maps the index of every unit belonging to this checkout —
// its WorkingDirectory under workersDir — to its main pid, 0 when it is not
// running. A unit counts if its file exists and is this checkout's, or the
// manager still has it loaded under a name with no file left to check
// ownership against, which stays in scope as it always has: there is nothing
// to tell it apart from this checkout's own. A unit whose file belongs to a
// different checkout is left out entirely, even when the manager reports it
// loaded, so a kill or a scale never reaches it. An error means the manager
// could not be asked; the file-based entries still come back.
func loadedServices(workersDir string) (map[int]int, error) {
	res := map[int]int{}
	foreign := map[int]bool{}
	var names []string
	if dir, err := userUnitDir(); err == nil {
		var own map[int]string
		own, foreign = ownUnitFiles(dir, workersDir)
		for n, name := range own {
			res[n] = 0
			names = append(names, name)
		}
	}
	args := append([]string{"show", "--property=Id,MainPID", unitPrefix + "*.service"}, names...)
	out, err := systemctl(systemctlTimeout, args...)
	if err != nil {
		return res, fmt.Errorf("systemctl --user show: %v: %s", err, strings.TrimSpace(out))
	}
	for n, pid := range parseShow(out) {
		if !foreign[n] {
			res[n] = pid
		}
	}
	return res, nil
}

// systemctl talks to the calling user's service manager.
func systemctl(timeout time.Duration, args ...string) (string, error) {
	return userCmd(timeout, "systemctl", append([]string{"--user", "--no-pager"}, args...)...)
}

// userCmd runs a tool that talks to the calling user's service manager.
// XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS are always set from the uid
// rather than inherited: `sudo -iu` and cron both start without either, and
// without XDG_RUNTIME_DIR the manager cannot be found at all. A bus address
// left over from whoever's shell hangar was started under — plain `su`
// preserves it, unlike the `sudo -iu` the README documents — would otherwise
// take precedence over the one XDG_RUNTIME_DIR implies, and point at some
// other account's bus.
func userCmd(timeout time.Duration, name string, args ...string) (string, error) {
	uid := os.Getuid()
	env := []string{
		fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid),
		fmt.Sprintf("DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus", uid),
	}
	return runEnv(timeout, "", env, name, args...)
}

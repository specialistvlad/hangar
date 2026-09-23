//go:build linux

package fleet

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// supervisorReady checks what every hangar service needs from systemd: a user
// manager that runs at boot and can be reached.
func (f *Fleet) supervisorReady() error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	// Without lingering the user manager — and every worker with it — stops at
	// the last logout and does not start at boot.
	if _, err := os.Stat(filepath.Join(lingerDir, u.Username)); err != nil {
		return fmt.Errorf("lingering is off for %s, so its workers would stop at logout and "+
			"never start at boot — enable it once with: sudo loginctl enable-linger %s", u.Username, u.Username)
	}
	if out, err := systemctl(systemctlTimeout, "show", "--property=Version"); err != nil {
		return fmt.Errorf("cannot reach %s's systemd user manager: %v: %s", u.Username, err, strings.TrimSpace(out))
	}
	return nil
}

// checkDockerAccess asks the user manager itself whether it can open the
// docker socket, because a unit gets the manager's groups, not the ones
// /etc/group lists now. A manager started before `usermod -aG docker` never
// gains the group — and with lingering on, logging out and back in does not
// restart it — so every docker step in every job would fail while the workers
// look healthy.
//
// The probe runs through systemd-run with the shell's own `test`: the
// uutils coreutils some distributions ship answer -w wrongly for a socket
// that is writable through a group. The path travels in the environment, so
// no quoting or $-expansion can change it. It is a oneshot started without
// --wait: systemd-run still waits for a oneshot to finish and reports its exit,
// but only --wait needs the user's session D-Bus, which a minimal server may
// not have and nothing else in hangar needs.
func (f *Fleet) checkDockerAccess(username string) error {
	sock, ok := strings.CutPrefix(f.cfg.DockerHost, "unix://")
	if !ok || sock == "" {
		return nil
	}
	// No daemon at all is for the jobs to report; it is not a stale session.
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	out, err := userCmd(systemctlTimeout, "systemd-run", "--user", "--quiet", "--collect",
		"--property=Type=oneshot", "--setenv=HANGAR_PROBE="+sock, "--",
		"/bin/sh", "-c", `test -r "$HANGAR_PROBE" && test -w "$HANGAR_PROBE"`)
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(out) == "" {
		return fmt.Errorf("%s's systemd user manager cannot open %s, so every docker step in a job "+
			"would fail. A running manager keeps the groups it started with: after adding %s to the "+
			"socket's group, restart it with `sudo systemctl restart user@%d.service` or reboot — "+
			"logging out and back in is not enough while lingering is on", username, sock, username, os.Getuid())
	}
	return fmt.Errorf("could not check docker access through the user manager: %v: %s", err, strings.TrimSpace(out))
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

// startService writes worker n's unit, enables it for boot and starts it.
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
	body := renderUnit(unitSpec{
		Worker: n,
		Name:   f.cfg.WorkerName(n),
		Dir:    f.cfg.WorkerDir(n),
		Tmp:    f.workerTmp(n),
		Stdout: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.out", n)),
		Stderr: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.err", n)),
	})
	if err := os.WriteFile(filepath.Join(dir, serviceName(n)), []byte(body), 0o644); err != nil {
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
// step is best-effort, so a kill still clears what it can when the user
// manager is unreachable.
func (f *Fleet) stopService(n int) {
	name := serviceName(n)
	_, _ = systemctl(stopTimeout, "disable", "--now", name)
	// KillMode=process — the vendor's choice, kept so a runner self-update can
	// outlive the listener — makes a stop signal only the main process. Whatever
	// a job left behind stays in the unit's cgroup; reap it before the worker
	// directory is deleted from under it.
	_, _ = systemctl(systemctlTimeout, "kill", "--signal=SIGKILL", name)

	if dir, err := userUnitDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, name))
		// disable removes this link itself; clearing it by hand covers the case
		// where it could not run, so the next boot has no dangling want.
		_ = os.Remove(filepath.Join(dir, "default.target.wants", name))
	}
	reloadMu.Lock()
	_, _ = systemctl(systemctlTimeout, "daemon-reload")
	reloadMu.Unlock()
	_, _ = systemctl(systemctlTimeout, "reset-failed", name)
}

// loadedServices maps the index of every hangar unit to its main pid, 0 when
// it is not running. A unit counts if its file exists or the manager still has
// it loaded, so a kill reaches both halves even when they disagree. An error
// means the manager could not be asked; the file-based entries still come back.
func loadedServices() (map[int]int, error) {
	res := map[int]int{}
	var names []string
	if dir, err := userUnitDir(); err == nil {
		files, _ := filepath.Glob(filepath.Join(dir, unitPrefix+"*.service"))
		for _, file := range files {
			if n, ok := unitIndex(filepath.Base(file)); ok {
				res[n] = 0
				names = append(names, filepath.Base(file))
			}
		}
	}
	args := append([]string{"show", "--property=Id,MainPID", unitPrefix + "*.service"}, names...)
	out, err := systemctl(systemctlTimeout, args...)
	if err != nil {
		return res, fmt.Errorf("systemctl --user show: %v: %s", err, strings.TrimSpace(out))
	}
	for n, pid := range parseShow(out) {
		res[n] = pid
	}
	return res, nil
}

// systemctl talks to the calling user's service manager.
func systemctl(timeout time.Duration, args ...string) (string, error) {
	return userCmd(timeout, "systemctl", append([]string{"--user", "--no-pager"}, args...)...)
}

// userCmd runs a tool that talks to the calling user's service manager.
// XDG_RUNTIME_DIR is always set from the uid rather than inherited: `sudo -iu`
// and cron both start without it, and without it the manager cannot be found.
func userCmd(timeout time.Duration, name string, args ...string) (string, error) {
	env := []string{fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid())}
	return runEnv(timeout, "", env, name, args...)
}

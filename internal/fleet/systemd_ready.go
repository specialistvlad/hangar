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
)

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
		return dockerAccessErr(username, sock, os.Getuid())
	}
	return fmt.Errorf("could not check docker access through the user manager: %v: %s", err, strings.TrimSpace(out))
}

// dockerAccessErr explains why the user manager cannot reach docker and how
// to fix it — and what that fix costs: restarting the manager is the only way
// to hand it a group it did not start with, but the restart stops every unit
// it runs, not only the worker whose provision hit this check. On a fleet
// that is already up, that includes every other worker and the exporter,
// with any job they are running mid-way through.
func dockerAccessErr(username, sock string, uid int) error {
	return fmt.Errorf("%s's systemd user manager cannot open %s, so every docker step in a job "+
		"would fail. A running manager keeps the groups it started with: after adding %s to the "+
		"socket's group, restart it with `sudo systemctl restart user@%d.service` or reboot — "+
		"logging out and back in is not enough while lingering is on. That restart stops every "+
		"unit this manager runs, not just the worker being added — every other hangar-wN.service "+
		"and hangar-metrics.service, including any job they are mid-way through — so on a fleet "+
		"that is already up, wait until no worker is busy, or run `make 0` first",
		username, sock, username, uid)
}

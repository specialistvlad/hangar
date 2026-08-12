package fleet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Command timeouts. Provisioning unpacks a ~125MB tarball and runs the runner's
// own registration script, so it needs room; launchctl is queried once a second
// by the dashboard and must never be the thing that wedges it.
const (
	provisionTimeout = 10 * time.Minute
	launchctlTimeout = 10 * time.Second
)

// launchd is hangar's supervisor. It starts workers at login, restarts them if
// they die, and — the reason it was chosen — keeps them running after the
// dashboard exits, since hangar never owns a runner as a child process.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s/runsvc.sh</string></array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ACTIONS_RUNNER_SVC</key><string>1</string>
  </dict>
  <key>ProcessType</key><string>Interactive</string>
  <key>SessionCreate</key><true/>
</dict>
</plist>
`

// plistPath is the one thing hangar writes outside its own folder: launchd only
// loads agents at login from ~/Library/LaunchAgents, and reboot survival is
// worth that single exception. `make 0` removes them again.
func (f *Fleet) plistPath(n int) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", f.cfg.Label(n)+".plist")
}

func (f *Fleet) domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// startService writes worker n's launchd agent and loads it.
func (f *Fleet) startService(n int) error {
	dir := f.cfg.WorkerDir(n)
	path := f.plistPath(n)
	if err := installRunsvc(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(plistTemplate,
		f.cfg.Label(n), dir, dir,
		filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.out", n)),
		filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.err", n)),
	)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	// A stale agent under the same label would make bootstrap fail outright.
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+f.cfg.Label(n))
	if out, err := runTimeout(launchctlTimeout, "", "launchctl", "bootstrap", f.domain(), path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// stopService unloads worker n's agent and removes its plist.
func (f *Fleet) stopService(n int) {
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+f.cfg.Label(n))
	_ = os.Remove(f.plistPath(n))
}

// installRunsvc copies the service entrypoint from bin/ up to the runner root.
// The tarball ships it only in bin/; the vendor's svc.sh does this copy as part
// of `install`, and a plist pointing at the root without it makes launchd exit
// 78 (EX_CONFIG) with nothing in stdout to explain why.
func installRunsvc(dir string) error {
	src := filepath.Join(dir, "bin", "runsvc.sh")
	body, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("runner tarball has no bin/runsvc.sh: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "runsvc.sh"), body, 0o755)
}

var launchctlLine = regexp.MustCompile(`^(-|\d+)\s+(-|\d+)\s+(\S+)$`)

// launchctlList maps loaded job labels to their pid. A label present with pid
// "-" is loaded but not currently running, which is reported as not running.
func launchctlList() map[string]int {
	res := map[string]int{}
	out, err := runTimeout(launchctlTimeout, "", "launchctl", "list")
	if err != nil {
		return res
	}
	for _, line := range strings.Split(out, "\n") {
		m := launchctlLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !strings.HasPrefix(m[3], "com.hangar.") {
			continue
		}
		pid, _ := strconv.Atoi(m[1]) // "-" parses to 0, which reads as stopped
		res[m[3]] = pid
	}
	return res
}

// run executes a provisioning command in dir and returns its combined output.
func run(dir, name string, args ...string) (string, error) {
	return runTimeout(provisionTimeout, dir, name, args...)
}

// runSecret is run with extra environment entries, used to keep short-lived
// runner tokens out of argv — `ps` shows a process's command line to every user
// on the machine, while its environment is readable only by the owner. The
// runner accepts any config argument as ACTIONS_RUNNER_INPUT_<ARG>.
//
// Safe against leaking into the worker: the runner's env.sh copies only a fixed
// allowlist (LANG, JAVA_HOME, NVM_BIN, …) into the worker's .env, and hangar
// overwrites that file afterwards anyway.
func runSecret(dir string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runTimeout executes a command with an explicit deadline, so no external tool
// can hang hangar indefinitely.
func runTimeout(timeout time.Duration, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

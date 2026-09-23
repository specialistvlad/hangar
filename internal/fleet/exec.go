package fleet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// provisionTimeout bounds each provisioning command. Provisioning unpacks a
// runner tarball of a few hundred MB and runs the runner's own registration
// script, so it needs room.
const provisionTimeout = 10 * time.Minute

// installRunsvc copies the service entrypoint from bin/ up to the runner root.
// The tarball ships it only in bin/; the vendor's svc.sh does this copy as part
// of `install` on both macOS and Linux, and a service definition pointing at the
// root without it fails to start — launchd exits 78 (EX_CONFIG) with nothing in
// stdout to explain why, systemd reports a bare "No such file or directory".
func installRunsvc(dir string) error {
	src := filepath.Join(dir, "bin", "runsvc.sh")
	body, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("runner tarball has no bin/runsvc.sh: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "runsvc.sh"), body, 0o755)
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
	return runEnv(provisionTimeout, dir, env, name, args...)
}

// runTimeout executes a command with an explicit deadline, so no external tool
// can hang hangar indefinitely.
func runTimeout(timeout time.Duration, dir, name string, args ...string) (string, error) {
	return runEnv(timeout, dir, nil, name, args...)
}

// runEnv is the one place the fleet package starts a process: a deadline, a working
// directory, and extra environment entries appended to hangar's own.
func runEnv(timeout time.Duration, dir string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

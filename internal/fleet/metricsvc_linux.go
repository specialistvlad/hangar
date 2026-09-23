//go:build linux

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The exporter runs as one more user unit next to the workers, supervised the
// same way: enabled for boot, restarted if it dies. Its name stays clear of
// the hangar-w<N> pattern, so scaling and kill never mistake it for a worker.
const metricsUnit = "hangar-metrics.service"

// The binary is an argument to env rather than the executable word, for the
// same reason runsvc.sh is one to bash: see unitTemplate.
const metricsUnitTemplate = `# Written by hangar (make metrics). Rewritten on every start; edits here are lost.
[Unit]
Description=hangar metrics exporter (Prometheus text format on %[1]s)

[Service]
Type=exec
WorkingDirectory=%[2]s
ExecStart=/usr/bin/env %[3]s serve
Restart=always
RestartSec=5
StandardOutput=append:%[4]s
StandardError=append:%[4]s

[Install]
WantedBy=default.target
`

func renderMetricsUnit(addr, root, bin, log string) string {
	return fmt.Sprintf(metricsUnitTemplate, unitValue(addr), unitValue(root), execArg(bin), unitValue(log))
}

// StartMetrics installs the exporter as a user service, enabled for boot, and
// restarts it, so it runs the binary that asked.
func (f *Fleet) StartMetrics() error {
	if err := f.supervisorReady(); err != nil {
		return err
	}
	bin, err := selfPath()
	if err != nil {
		return err
	}
	if err := checkUnitPaths(f.cfg.Root, bin); err != nil {
		return err
	}
	dir, err := userUnitDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, metricsUnit)
	if other, ok := foreignMetricsRoot(path, f.cfg.Root); ok {
		return fmt.Errorf("%s already serves the checkout at %s — run `make metrics-stop` there first", metricsUnit, other)
	}
	for _, d := range []string{dir, f.cfg.LogsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	body := renderMetricsUnit(f.cfg.MetricsAddr, f.cfg.Root, bin, filepath.Join(f.cfg.LogsDir(), "metrics.log"))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	reloadMu.Lock()
	defer reloadMu.Unlock()
	for _, args := range [][]string{{"daemon-reload"}, {"enable", metricsUnit}, {"restart", metricsUnit}} {
		if out, err := systemctl(systemctlTimeout, args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
		}
	}
	return nil
}

// StopMetrics stops and removes the exporter's service. Best-effort, like
// stopping a worker — except for a service that belongs to a different
// checkout of this repository sharing the account, which it leaves running
// and says so, rather than stopping another checkout's exporter out from
// under it.
func (f *Fleet) StopMetrics() error {
	dir, err := userUnitDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, metricsUnit)
	if other, ok := foreignMetricsRoot(path, f.cfg.Root); ok {
		return fmt.Errorf("%s belongs to the checkout at %s — leaving it running; stop it from there with `make metrics-stop`", metricsUnit, other)
	}
	_, _ = systemctl(systemctlTimeout, "disable", "--now", metricsUnit)
	_ = os.Remove(path)
	_ = os.Remove(filepath.Join(dir, "default.target.wants", metricsUnit))
	reloadMu.Lock()
	_, _ = systemctl(systemctlTimeout, "daemon-reload")
	reloadMu.Unlock()
	_, _ = systemctl(systemctlTimeout, "reset-failed", metricsUnit)
	return nil
}

// MetricsRunning says whether the exporter's service has a live process.
func (f *Fleet) MetricsRunning() bool {
	out, err := systemctl(systemctlTimeout, "show", "--property=MainPID", "--value", metricsUnit)
	return err == nil && strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "0"
}

// foreignMetricsRoot reports the checkout root recorded in the exporter's
// unit at path when it is not root: its WorkingDirectory equals a checkout's
// Root directly, and its ExecStart runs that checkout's own .bin/hangar, so
// either one landing outside root means the unit already serves a different
// checkout's fleet — one `make metrics` from another worktree or clone left
// running under this account, still answering on the same port.
func foreignMetricsRoot(path, root string) (string, bool) {
	if dir, ok := unitWorkingDir(path); ok && !underDir(dir, root) {
		return dir, true
	}
	if bin, ok := unitBinArg(path); ok && !underDir(bin, root) {
		return filepath.Dir(filepath.Dir(bin)), true
	}
	return "", false
}

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
	for _, d := range []string{dir, f.cfg.LogsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	body := renderMetricsUnit(f.cfg.MetricsAddr, f.cfg.Root, bin, filepath.Join(f.cfg.LogsDir(), "metrics.log"))
	if err := os.WriteFile(filepath.Join(dir, metricsUnit), []byte(body), 0o644); err != nil {
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
// stopping a worker.
func (f *Fleet) StopMetrics() {
	_, _ = systemctl(systemctlTimeout, "disable", "--now", metricsUnit)
	if dir, err := userUnitDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, metricsUnit))
		_ = os.Remove(filepath.Join(dir, "default.target.wants", metricsUnit))
	}
	reloadMu.Lock()
	_, _ = systemctl(systemctlTimeout, "daemon-reload")
	reloadMu.Unlock()
	_, _ = systemctl(systemctlTimeout, "reset-failed", metricsUnit)
}

// MetricsRunning says whether the exporter's service has a live process.
func (f *Fleet) MetricsRunning() bool {
	out, err := systemctl(systemctlTimeout, "show", "--property=MainPID", "--value", metricsUnit)
	return err == nil && strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "0"
}

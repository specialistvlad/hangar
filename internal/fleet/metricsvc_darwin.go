//go:build darwin

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// The exporter runs as one more launchd agent next to the workers, kept alive
// the same way. Its label stays clear of com.hangar.w<N>, so scaling and kill
// never mistake it for a worker.
const metricsLabel = "com.hangar.metrics"

var metricsPlist = template.Must(template.New("metrics").Funcs(template.FuncMap{"x": xmlText}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{x .Label}}</string>
  <key>ProgramArguments</key>
  <array><string>{{x .Bin}}</string><string>serve</string></array>
  <key>WorkingDirectory</key><string>{{x .Root}}</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>{{x .Log}}</string>
  <key>StandardErrorPath</key><string>{{x .Log}}</string>
</dict>
</plist>
`))

func renderMetricsPlist(bin, root, log string) (string, error) {
	var b strings.Builder
	err := metricsPlist.Execute(&b, struct{ Label, Bin, Root, Log string }{metricsLabel, bin, root, log})
	return b.String(), err
}

func metricsPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", metricsLabel+".plist")
}

// StartMetrics installs the exporter as a login agent and (re)loads it, so it
// runs the binary that asked.
func (f *Fleet) StartMetrics() error {
	bin, err := selfPath()
	if err != nil {
		return err
	}
	path := metricsPlistPath()
	if other, ok := foreignMetricsRoot(path, f.cfg.Root); ok {
		return fmt.Errorf("%s already serves the checkout at %s — run `make metrics-stop` there first", metricsLabel, other)
	}
	if err := os.MkdirAll(f.cfg.LogsDir(), 0o755); err != nil {
		return err
	}
	body, err := renderMetricsPlist(bin, f.cfg.Root, filepath.Join(f.cfg.LogsDir(), "metrics.log"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+metricsLabel)
	if out, err := runTimeout(launchctlTimeout, "", "launchctl", "bootstrap", f.domain(), path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// StopMetrics unloads the exporter's agent and removes its plist —
// except for a plist that belongs to a different checkout of this repository
// sharing the account, which it leaves loaded and says so, rather than
// stopping another checkout's exporter out from under it.
func (f *Fleet) StopMetrics() error {
	path := metricsPlistPath()
	if other, ok := foreignMetricsRoot(path, f.cfg.Root); ok {
		return fmt.Errorf("%s belongs to the checkout at %s — leaving it running; stop it from there with `make metrics-stop`", metricsLabel, other)
	}
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+metricsLabel)
	_ = os.Remove(path)
	return nil
}

// foreignMetricsRoot reports the checkout root recorded in the exporter's
// plist at path when it is not root: its WorkingDirectory equals a
// checkout's Root directly, and its first ProgramArguments entry runs that
// checkout's own .bin/hangar, so either one landing outside root means the
// plist already serves a different checkout's fleet — one `make metrics`
// from another worktree or clone left loaded under this account, still
// answering on the same port.
func foreignMetricsRoot(path, root string) (string, bool) {
	if dir, ok := unitWorkingDir(path); ok && !underDir(dir, root) {
		return dir, true
	}
	if bin, ok := unitBinArg(path); ok && !underDir(bin, root) {
		return filepath.Dir(filepath.Dir(bin)), true
	}
	return "", false
}

// MetricsRunning says whether this checkout's own exporter has a live
// process — an agent that belongs to a different checkout sharing the
// account reports as not running here, the same ownership foreignMetricsRoot
// already gives StartMetrics and StopMetrics, so `make status` never borrows
// another checkout's exporter, on what may even be another checkout's port.
func (f *Fleet) MetricsRunning() bool {
	if _, ok := foreignMetricsRoot(metricsPlistPath(), f.cfg.Root); ok {
		return false
	}
	out, err := runTimeout(launchctlTimeout, "", "launchctl", "list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[2] == metricsLabel {
			return fields[0] != "-"
		}
	}
	return false
}

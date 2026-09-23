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
	if err := os.MkdirAll(f.cfg.LogsDir(), 0o755); err != nil {
		return err
	}
	body, err := renderMetricsPlist(bin, f.cfg.Root, filepath.Join(f.cfg.LogsDir(), "metrics.log"))
	if err != nil {
		return err
	}
	path := metricsPlistPath()
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

// StopMetrics unloads the exporter's agent and removes its plist.
func (f *Fleet) StopMetrics() {
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+metricsLabel)
	_ = os.Remove(metricsPlistPath())
}

// MetricsRunning says whether the exporter's agent has a live process.
func (f *Fleet) MetricsRunning() bool {
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

//go:build darwin

package fleet

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// launchctl is queried once a second by the dashboard and must never be the
// thing that wedges it.
const launchctlTimeout = 10 * time.Second

// launchd is hangar's supervisor on macOS. It starts workers at login, restarts
// them if they die, and — the reason it was chosen — keeps them running after
// the dashboard exits, since hangar never owns a runner as a child process.
//
// The agent runs runsvc.sh through sh, after tmpGuard has recreated and
// checked the worker's TMPDIR — the step the systemd unit takes with
// ExecStartPre. The paths are passed as arguments, so nothing needs shell
// quoting, and exec leaves runsvc.sh as the process launchd tracks and
// signals.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{x .Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>{{x .Script}}</string>
    <string>sh</string>
    <string>{{x .Tmp}}</string>
    <string>{{x .Dir}}/runsvc.sh</string>
  </array>
  <key>WorkingDirectory</key><string>{{x .Dir}}</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>{{x .Stdout}}</string>
  <key>StandardErrorPath</key><string>{{x .Stderr}}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ACTIONS_RUNNER_SVC</key><string>1</string>
  </dict>
  <key>ProcessType</key><string>Interactive</string>
  <key>SessionCreate</key><true/>
</dict>
</plist>
`

// plist escapes every value it interpolates as XML text, so a path holding &,
// < or > cannot break the property list. html/template is no substitute: it
// escapes the <?xml prolog too.
var plist = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlText}).Parse(plistTemplate))

func xmlText(s string) (string, error) {
	var b strings.Builder
	err := xml.EscapeText(&b, []byte(s))
	return b.String(), err
}

// agentSpec is everything that varies between two workers' agents.
type agentSpec struct {
	Label, Dir, Tmp, Stdout, Stderr string
}

func renderPlist(a agentSpec) (string, error) {
	var b strings.Builder
	err := plist.Execute(&b, struct {
		agentSpec
		Script string
	}{a, tmpGuard + `; exec "$2"`})
	return b.String(), err
}

// serviceName is the launchd job label for worker n. Namespaced under
// com.hangar so it can never collide with a runner installed by the vendor's
// own svc.sh.
func serviceName(n int) string { return fmt.Sprintf("com.hangar.w%d", n) }

// labelIndex matches the label serviceName builds. Parsing it back is what lets
// a kill reach an agent whose worker directory has already gone.
var labelIndex = regexp.MustCompile(`^com\.hangar\.w(\d+)$`)

// preflight refuses to provision workers that would register on GitHub and
// then never run: a launchd agent in the GUI domain starts at login by
// construction, and Docker Desktop's socket belongs to the user who runs it,
// so the one thing left to catch here is a path or name the plist cannot
// carry — the same check Linux's preflight runs.
func (f *Fleet) preflight() error {
	return checkUnitPaths(f.cfg.Root, f.cfg.TmpRoot, f.cfg.NamePrefix)
}

// plistPath is where hangar writes agents outside its own folder: launchd only
// loads agents at login from ~/Library/LaunchAgents, and reboot survival is
// worth that exception. The workers' agents go on `make 0`, the exporter's on
// `make metrics-stop`. The only other place is an opt-in WORKER_TMP_ROOT.
func (f *Fleet) plistPath(n int) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceName(n)+".plist")
}

func (f *Fleet) domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// startService writes worker n's launchd agent and loads it. A plist already
// at that name belonging to a different checkout of this repository sharing
// the account is left alone: see foreignUnitRoot.
func (f *Fleet) startService(n int) error {
	dir := f.cfg.WorkerDir(n)
	path := f.plistPath(n)
	if err := installRunsvc(dir); err != nil {
		return err
	}
	if other, ok := foreignUnitRoot(path, f.cfg.WorkersDir()); ok {
		return fmt.Errorf("%s already belongs to the checkout at %s — stop it there (`make kill` or "+
			"`make 0`) before this checkout uses worker %d", serviceName(n), other, n)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := renderPlist(agentSpec{
		Label:  serviceName(n),
		Dir:    dir,
		Tmp:    f.workerTmp(n),
		Stdout: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.out", n)),
		Stderr: filepath.Join(f.cfg.LogsDir(), fmt.Sprintf("w%d.err", n)),
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	// A stale agent under the same label would make bootstrap fail outright.
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+serviceName(n))
	if out, err := runTimeout(launchctlTimeout, "", "launchctl", "bootstrap", f.domain(), path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// stopService unloads worker n's agent and removes its plist. A plist
// belonging to a different checkout of this repository sharing the account
// is left alone — not booted out, not removed: see foreignUnitRoot.
func (f *Fleet) stopService(n int) {
	path := f.plistPath(n)
	if _, ok := foreignUnitRoot(path, f.cfg.WorkersDir()); ok {
		return
	}
	_, _ = runTimeout(launchctlTimeout, "", "launchctl", "bootout", f.domain()+"/"+serviceName(n))
	_ = os.Remove(path)
}

// loadedServices maps the index of every agent belonging to this checkout —
// its WorkingDirectory under workersDir — to its pid. An error means launchd
// could not be asked — which is not the same as nothing being loaded, and
// callers that decide something must tell the two apart. An agent whose
// plist is on disk and is this checkout's counts too, at pid 0, so a kill
// still reaches it when launchd cannot be asked; one whose plist has no file
// left to check ownership against stays in scope the same way, since there is
// nothing to tell it apart from this checkout's own. An agent whose plist
// belongs to a different checkout is left out entirely, even when launchd
// reports it loaded.
func loadedServices(workersDir string) (map[int]int, error) {
	res := map[int]int{}
	home, _ := os.UserHomeDir()
	own, foreign := ownAgents(filepath.Join(home, "Library", "LaunchAgents"), workersDir)
	for n := range own {
		res[n] = 0
	}
	out, err := runTimeout(launchctlTimeout, "", "launchctl", "list")
	if err != nil {
		return res, fmt.Errorf("launchctl list: %v", err)
	}
	for n, pid := range parseLaunchctlList(out) {
		if !foreign[n] {
			res[n] = pid
		}
	}
	return res, nil
}

// launchctlLine matches one `launchctl list` row: pid, last exit status,
// label. The status is negative when the last exit was a signal — "-9" after a
// SIGKILL — so it is matched as any token rather than as digits.
var launchctlLine = regexp.MustCompile(`^(-|\d+)\s+\S+\s+(\S+)$`)

// parseLaunchctlList reads `launchctl list` output. A label present with pid
// "-" is loaded but not currently running, which is reported as pid 0; jobs
// hangar does not own are skipped.
func parseLaunchctlList(out string) map[int]int {
	res := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		m := launchctlLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		idx := labelIndex.FindStringSubmatch(m[2])
		if idx == nil {
			continue
		}
		n, err := strconv.Atoi(idx[1])
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(m[1]) // "-" parses to 0, which reads as stopped
		res[n] = pid
	}
	return res
}

//go:build linux

package fleet

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const unitPrefix = "hangar-w"

// serviceName is the systemd unit for worker n. The hangar- prefix keeps it
// clear of units the vendor's svc.sh installs, which are named actions.runner.*.
func serviceName(n int) string { return fmt.Sprintf("%s%d.service", unitPrefix, n) }

var unitName = regexp.MustCompile(`^hangar-w(\d+)\.service$`)

// unitIndex parses serviceName back, which is what lets a kill reach a unit
// whose worker directory has already gone.
func unitIndex(name string) (int, bool) {
	m := unitName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// unitTemplate follows the vendor's own actions.runner.service.template —
// runsvc.sh, KillMode=process, SIGTERM with a five-minute grace — and adds
// what that template leaves to a root-installed system unit:
//
//   - runsvc.sh is run as an argument to bash rather than as the executable.
//     systemd parses the executable word of an Exec line more strictly than
//     its arguments: it rejects quotes and backslashes there outright and does
//     not unescape $$, so a worker path holding any of those would never
//     start. As an argument every character survives quoting. runsvc.sh is a
//     bash script, so the process tree is the same either way.
//   - Type=exec, so a start that cannot exec fails `systemctl restart` —
//     and with it the scale — instead of reporting success and then looping.
//   - Restart=always with a 10s throttle, matching the launchd agent's
//     KeepAlive and ThrottleInterval, since nothing else will restart it.
//   - WantedBy=default.target, the user manager's boot target.
//   - tmpGuard before every start: the worker's TMPDIR recreated — it may live
//     on a tmpfs that a reboot wiped — and refused unless this account owns it
//     and the directory holding it.
//   - A 65536 open-file limit. A user service otherwise starts at a soft limit
//     of 1024, which JavaScript toolchains exhaust mid-build.
//   - Output appended to logs/, the same files the launchd agent writes.
//
// No After=network-online.target: a user manager cannot order against system
// targets, and the runner retries its own connection anyway.
const unitTemplate = `# Written by hangar. Scaling rewrites or removes it; edits here are lost.
[Unit]
Description=hangar worker %[1]d (GitHub Actions runner %[2]s)

[Service]
Type=exec
WorkingDirectory=%[3]s
ExecStartPre=/bin/sh -c %[8]s sh %[4]s
ExecStart=/bin/bash %[5]s
KillMode=process
KillSignal=SIGTERM
TimeoutStopSec=5min
Restart=always
RestartSec=10
LimitNOFILE=65536
StandardOutput=append:%[6]s
StandardError=append:%[7]s

[Install]
WantedBy=default.target
`

// unitSpec is everything that varies between two workers' units.
type unitSpec struct {
	Worker int
	Name   string
	Dir    string
	Tmp    string
	Stdout string
	Stderr string
}

func renderUnit(s unitSpec) string {
	return fmt.Sprintf(unitTemplate,
		s.Worker, unitValue(s.Name),
		unitValue(s.Dir),
		execArg(s.Tmp),
		execArg(s.Dir+"/runsvc.sh"),
		unitValue(s.Stdout), unitValue(s.Stderr),
		execArg(tmpGuard),
	)
}

// unitValue escapes a setting that expands %-specifiers, so a path holding a
// literal % is not read as one.
func unitValue(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// execArg quotes one argument — never the executable — of an Exec*= line.
// Arguments expand $VARIABLES and split on whitespace, so the path is
// double-quoted with its backslashes, quotes, dollar signs and percent signs
// escaped. Control characters cannot be written into a unit at all; preflight
// refuses paths that hold one.
func execArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

// parseShow reads `systemctl show --property=Id,MainPID` output: one block per
// unit, blocks separated by a blank line. Units hangar does not own are
// skipped, and a unit listed twice — once by pattern, once by name — collapses.
func parseShow(out string) map[int]int {
	res := map[int]int{}
	for _, block := range strings.Split(out, "\n\n") {
		id, pid := "", 0
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch k {
			case "Id":
				id = v
			case "MainPID":
				pid, _ = strconv.Atoi(v)
			}
		}
		if n, ok := unitIndex(id); ok {
			res[n] = pid
		}
	}
	return res
}

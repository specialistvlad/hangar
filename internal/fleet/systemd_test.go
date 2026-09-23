//go:build linux

package fleet

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// userCmd must hand every command its own XDG_RUNTIME_DIR and
// DBUS_SESSION_BUS_ADDRESS, derived from the uid, regardless of what either
// was set to in hangar's own environment — the case a stale inherited bus
// address (e.g. from a plain `su` rather than `sudo -iu`) would otherwise slip
// through as.
func TestUserCmdSetsRuntimeAndBusFromUID(t *testing.T) {
	uid := os.Getuid()
	// The seeded values stand in for whatever hangar's own process inherited
	// and must belong to some other uid, or a test run as uid 0 — as the
	// container this suite's own Linux check runs in does, with no --user
	// flag — would seed the real uid's own values and could pass whether or
	// not userCmd actually overrides them.
	foreign := uid + 1
	t.Setenv("XDG_RUNTIME_DIR", fmt.Sprintf("/run/user/%d", foreign))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", fmt.Sprintf("unix:path=/run/user/%d/bus", foreign))

	out, err := userCmd(5*time.Second, "sh", "-c", `printf '%s %s' "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS"`)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("/run/user/%d unix:path=/run/user/%d/bus", uid, uid)
	if out != want {
		t.Errorf("userCmd env = %q, want %q", out, want)
	}
}

// The unit is what keeps a worker alive across crashes, logouts and reboots, so
// the settings that do that are pinned here rather than trusted to survive an
// innocent-looking edit of the template.
func TestRenderUnitKeepsTheWorkerAlive(t *testing.T) {
	unit := renderUnit(unitSpec{
		Worker: 3, Name: "box-w3", Dir: "/srv/hangar/workers/w3", Tmp: "/tmp/hangar/w3",
		Stdout: "/srv/hangar/logs/w3.out", Stderr: "/srv/hangar/logs/w3.err",
	})
	for _, want := range []string{
		`ExecStart=/bin/bash "/srv/hangar/workers/w3/runsvc.sh"`,
		"Type=exec",
		"WorkingDirectory=/srv/hangar/workers/w3",
		`ExecStartPre=/bin/sh -c "[ ! -L \"$${1%%/*}\" ] && mkdir -p \"$$1\" && `,
		`[ -O \"$${1%%/*}\" ] && chmod go-w \"$${1%%/*}\" && [ -d \"$$1\" ] && [ ! -L \"$$1\" ] && [ -O \"$$1\" ]`,
		`exit 78; }" sh "/tmp/hangar/w3"`,
		"Restart=always",
		"KillMode=process",
		"WantedBy=default.target",
		"StandardOutput=append:/srv/hangar/logs/w3.out",
		"StandardError=append:/srv/hangar/logs/w3.err",
		"LimitNOFILE=65536",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q\n%s", want, unit)
		}
	}
}

// systemd expands %-specifiers in every setting, and $VARIABLES and quoting in
// Exec arguments. A path holding any of those must reach the process
// unchanged, which is why runsvc.sh is passed as an argument: systemd rejects
// quotes and backslashes in the executable word of an Exec line.
func TestRenderUnitEscapesPaths(t *testing.T) {
	unit := renderUnit(unitSpec{
		Worker: 1, Name: "w%1", Dir: `/data/my "fleet" 100%/$HOME\w1`, Tmp: `/t/$x "y"`,
		Stdout: "/l/100%.out", Stderr: "/l/e",
	})
	for _, want := range []string{
		`ExecStart=/bin/bash "/data/my \"fleet\" 100%%/$$HOME\\w1/runsvc.sh"`,
		`exit 78; }" sh "/t/$$x \"y\""`,
		`WorkingDirectory=/data/my "fleet" 100%%/$HOME\w1`,
		"StandardOutput=append:/l/100%%.out",
		"(GitHub Actions runner w%%1)",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q\n%s", want, unit)
		}
	}
}

// `systemctl show` prints one block per unit. Units hangar does not own come
// back from a pattern too, a unit can be named twice, and a unit that is loaded
// but not running reports MainPID=0 — a kill must still reach it.
func TestParseShow(t *testing.T) {
	out := `Id=hangar-w2.service
MainPID=0

Id=hangar-w1.service
MainPID=4242

Id=hangar-watchdog.service
MainPID=7

Id=hangar-w1.service
MainPID=4242

Id=hangar-w10.service
MainPID=11
`
	want := map[int]int{1: 4242, 2: 0, 10: 11}
	if got := parseShow(out); !reflect.DeepEqual(got, want) {
		t.Errorf("parseShow = %v, want %v", got, want)
	}
	if got := parseShow(""); len(got) != 0 {
		t.Errorf("empty output should yield nothing, got %v", got)
	}
}

// The restart checkDockerAccess suggests stops every unit the user manager
// runs, not only the worker whose provision hit the check, so the message
// must say that before an operator copies the command onto a live fleet.
func TestDockerAccessErrWarnsAboutTheWholeFleet(t *testing.T) {
	err := dockerAccessErr("hangar", "/var/run/docker.sock", 1001)
	for _, want := range []string{
		"sudo systemctl restart user@1001.service",
		"stops every unit this manager runs",
		"hangar-wN.service",
		"hangar-metrics.service",
		"wait until no worker is busy",
		"make 0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("dockerAccessErr missing %q\n%s", want, err)
		}
	}
}

func TestUnitIndex(t *testing.T) {
	if n, ok := unitIndex(serviceName(12)); !ok || n != 12 {
		t.Errorf("serviceName(12) = %q does not parse back to 12", serviceName(12))
	}
	for _, name := range []string{"hangar-w.service", "hangar-wx.service", "actions.runner.x.service", "hangar-w1.service.d"} {
		if _, ok := unitIndex(name); ok {
			t.Errorf("unitIndex(%q) should not match", name)
		}
	}
}

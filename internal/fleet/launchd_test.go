//go:build darwin

package fleet

import (
	"reflect"
	"strings"
	"testing"
)

// `launchctl list` prints every job in the session. Only hangar's own agents
// may come back, keyed by worker index, with a loaded-but-stopped agent as pid
// 0 — a kill must still boot it out.
func TestParseLaunchctlList(t *testing.T) {
	out := `PID	Status	Label
123	0	com.hangar.w1
-	78	com.hangar.w2
456	0	com.apple.Finder
789	0	com.hangar.watchdog
-	0	com.hangar.w10
321	-9	com.hangar.w3
-	-15	com.hangar.w4
`
	// w3 was restarted after a SIGKILL and is running; w4 was stopped by one.
	want := map[int]int{1: 123, 2: 0, 3: 321, 4: 0, 10: 0}
	if got := parseLaunchctlList(out); !reflect.DeepEqual(got, want) {
		t.Errorf("parseLaunchctlList = %v, want %v", got, want)
	}
}

func TestServiceNameRoundTrips(t *testing.T) {
	m := labelIndex.FindStringSubmatch(serviceName(7))
	if m == nil || m[1] != "7" {
		t.Errorf("serviceName(7) = %q does not parse back to 7", serviceName(7))
	}
}

// The agent must recreate and check TMPDIR before runsvc.sh starts — a RAM
// disk is empty after a reboot, and a directory someone else owns must stop
// the worker — and hand both paths over as arguments, so a path with spaces or
// XML metacharacters survives intact.
func TestRenderPlist(t *testing.T) {
	body, err := renderPlist(agentSpec{
		Label: "com.hangar.w2", Dir: "/Users/me/hangar & co/workers/w2", Tmp: "/Volumes/ram/w2",
		Stdout: "/l/w2.out", Stderr: "/l/w2.err",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>/bin/sh</string>",
		`[ ! -L &#34;$1&#34; ] &amp;&amp; [ -O &#34;$1&#34; ] &amp;&amp; [ -O &#34;${1%/*}&#34; ]`,
		"exit 78; }; exec &#34;$2&#34;</string>",
		"<string>/Volumes/ram/w2</string>",
		"<string>/Users/me/hangar &amp; co/workers/w2/runsvc.sh</string>",
		"<key>WorkingDirectory</key><string>/Users/me/hangar &amp; co/workers/w2</string>",
		"<key>ACTIONS_RUNNER_SVC</key><string>1</string>",
		"<key>KeepAlive</key><true/>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q\n%s", want, body)
		}
	}
}

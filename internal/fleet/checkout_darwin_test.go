//go:build darwin

package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// unitWorkingDir must read back exactly what renderPlist wrote, including a
// path with an XML metacharacter, and report ok=false for a plist that has
// not been written yet.
func TestUnitWorkingDirRoundTripsRenderPlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.hangar.w2.plist")
	if _, ok := unitWorkingDir(path); ok {
		t.Error("a plist that has not been written yet must report ok=false")
	}

	body, err := renderPlist(agentSpec{
		Label: "com.hangar.w2", Dir: "/Users/me/hangar & co/workers/w2", Tmp: "/Volumes/ram/w2",
		Stdout: "/l/w2.out", Stderr: "/l/w2.err",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "/Users/me/hangar & co/workers/w2"
	if got, ok := unitWorkingDir(path); !ok || got != want {
		t.Errorf("unitWorkingDir = %q, %v, want %q, true", got, ok, want)
	}
}

// unitBinArg must read back the exporter's first ProgramArguments entry, the
// only plist where a worker's own leading /bin/sh would not be the useful
// value.
func TestUnitBinArgRoundTripsRenderMetricsPlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.hangar.metrics.plist")
	body, err := renderMetricsPlist("/Users/me/h & co/.bin/hangar", "/Users/me/h & co", "/l/metrics.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := unitBinArg(path); !ok || got != "/Users/me/h & co/.bin/hangar" {
		t.Errorf("unitBinArg = %q, %v, want /Users/me/h & co/.bin/hangar, true", got, ok)
	}
	if got, ok := unitWorkingDir(path); !ok || got != "/Users/me/h & co" {
		t.Errorf("unitWorkingDir = %q, %v, want /Users/me/h & co, true", got, ok)
	}
}

// startService and stopService must refuse to touch a plist that already
// belongs to a different checkout — its WorkingDirectory falls outside this
// checkout's WorkersDir — and leave one that is unwritten, or already this
// checkout's own, alone to proceed.
func TestForeignUnitRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.hangar.w1.plist")
	if _, ok := foreignUnitRoot(path, "/srv/a/workers"); ok {
		t.Error("a plist that has not been written yet must not be treated as foreign")
	}

	write := func(dir string) {
		body, err := renderPlist(agentSpec{Label: "com.hangar.w1", Dir: dir, Tmp: "/tmp/w1", Stdout: "/l/w1.out", Stderr: "/l/w1.err"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("/srv/a/workers/w1")
	if _, ok := foreignUnitRoot(path, "/srv/a/workers"); ok {
		t.Error("this checkout's own plist must not be treated as foreign")
	}

	write("/srv/b/workers/w1")
	if root, ok := foreignUnitRoot(path, "/srv/a/workers"); !ok || root != "/srv/b" {
		t.Errorf("foreignUnitRoot = %q, %v, want /srv/b, true", root, ok)
	}
}

// loadedServices must count only this checkout's own agents from what it
// finds on disk, keep an index shared with another checkout out of its own
// view entirely, and leave a foreign agent's plist untouched — ownAgents is
// what it asks before it ever calls launchd.
func TestOwnAgents(t *testing.T) {
	dir := t.TempDir()
	write := func(name, workingDir string) {
		body, err := renderPlist(agentSpec{Label: "com.hangar.w", Dir: workingDir, Tmp: "/tmp", Stdout: "/l/o", Stderr: "/l/e"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("com.hangar.w1.plist", "/srv/a/workers/w1")    // this checkout's
	write("com.hangar.w2.plist", "/srv/b/workers/w2")    // another checkout's
	write("com.apple.Finder.plist", "/srv/a/workers/w9") // not a worker agent at all

	own, foreign := ownAgents(dir, "/srv/a/workers")
	if want := map[int]bool{1: true}; !reflect.DeepEqual(own, want) {
		t.Errorf("own = %v, want %v", own, want)
	}
	if !foreign[2] {
		t.Error("worker 2's agent belongs to another checkout and must be reported foreign")
	}
	if foreign[1] {
		t.Error("worker 1's own agent must not be reported foreign")
	}
	if _, err := os.Stat(filepath.Join(dir, "com.hangar.w2.plist")); err != nil {
		t.Errorf("the foreign agent's plist must be left in place: %v", err)
	}
}

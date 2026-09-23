//go:build linux

package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// unitWorkingDir must read back exactly what renderUnit wrote, including a
// path with characters unitValue doubles, and report ok=false for a unit that
// has not been written yet.
func TestUnitWorkingDirRoundTripsRenderUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hangar-w3.service")
	if _, ok := unitWorkingDir(path); ok {
		t.Error("a unit that has not been written yet must report ok=false")
	}

	body := renderUnit(unitSpec{
		Worker: 3, Name: "box-w3", Dir: `/srv/h "x" 100%/workers/w3`, Tmp: "/tmp/w3",
		Stdout: "/l/w3.out", Stderr: "/l/w3.err",
	})
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := `/srv/h "x" 100%/workers/w3`
	if got, ok := unitWorkingDir(path); !ok || got != want {
		t.Errorf("unitWorkingDir = %q, %v, want %q, true", got, ok, want)
	}
}

// unitBinArg must read back the exporter's ExecStart argument through the
// same escaping renderMetricsUnit applies to a binary path.
func TestUnitBinArgRoundTripsRenderMetricsUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hangar-metrics.service")
	if _, ok := unitBinArg(path); ok {
		t.Error("a unit that has not been written yet must report ok=false")
	}

	body := renderMetricsUnit("127.0.0.1:9151", "/srv/h $x", `/srv/h $x/.bin/hangar`, "/l/metrics.log")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := unitWorkingDir(path); !ok || got != "/srv/h $x" {
		t.Errorf("unitWorkingDir = %q, %v, want /srv/h $x, true", got, ok)
	}
	if got, ok := unitBinArg(path); !ok || got != "/srv/h $x/.bin/hangar" {
		t.Errorf("unitBinArg = %q, %v, want /srv/h $x/.bin/hangar, true", got, ok)
	}
}

// startService and stopService must refuse to touch a unit that already
// belongs to a different checkout — its WorkingDirectory falls outside this
// checkout's WorkersDir — and leave one that is unwritten, or already this
// checkout's own, alone to proceed.
func TestForeignUnitRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hangar-w1.service")
	if _, ok := foreignUnitRoot(path, "/srv/a/workers"); ok {
		t.Error("a unit that has not been written yet must not be treated as foreign")
	}

	write := func(dir string) {
		body := renderUnit(unitSpec{Worker: 1, Name: "w1", Dir: dir, Tmp: "/tmp/w1", Stdout: "/l/w1.out", Stderr: "/l/w1.err"})
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("/srv/a/workers/w1")
	if _, ok := foreignUnitRoot(path, "/srv/a/workers"); ok {
		t.Error("this checkout's own unit must not be treated as foreign")
	}

	write("/srv/b/workers/w1")
	if root, ok := foreignUnitRoot(path, "/srv/a/workers"); !ok || root != "/srv/b" {
		t.Errorf("foreignUnitRoot = %q, %v, want /srv/b, true", root, ok)
	}
}

// loadedServices must count only this checkout's own units from what it
// finds on disk, keep an index shared with another checkout out of its own
// view entirely, and leave a foreign unit's file untouched — ownUnitFiles is
// what it asks before it ever calls systemd.
func TestOwnUnitFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, workingDir string) {
		body := renderUnit(unitSpec{Worker: 1, Name: "w", Dir: workingDir, Tmp: "/tmp", Stdout: "/l/o", Stderr: "/l/e"})
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hangar-w1.service", "/srv/a/workers/w1")  // this checkout's
	write("hangar-w2.service", "/srv/b/workers/w2")  // another checkout's
	write("not-hangar.service", "/srv/a/workers/w9") // not a worker unit at all

	own, foreign := ownUnitFiles(dir, "/srv/a/workers")
	if want := map[int]string{1: "hangar-w1.service"}; !reflect.DeepEqual(own, want) {
		t.Errorf("own = %v, want %v", own, want)
	}
	if !foreign[2] {
		t.Error("worker 2's unit belongs to another checkout and must be reported foreign")
	}
	if foreign[1] {
		t.Error("worker 1's own unit must not be reported foreign")
	}
	if _, err := os.Stat(filepath.Join(dir, "hangar-w2.service")); err != nil {
		t.Errorf("the foreign unit's file must be left in place: %v", err)
	}
}

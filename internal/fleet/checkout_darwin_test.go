//go:build darwin

package fleet

import (
	"os"
	"path/filepath"
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

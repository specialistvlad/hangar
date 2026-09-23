//go:build linux

package fleet

import (
	"os"
	"path/filepath"
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

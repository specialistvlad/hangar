package logs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The sample lines below are copied verbatim from a real runner's _diag files.
// These regexes are the fleet's entire notion of "what is this worker doing",
// so they are pinned against real output rather than invented output.

func TestJobStart(t *testing.T) {
	for _, line := range []string{
		"Running job: release (api) / build",
		"[2026-08-12 18:13:37Z INFO JobDispatcher] Running job: release (web) / build",
	} {
		m := reJobStart.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("no match for %q", line)
		}
		if want := "release ("; m[1][:9] != want {
			t.Errorf("got job %q, want it to start with %q", m[1], want)
		}
		if m[1][len(m[1])-1] == ' ' {
			t.Errorf("job name %q has trailing space", m[1])
		}
	}
}

func TestJobEnd(t *testing.T) {
	line := "Job release (web) / build completed with result: Succeeded"
	m := reJobEnd.FindStringSubmatch(line)
	if m == nil {
		t.Fatal("no match")
	}
	if m[1] != "release (web) / build" {
		t.Errorf("job = %q", m[1])
	}
	if m[2] != "Succeeded" {
		t.Errorf("result = %q", m[2])
	}
}

// A "completed" line must not be mistaken for a start, or a worker would look
// permanently busy after its first job.
func TestJobEndIsNotAStart(t *testing.T) {
	line := "Job release (api) / build completed with result: Failed"
	if reJobStart.MatchString(line) {
		t.Error("end line matched the start pattern")
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		"2026-08-12T18:14:43.1112040Z #30 [release 3/4] COPY api/entrypoint.sh": "#30 [release 3/4] COPY api/entrypoint.sh",
		"2026-08-12T18:13:37.3807210Z ##[group]GITHUB_TOKEN Permissions":          "GITHUB_TOKEN Permissions",
		"2026-08-12T18:14:43.1113330Z #30 DONE 0.0s":                              "#30 DONE 0.0s",
		"2026-08-12T18:14:43.0000000Z ##[error]build failed":                      "error: build failed",
		"2026-08-12T18:14:43.0000000Z \x1b[1m\x1b[32mok\x1b[0m":                   "ok",
		"2026-08-12T18:14:43.0000000Z   ":                                         "",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// tailer must follow rotation: when the runner opens a log for the next step,
// the new file is read from its beginning, not from the old file's offset.
func TestTailerFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	glob := filepath.Join(dir, "*.log")

	first := filepath.Join(dir, "a.log")
	if err := os.WriteFile(first, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tl := &tailer{glob: glob}
	tl.seekEnd() // attaching mid-stream must not replay history
	if got := tl.read(); len(got) != 0 {
		t.Fatalf("seekEnd then read returned %v, want nothing", got)
	}

	if err := os.WriteFile(first, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tl.read(); len(got) != 1 || got[0] != "three" {
		t.Fatalf("append: got %v, want [three]", got)
	}

	// A newer file appears — the whole thing should be read.
	second := filepath.Join(dir, "b.log")
	if err := os.WriteFile(second, []byte("\ufeff"+"four\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(second, time.Now().Add(time.Second), time.Now().Add(time.Second))
	got := tl.read()
	if len(got) != 1 || got[0] != "four" {
		t.Fatalf("rotation: got %#v, want [four] (BOM stripped)", got)
	}
}

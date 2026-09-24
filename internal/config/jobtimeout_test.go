package config

import (
	"os"
	"path/filepath"
	"testing"
)

// JOB_TIMEOUT_MINUTES defaults to half an hour, 0 turns it off, and a value
// that is not a plain number of minutes fails Load instead of falling back —
// "90m" silently read as the default would stop a 90-minute build at 30.
func TestJobTimeout(t *testing.T) {
	load := func(body string) (*Config, error) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(dir)
	}
	for body, want := range map[string]int{
		"":                           DefaultJobTimeoutMinutes,
		"JOB_TIMEOUT_MINUTES=\n":     DefaultJobTimeoutMinutes,
		"JOB_TIMEOUT_MINUTES=0\n":    0,
		"JOB_TIMEOUT_MINUTES=90\n":   90,
		"JOB_TIMEOUT_MINUTES=\"45\"": 45,
		"JOB_TIMEOUT_MINUTES=7200\n": 7200,
	} {
		c, err := load(body)
		if err != nil {
			t.Errorf("Load(%q): %v", body, err)
			continue
		}
		if c.JobTimeoutMinutes != want {
			t.Errorf("Load(%q).JobTimeoutMinutes = %d, want %d", body, c.JobTimeoutMinutes, want)
		}
	}
	for _, bad := range []string{"90m", "1.5", "-1", "7201", "thirty", "30 # minutes"} {
		if _, err := load("JOB_TIMEOUT_MINUTES=" + bad + "\n"); err == nil {
			t.Errorf("JOB_TIMEOUT_MINUTES=%s accepted", bad)
		}
	}
}

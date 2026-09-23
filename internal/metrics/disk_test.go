//go:build darwin || linux

package metrics

import (
	"path/filepath"
	"testing"
)

// Unreadable paths are skipped rather than reported as full, and a readable one
// is labeled so the dashboard can say which disk it is watching.
func TestTightestSkipsMissingPaths(t *testing.T) {
	dir := t.TempDir()
	free, total, label, _ := tightest([]Disk{
		{Label: "gone", Path: filepath.Join(dir, "missing")},
		{Label: "empty", Path: ""},
		{Label: "workers", Path: dir},
	})
	if label != "workers" || total == 0 || free > total {
		t.Errorf("tightest = %d/%d %q, want the readable path", free, total, label)
	}
	if _, _, label, _ := tightest(nil); label != "" {
		t.Errorf("nothing watched should report nothing, got %q", label)
	}
}

// A small, nearly empty tmpfs must not outrank a large volume that is filling:
// each is judged against its own size.
func TestDiskLevelScalesWithSize(t *testing.T) {
	const G = 1 << 30
	for _, c := range []struct {
		free, total uint64
		want        int
	}{
		{30 * G, 32 * G, DiskOK},        // an empty 32G tmpfs is fine
		{6 * G, 32 * G, DiskLow},        // under a quarter free
		{2 * G, 32 * G, DiskCritical},   // under a tenth free
		{200 * G, 500 * G, DiskOK},      // plenty on a large volume
		{60 * G, 500 * G, DiskLow},      // below the absolute 80G
		{20 * G, 500 * G, DiskCritical}, // below the absolute 30G
		{90 * G, 4000 * G, DiskOK},      // huge disks use the absolute figures
		{70 * G, 4000 * G, DiskLow},
	} {
		if got := diskLevel(c.free, c.total); got != c.want {
			t.Errorf("diskLevel(%dG free of %dG) = %d, want %d", c.free/G, c.total/G, got, c.want)
		}
	}
}

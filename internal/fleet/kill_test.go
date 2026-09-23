package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// A force-kill exists for the case where GitHub cannot be reached at all, so it
// must not depend on the two sides of the fleet agreeing. A worker directory
// with no service, and a service whose directory was already deleted, are both
// leftovers a scale-down would have cleaned up, and both have to be torn down.
// Which services hangar owns is decided by each platform's parser, tested
// alongside it.
func TestKillTargetsCoverDisksAndServices(t *testing.T) {
	for _, tc := range []struct {
		name   string
		disk   []Worker
		loaded map[int]int
		want   []int
	}{
		{
			name:   "matched pairs are targeted once each",
			disk:   []Worker{{Index: 1}, {Index: 2}},
			loaded: map[int]int{1: 100, 2: 101},
			want:   []int{1, 2},
		},
		{
			name:   "a service whose directory is gone is still stopped",
			disk:   nil,
			loaded: map[int]int{3: 102},
			want:   []int{3},
		},
		{
			name:   "a directory with no service is still deleted",
			disk:   []Worker{{Index: 4}},
			loaded: nil,
			want:   []int{4},
		},
		{
			name:   "targets come back in ascending order",
			disk:   []Worker{{Index: 10}, {Index: 2}},
			loaded: map[int]int{1: 0},
			want:   []int{1, 2, 10},
		},
		{
			name:   "nothing on either side means nothing to kill",
			disk:   nil,
			loaded: map[int]int{},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := killTargets(tc.disk, tc.loaded)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("killTargets = %v, want %v", got, tc.want)
			}
		})
	}
}

// Kill reports how many targets it attempted alongside how many removeWorker
// left behind, so main.go can print a distinct summary and exit non-zero
// instead of calling a kill complete when a worker directory is still there.
func TestKillReportsWorkersLeftOnDisk(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can remove through any permission bits")
	}
	f := New(&config.Config{Root: t.TempDir(), NamePrefix: "t-w"})
	mkWorker(t, f, 1, true, true)
	dir := f.cfg.WorkerDir(1)
	if err := os.WriteFile(filepath.Join(dir, "stuck"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No write permission on the worker's own directory: removeWorker can list
	// "stuck" but cannot unlink it, the same shape as a file a job's container
	// left behind as root.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	attempted, left := f.Kill(func(string) {})
	if attempted != 1 {
		t.Errorf("attempted = %d, want 1", attempted)
	}
	if left != 1 {
		t.Errorf("left = %d, want 1: removeWorker could not clear the read-only directory", left)
	}
}

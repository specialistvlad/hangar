package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// readyMarker is written as the last step of provisioning. .runner alone is
// not enough: config.sh writes it partway through, before hangar has written
// the worker's isolation or started its service.
const readyMarker = ".hangar-ready"

// Worker is one runner directory under workers/.
type Worker struct {
	Index int
	Name  string
	Dir   string
	// Registered says GitHub accepted the runner: config.sh writes .runner only
	// then. Ready says hangar finished the worker as well: its marker, written
	// as provisioning's last step, is there. Anything short of Ready is a
	// provision that failed or was interrupted, and the next scale completes it
	// in place. Workers from before the marker get one; see migrateMarkers.
	Registered bool
	Ready      bool
	Running    bool
	PID        int
}

// List reports the workers that exist on disk. The directories are the state:
// there is no separate registry file that could drift out of sync with them.
func (f *Fleet) List() []Worker {
	loaded, _ := loadedServices(f.cfg.WorkersDir()) // for display: a failed listing shows workers as stopped
	return f.list(loaded)
}

// ListChecked is List for a caller that must tell "stopped" from "could not
// ask the supervisor": the workers on disk come back either way.
func (f *Fleet) ListChecked() ([]Worker, error) {
	loaded, err := loadedServices(f.cfg.WorkersDir())
	return f.list(loaded), err
}

// list is List against a given view of the supervisor, so that what is
// decided from a listing can also be tested without one.
func (f *Fleet) list(loaded map[int]int) []Worker {
	entries, err := os.ReadDir(f.cfg.WorkersDir())
	if err != nil {
		return nil
	}

	var ws []Worker
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "w") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "w"))
		if err != nil {
			continue
		}
		dir := f.cfg.WorkerDir(n)
		registered := exists(filepath.Join(dir, ".runner"))
		pid := loaded[n]
		ws = append(ws, Worker{
			Index:      n,
			Name:       f.cfg.WorkerName(n),
			Dir:        dir,
			Registered: registered,
			Ready:      registered && exists(filepath.Join(dir, readyMarker)),
			Running:    pid > 0,
			PID:        pid,
		})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Index < ws[j].Index })
	return ws
}

// plan returns the worker indexes to create and to remove to reach n. Removals
// are ordered highest-first so the fleet never has a gap mid-operation.
//
// A worker counts as present only once it is Ready. One that is not — left by
// a provision that failed or was interrupted, or by a removal that could not
// delete everything — is completed in place: see provision.
func plan(n int, ws []Worker) (add, drop []int) {
	have := map[int]bool{}
	ready := map[int]bool{}
	for _, w := range ws {
		have[w.Index] = true
		ready[w.Index] = w.Ready
	}
	for i := 1; i <= n; i++ {
		if !ready[i] {
			add = append(add, i)
		}
	}
	for i := range have {
		if i > n {
			drop = append(drop, i)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(drop)))
	return add, drop
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// migrateMarkers gives the ready marker to workers provisioned before it
// existed. Before the marker, a worker whose service runs had finished every
// provisioning step, so a running one is marked; one that is registered but
// not running is left to be completed in place. The supervisor must have
// answered: reading a failed listing as "nothing runs" would rebuild — and
// restart — every such worker, canceling their jobs. Only workers a scale to n
// keeps matter: one above n is removed either way, so a scale-down still works
// when the supervisor does not answer.
func (f *Fleet) migrateMarkers(n int, ws []Worker, listErr error) error {
	for i, w := range ws {
		if !w.Registered || w.Ready || w.Index > n {
			continue
		}
		if listErr != nil {
			return fmt.Errorf("cannot tell which workers are running, so not scaling: %w — retry", listErr)
		}
		if !w.Running {
			continue
		}
		if err := os.WriteFile(filepath.Join(w.Dir, readyMarker), nil, 0o644); err != nil {
			return err
		}
		ws[i].Ready = true
	}
	return nil
}

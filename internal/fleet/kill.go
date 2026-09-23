package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

// killTargets is every worker index the machine still holds a trace of: a
// directory under workers/, a service the supervisor still knows, or both.
// Scale works from the directories alone because it can trust its own
// bookkeeping; a kill is reached for when that bookkeeping is already suspect,
// so it takes the union.
func killTargets(disk []Worker, loaded map[int]int) []int {
	seen := map[int]bool{}
	for _, w := range disk {
		seen[w.Index] = true
	}
	for n := range loaded {
		seen[n] = true
	}

	var out []int
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Kill tears the whole fleet off this machine without talking to GitHub: it
// stops each worker's service, removes its definition and deletes the worker's
// directories.
//
// It is the escape hatch for the case Scale cannot serve. Scale unregisters
// every worker server-side first, which needs a removal token, so an expired or
// wrong-scoped GH_TOKEN leaves a running fleet with no way to stop it. Kill
// gives up the server-side half to keep the local half always available: the
// registrations survive, listed in the org as offline, until a token exists to
// remove them or an operator deletes them by hand.
//
// It returns the number of workers torn down.
func (f *Fleet) Kill(progress func(string)) int {
	// Kill takes no lock — it is what is left when a scale hangs — but a scale
	// still running elsewhere would recreate workers it has not reached yet.
	if unlock, err := lockScale(f.lockPath(), progress, false); errors.Is(err, ErrScaleBusy) {
		progress("warning: a scale is running in another process and may recreate workers — stop it and run kill again")
	} else if err == nil {
		unlock()
	}
	loaded, err := loadedServices(f.cfg.WorkersDir())
	if err != nil {
		progress(fmt.Sprintf("warning: %v — killing what is on disk and in the service definitions", err))
	}
	disk, listErr := f.list(loaded)
	if listErr != nil {
		progress(fmt.Sprintf("warning: %v — killing what the supervisor lists", listErr))
	}
	targets := killTargets(disk, loaded)
	for _, n := range targets {
		progress(fmt.Sprintf("killing %s", f.cfg.WorkerName(n)))
		f.stopService(n)
		if err := f.removeWorker(n); err != nil {
			progress(fmt.Sprintf("  w%d left on disk: %v", n, err))
		}
	}
	return len(targets)
}

// removeWorker deletes everything on disk that belongs to worker n: its
// directory, and its TMPDIR when WORKER_TMP_ROOT put that somewhere else.
func (f *Fleet) removeWorker(n int) error {
	dir := f.cfg.WorkerDir(n)
	err := os.RemoveAll(dir)
	if errors.Is(err, fs.ErrPermission) {
		// On Linux a container that bind-mounts part of a worker writes as root,
		// so a job can leave files its own account cannot delete — in the
		// workspace, the private home or TMPDIR alike.
		err = rootOwnedHint(err, f.cfg.WorkersDir(), fmt.Sprintf("w%d", n))
	}
	if f.cfg.TmpRoot == "" {
		return err
	}
	// Only a directory this account owns is removed. Anything else at that
	// path — a symlink, or another user's directory — was not made by hangar.
	tmp := f.workerTmp(n)
	fi, statErr := os.Lstat(tmp)
	switch {
	case os.IsNotExist(statErr):
	case statErr != nil:
		err = errors.Join(err, statErr)
	case !fi.IsDir() || !ownedByMe(fi):
		err = errors.Join(err, fmt.Errorf("left %s in place: not a directory this account owns", tmp))
	default:
		tmpErr := os.RemoveAll(tmp)
		if errors.Is(tmpErr, fs.ErrPermission) {
			tmpErr = rootOwnedHint(tmpErr, f.cfg.TmpRoot, fmt.Sprintf("w%d", n))
		}
		err = errors.Join(err, tmpErr)
	}
	return err
}

// rootOwnedHint explains a permission error from deleting parent/name and how
// to finish the job: the fleet's account can still reach docker, and a
// container can delete what a container wrote.
func rootOwnedHint(err error, parent, name string) error {
	return fmt.Errorf("%w — likely files a container wrote as root; remove them through docker, "+
		"then scale again: docker run --rm -v %q:/w alpine rm -rf /w/%s", err, parent, name)
}

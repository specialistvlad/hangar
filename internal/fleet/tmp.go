package fleet

import (
	"fmt"
	"os"
	"path/filepath"
)

// tmpGuard runs before every worker start, on both platforms, with the
// worker's TMPDIR as $1. It recreates the directory — a RAM disk or tmpfs is
// empty after a reboot — and refuses to start the worker unless the directory
// and its parent both belong to this account and neither is a symlink:
// whoever controls the parent can swap the directory, and with it any script a
// job writes there, while the runner uses it. The parent is locked down —
// chmod go-w — before $1 is trusted, and only then is $1 itself checked and
// chmod 0700: checking $1 first and closing the parent after would leave a
// window, between the two, for whoever still had write access to the parent
// to swap $1 out from under the check. The parent is checked for a symlink
// again after mkdir too, since that is the state the runner will use. Shell
// builtins only, so it behaves the same under dash and macOS sh; BSD install
// exits 0 when its chmod fails, so it is not used.
const tmpGuard = `[ ! -L "${1%/*}" ] && mkdir -p "$1" && [ ! -L "${1%/*}" ] && [ -O "${1%/*}" ] && ` +
	`chmod go-w "${1%/*}" && [ -d "$1" ] && [ ! -L "$1" ] && [ -O "$1" ] && chmod 0700 "$1" || ` +
	`{ echo "hangar: refusing TMPDIR $1 - it must be a directory, not a symlink, owned by this account, inside a directory this account owns" >&2; exit 78; }`

// prepareTmp creates worker n's TMPDIR and checks it the way tmpGuard will at
// every start, so a TMPDIR that would stop the worker fails the provision
// before anything is registered.
func (f *Fleet) prepareTmp(n int) error {
	if f.cfg.TmpRoot != "" {
		if err := checkTmpRoot(f.cfg.TmpRoot); err != nil {
			return err
		}
	}
	tmp := f.workerTmp(n)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	if err := claimDir(filepath.Dir(tmp), 0o022); err != nil {
		return err
	}
	return claimDir(tmp, 0o077)
}

// checkTmpRoot refuses a WORKER_TMP_ROOT that is itself, or sits inside, a
// directory every account can write to — /tmp, /var/tmp, /dev/shm and the
// like, or a tmpfs mounted with a permissive mode. The system's tmp cleaner
// sweeps those while workers run, deleting an idle worker's TMPDIR between
// starts, and once the root itself is gone any other account can recreate it
// and the workers' directories under it, with no start for tmpGuard to catch
// it.
func checkTmpRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for dir := real; ; dir = filepath.Dir(dir) {
		fi, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("WORKER_TMP_ROOT %s is, or is inside, %s, which every account can write to — "+
				"use a directory or mount only this account can write to (e.g. mount a tmpfs with "+
				"mode=0700,uid=<fleet user>)", root, dir)
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// claimDir makes sure dir is a real directory this account owns, then clears
// the given permission bits. A worker's TMPDIR and the directory holding it may
// already exist — left by an earlier fleet, or planted by another user of a
// shared WORKER_TMP_ROOT — and a job writing scripts into a directory someone
// else controls could have them swapped before they run.
func claimDir(dir string, clear os.FileMode) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || !ownedByMe(fi) {
		return fmt.Errorf("%s is not a directory owned by this account — a worker's TMPDIR and the "+
			"directory holding it must belong to the fleet's account alone (see WORKER_TMP_ROOT)", dir)
	}
	if fi.Mode().Perm()&clear == 0 {
		return nil
	}
	return os.Chmod(dir, fi.Mode().Perm()&^clear)
}

//go:build darwin || linux

package fleet

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// ownedByMe says a file belongs to the account hangar runs as.
func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// lockScale takes an exclusive lock that lives as long as this process holds
// it: the kernel drops a flock when its holder exits, however it exits, so a
// crashed scale never leaves the fleet locked. The file is opened close-on-exec,
// so tar and config.sh do not inherit it. With wait a second scale waits its
// turn and says so; without, it returns ErrScaleBusy.
func lockScale(path string, progress func(string), wait bool) (func(), error) {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(fh.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = fh.Close()
			return nil, err
		}
		if !wait {
			_ = fh.Close()
			return nil, ErrScaleBusy
		}
		progress("another scale is running — waiting for it to finish")
		if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
			_ = fh.Close()
			return nil, err
		}
	}
	return func() { _ = fh.Close() }, nil
}

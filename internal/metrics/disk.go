//go:build darwin || linux

package metrics

import "syscall"

// Disk levels, from fine to about to run out.
const (
	DiskOK = iota
	DiskLow
	DiskCritical
)

// diskLevel judges free space against the filesystem's own size. Fixed
// thresholds cannot serve a 32G tmpfs and a 500G docker volume at once: the
// tmpfs would read low while empty, and hide the volume that fills first.
// On large disks the absolute 30G and 80G caps take over — from 300G and 320G
// respectively — since parallel image builds need tens of gigabytes however
// large the disk is.
func diskLevel(free, total uint64) int {
	switch {
	case free < min(30<<30, total/10):
		return DiskCritical
	case free < min(80<<30, total/4):
		return DiskLow
	}
	return DiskOK
}

// tightest reports the watched filesystem closest to running out: the worst
// level, then the least space free. Paths that cannot be read are skipped.
//
// On Linux a directory under an ext4 or XFS project quota reports the quota
// rather than the whole filesystem — but only when the directory carries the
// project-inherit flag (chattr +P) and quotas are enforced; otherwise it
// reports the whole filesystem.
func tightest(disks []Disk) (free, total uint64, label string, level int) {
	found := false
	for _, d := range disks {
		var st syscall.Statfs_t
		if d.Path == "" || syscall.Statfs(d.Path, &st) != nil {
			continue
		}
		f, t := st.Bavail*uint64(st.Bsize), st.Blocks*uint64(st.Bsize)
		l := diskLevel(f, t)
		if !found || l > level || (l == level && f < free) {
			free, total, label, level, found = f, t, d.Label, l, true
		}
	}
	return free, total, label, level
}

//go:build !darwin && !linux

package metrics

import "time"

// hangar samples macOS through sysctl, vm_stat and ps, and Linux through /proc
// and cgroups. The stubs below exist only so that building for another platform
// stops on one name that says why, rather than on a list of undefined
// functions.
var _ = hangarRunsOnlyOnMacOSAndLinux

type dockerState struct{}

func memTotal() uint64 { return 0 }
func memUsed() uint64  { return 0 }
func loadAvg() float64 { return 0 }

func tightest([]Disk) (free, total uint64, label string, level int) { return 0, 0, "", 0 }
func dockerUnavailable() string                                     { return "unsupported platform" }
func platformDisks(string) ([]Disk, bool)                           { return nil, true }

func (s *Sampler) dockerUsage(time.Time) (float64, uint64, bool) { return 0, 0, false }

const (
	DiskOK = iota
	DiskLow
	DiskCritical
)

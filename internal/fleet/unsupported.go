//go:build !darwin && !linux

package fleet

import "io/fs"

// hangar supervises workers with launchd on macOS and systemd on Linux, and has
// no supervisor for anything else. The stubs below exist only so that building
// for another platform stops on one name that says why, rather than on a list
// of undefined functions.
var _ = hangarRunsOnlyOnMacOSAndLinux

func (f *Fleet) preflight() error                          { return nil }
func (f *Fleet) startService(int) error                    { return nil }
func (f *Fleet) stopService(int)                           {}
func loadedServices() (map[int]int, error)                 { return nil, nil }
func ownedByMe(fs.FileInfo) bool                           { return false }
func lockScale(string, func(string), bool) (func(), error) { return func() {}, nil }

func (f *Fleet) StartMetrics() error  { return nil }
func (f *Fleet) StopMetrics()         {}
func (f *Fleet) MetricsRunning() bool { return false }

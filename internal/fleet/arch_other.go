//go:build !darwin

package fleet

import "runtime"

// machineArch is the CPU the runner should be built for. Off macOS there is no
// translation layer that runs a binary of another architecture unnoticed, so
// hangar's own architecture is the machine's.
func machineArch() string { return runtime.GOARCH }

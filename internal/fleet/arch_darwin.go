//go:build darwin

package fleet

import (
	"runtime"
	"strings"
	"time"
)

// machineArch is the CPU the runner should be built for. On a Mac that is the
// hardware, not the architecture hangar itself was compiled for: an x86_64 Go
// toolchain on Apple Silicon builds an amd64 hangar that runs under Rosetta,
// and following its GOARCH would install x64 runners that register as X64 and
// never receive jobs aimed at ARM64. hw.optional.arm64 reads 1 on Apple Silicon
// even from under Rosetta, and does not exist on an Intel Mac.
func machineArch() string {
	// By absolute path: /usr/sbin is missing from cron's and many minimal PATHs,
	// and falling back to GOARCH there would bring back the Rosetta runners.
	out, err := runTimeout(5*time.Second, "", "/usr/sbin/sysctl", "-n", "hw.optional.arm64")
	if err == nil && strings.TrimSpace(out) == "1" {
		return "arm64"
	}
	return runtime.GOARCH
}

package fleet

import (
	"os"
	"path/filepath"
)

// selfPath is the running hangar binary, symlinks resolved, for a service to
// start again later: `make build` replaces it in place, and a restart picks up
// the new one.
func selfPath() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(bin)
}

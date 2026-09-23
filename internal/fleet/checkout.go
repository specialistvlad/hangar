package fleet

import (
	"path/filepath"
	"strings"
)

// underDir reports whether path is dir itself or somewhere inside it, once
// both are cleaned — the test every ownership check below is built from: a
// unit or plist's recorded WorkingDirectory or binary path either falls
// under this checkout's own tree or it does not.
func underDir(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

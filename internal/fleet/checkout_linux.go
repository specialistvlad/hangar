//go:build linux

package fleet

import (
	"os"
	"strings"
)

// unitWorkingDir reads WorkingDirectory= from a unit file already on disk —
// a setting every unit hangar has written carries, on this branch and on
// whatever wrote the file before this ownership check existed — so
// startService and StartMetrics can tell a name already claimed by a
// different checkout of this repository sharing the account apart from this
// checkout's own, before overwriting or restarting it. A missing file, or
// one with no such line, reports ok=false: there is nothing to compare, so
// the caller treats the name as unclaimed.
func unitWorkingDir(path string) (string, bool) {
	return unitSetting(path, "WorkingDirectory=")
}

// unitBinArg reads the quoted argument off a unit's ExecStart= line,
// reversing the escaping execArg applied when it was written. Only the
// exporter's unit needs this: its ExecStart runs this checkout's own binary,
// so a foreign one there is as telling as a foreign WorkingDirectory.
func unitBinArg(path string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	r := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `$$`, `$`, `%%`, `%`)
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		i, j := strings.IndexByte(line, '"'), strings.LastIndexByte(line, '"')
		if i >= 0 && j > i {
			return r.Replace(line[i+1 : j]), true
		}
	}
	return "", false
}

// unitSetting reads the first line of body starting with prefix, undoing the
// %-doubling unitValue applies when a unit is written.
func unitSetting(path, prefix string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return strings.ReplaceAll(strings.TrimSpace(v), "%%", "%"), true
		}
	}
	return "", false
}

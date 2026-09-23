//go:build linux

package fleet

import (
	"os"
	"path/filepath"
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

// foreignUnitRoot reports the checkout root recorded in the worker unit
// already at path when it is not, or is not beneath, workersDir: startService
// must never write over or restart such a unit, since doing so would
// silently take over a fleet that is not this checkout's. ok is false when
// there is nothing to protect — no unit yet, or one this checkout already
// wrote.
func foreignUnitRoot(path, workersDir string) (string, bool) {
	dir, ok := unitWorkingDir(path)
	if !ok || underDir(dir, workersDir) {
		return "", false
	}
	// dir is <root>/workers/wN; walking up twice recovers <root>.
	return filepath.Dir(filepath.Dir(dir)), true
}

// ownUnitFiles globs dir for hangar's worker units and splits their indexes
// into this checkout's own — a file whose WorkingDirectory falls under
// workersDir — and another checkout's, before loadedServices ever asks
// systemd about them. A unit systemd still has loaded whose file is gone
// carries no signal to split on, so it is in neither map, which is exactly
// what leaves it in scope for loadedServices as it always has been.
func ownUnitFiles(dir, workersDir string) (own map[int]string, foreign map[int]bool) {
	own, foreign = map[int]string{}, map[int]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, unitPrefix+"*.service"))
	for _, file := range files {
		n, ok := unitIndex(filepath.Base(file))
		if !ok {
			continue
		}
		if _, ok := foreignUnitRoot(file, workersDir); ok {
			foreign[n] = true
			continue
		}
		own[n] = filepath.Base(file)
	}
	return own, foreign
}

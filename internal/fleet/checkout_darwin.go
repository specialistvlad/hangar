//go:build darwin

package fleet

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// unitWorkingDir reads a plist's WorkingDirectory — present in every agent
// hangar has written, this branch's and origin/main's alike — so startService
// and StartMetrics can tell a name already claimed by a different checkout of
// this repository sharing the account apart from this checkout's own, before
// overwriting or reloading it. A missing file, or one with no such key,
// reports ok=false: there is nothing to compare, so the caller treats the
// name as unclaimed.
func unitWorkingDir(path string) (string, bool) {
	return plistFile(path, "WorkingDirectory")
}

// unitBinArg reads a plist's first ProgramArguments entry. Only the
// exporter's plist needs this: its ProgramArguments[0] is this checkout's own
// binary, so a foreign one there is as telling as a foreign WorkingDirectory.
// A worker's ProgramArguments leads with /bin/sh on this branch, so callers
// checking a worker's ownership use unitWorkingDir alone.
func unitBinArg(path string) (string, bool) {
	return plistFile(path, "ProgramArguments")
}

func plistFile(path, key string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return plistValue(body, key)
}

// plistValue reads the first <string> that follows <key>key</key> in a plist
// body — or, when that key introduces an <array>, the first <string> inside
// it — the shape every value hangar's templates write. encoding/xml decodes
// entities the same way xmlText wrote them, and skips the DOCTYPE line
// without fetching it.
func plistValue(body []byte, key string) (string, bool) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	last := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "key":
			last = plistText(dec)
		case "string":
			if last == key {
				return plistText(dec), true
			}
		}
	}
}

// plistText reads the character data immediately inside the element whose
// start tag was just consumed — every <key> and <string> hangar writes holds
// its text with nothing else nested inside.
func plistText(dec *xml.Decoder) string {
	tok, err := dec.Token()
	if err != nil {
		return ""
	}
	cd, ok := tok.(xml.CharData)
	if !ok {
		return ""
	}
	return string(cd)
}

// foreignUnitRoot reports the checkout root recorded in the worker plist
// already at path when it is not, or is not beneath, workersDir: startService
// must never write over or reload such a plist, since doing so would
// silently take over a fleet that is not this checkout's. ok is false when
// there is nothing to protect — no plist yet, or one this checkout already
// wrote.
func foreignUnitRoot(path, workersDir string) (string, bool) {
	dir, ok := unitWorkingDir(path)
	if !ok || underDir(dir, workersDir) {
		return "", false
	}
	// dir is <root>/workers/wN; walking up twice recovers <root>.
	return filepath.Dir(filepath.Dir(dir)), true
}

// ownAgents globs dir for hangar's worker agents and splits their indexes
// into this checkout's own — a plist whose WorkingDirectory falls under
// workersDir — and another checkout's, before loadedServices ever asks
// launchd about them. An agent launchd still has loaded whose plist is gone
// carries no signal to split on, so it is in neither map, which is exactly
// what leaves it in scope for loadedServices as it always has been.
func ownAgents(dir, workersDir string) (own map[int]bool, foreign map[int]bool) {
	own, foreign = map[int]bool{}, map[int]bool{}
	plists, _ := filepath.Glob(filepath.Join(dir, "com.hangar.w*.plist"))
	for _, p := range plists {
		m := labelIndex.FindStringSubmatch(strings.TrimSuffix(filepath.Base(p), ".plist"))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if _, ok := foreignUnitRoot(p, workersDir); ok {
			foreign[n] = true
			continue
		}
		own[n] = true
	}
	return own, foreign
}

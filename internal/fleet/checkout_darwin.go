//go:build darwin

package fleet

import (
	"bytes"
	"encoding/xml"
	"os"
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

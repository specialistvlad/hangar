package fleet

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// checkUnitPaths refuses values that cannot survive being written into a
// worker's unit or plist: Root, TmpRoot and the name a worker registers
// under all land there. A systemd unit setting is one line of UTF-8, and
// systemd drops a setting that holds a control character or invalid UTF-8;
// a launchd plist tolerates the same bytes syntactically but xmlText's
// encoding/xml silently substitutes U+FFFD for them. Either way a worker
// built from such a value registers on GitHub and its agent never runs.
// Both platforms call this from preflight, before anything is registered.
func checkUnitPaths(values ...string) error {
	for _, v := range values {
		if !utf8.ValidString(v) || strings.IndexFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return fmt.Errorf("%q holds a control character or invalid UTF-8, which a unit or plist cannot carry — move hangar, or set RUNNER_NAME_PREFIX, to a plain value", v)
		}
	}
	return nil
}

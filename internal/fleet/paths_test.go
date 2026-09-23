package fleet

import "testing"

// A value a unit or plist cannot hold — a control character, or bytes that
// are not UTF-8 — is refused before anything is registered, rather than
// producing a unit that never loads or a plist launchd silently corrupts.
// Both platforms' preflight call this with Root, TmpRoot and NamePrefix,
// since all three land in a unit or plist.
func TestUnitPathsRejectWhatUnitsCannotHold(t *testing.T) {
	if err := checkUnitPaths("/srv/hangar", "/mnt/tmp $x", "box-w"); err != nil {
		t.Errorf("ordinary values were refused: %v", err)
	}
	for _, bad := range []string{"/srv/han\ngar", "/srv/\tx", "/srv/\x7f", "/srv/caf\xff"} {
		if err := checkUnitPaths(bad, "", ""); err == nil {
			t.Errorf("checkUnitPaths(%q) should fail", bad)
		}
		// The same bad value in the worker name position, alongside ordinary
		// paths, must fail the whole check — NamePrefix gets no lighter a
		// pass than Root or TmpRoot.
		if err := checkUnitPaths("/srv/hangar", "", bad); err == nil {
			t.Errorf("a bad NamePrefix %q should fail alongside ordinary paths", bad)
		}
	}
}

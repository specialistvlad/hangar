package fleet

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
)

// labelIndex matches the launchd label config.Label builds. Parsing it back is
// what lets a kill reach an agent whose worker directory has already gone.
var labelIndex = regexp.MustCompile(`^com\.hangar\.w(\d+)$`)

// killTargets is every worker index the machine still holds a trace of: a
// directory under workers/, a loaded launchd agent, or both. Scale works from
// the directories alone because it can trust its own bookkeeping; a kill is
// reached for when that bookkeeping is already suspect, so it takes the union.
func killTargets(disk []Worker, loaded map[string]int) []int {
	seen := map[int]bool{}
	for _, w := range disk {
		seen[w.Index] = true
	}
	for label := range loaded {
		if m := labelIndex.FindStringSubmatch(label); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				seen[n] = true
			}
		}
	}

	var out []int
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Kill tears the whole fleet off this Mac without talking to GitHub: it boots
// out each launchd agent, removes its plist and deletes the worker directory.
//
// It is the escape hatch for the case Scale cannot serve. Scale unregisters
// every worker server-side first, which needs a removal token, so an expired or
// wrong-scoped GH_TOKEN leaves a running fleet with no way to stop it. Kill
// gives up the server-side half to keep the local half always available: the
// registrations survive, listed in the org as offline, until a token exists to
// remove them or an operator deletes them by hand.
//
// It returns the number of workers torn down.
func (f *Fleet) Kill(progress func(string)) int {
	targets := killTargets(f.List(), launchctlList())
	for _, n := range targets {
		progress(fmt.Sprintf("killing %s", f.cfg.WorkerName(n)))
		f.stopService(n)
		if err := os.RemoveAll(f.cfg.WorkerDir(n)); err != nil {
			progress(fmt.Sprintf("  %s left on disk: %v", f.cfg.WorkerDir(n), err))
		}
	}
	return len(targets)
}

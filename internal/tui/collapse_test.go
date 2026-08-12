package tui

import "testing"

func TestProgressPrefix(t *testing.T) {
	// git and BuildKit counter lines should collapse onto each other.
	for _, l := range []string{
		"Updating files:  80% (7866/9832)",
		"Receiving objects:  50% (100/200)",
		"Resolving deltas:   9% (1/11)",
	} {
		if progressPrefix(l) == "" {
			t.Errorf("%q should be treated as progress", l)
		}
	}
	// Distinct BuildKit steps must NOT collapse — their shared prefix is "#".
	for _, l := range []string{"#30 DONE 0.0s", "#31 [4/9] RUN npm ci", "12345", ""} {
		if got := progressPrefix(l); got != "" {
			t.Errorf("%q should not be progress, got prefix %q", l, got)
		}
	}
	if progressPrefix("Updating files:  80%") != progressPrefix("Updating files:  99%") {
		t.Error("successive counter lines must share a prefix")
	}
}

func TestCollapseInto(t *testing.T) {
	lines := []logLine{{1, "Updating files:  10% (1/10)"}, {2, "some other output"}}

	if !collapseInto(lines, 1, "Updating files:  20% (2/10)") {
		t.Fatal("should have collapsed onto w1's earlier progress line")
	}
	if lines[0].text != "Updating files:  20% (2/10)" {
		t.Errorf("in-place update failed: %q", lines[0].text)
	}
	// A different worker's identical progress must not overwrite w1's.
	if collapseInto(lines, 3, "Updating files:  30% (3/10)") {
		t.Error("collapsed across workers")
	}
	if collapseInto(lines, 1, "#30 DONE 0.0s") {
		t.Error("non-progress line should append, not collapse")
	}
}

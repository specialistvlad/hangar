package config

import (
	"fmt"
	"strings"
)

// cleanSharePath validates one SHARE_PATHS entry. Entries name a path inside
// the real home, so an absolute path or a .. escape is rejected rather than
// silently linking a worker at something outside it.
func cleanSharePath(raw string) (string, error) {
	rel := strings.TrimPrefix(strings.TrimSpace(raw), "~/")
	if rel == "" || strings.HasPrefix(rel, "/") || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") ||
		strings.HasSuffix(rel, "/..") {
		return "", fmt.Errorf("SHARE_PATHS entry %q must be a plain path relative to your home directory", raw)
	}
	return rel, nil
}

package tui

import (
	"strings"
	"unicode"
)

// collapseWindow is how far back a progress line looks for its predecessor.
// With several workers interleaving, consecutive lines from one worker are
// rarely adjacent in the buffer, but they stay close.
const collapseWindow = 24

// progressPrefix returns the stable part of a counter line — everything before
// its first digit — or "" if the line is not progress-shaped.
//
// git and BuildKit emit hundreds of near-identical counter lines
// ("Updating files:  80% (7866/9832)", then 81%, then 82%). Rendering every one
// buries the output that matters, so lines sharing a prefix overwrite each
// other and only the newest is shown.
//
// The prefix must be at least four characters and contain a letter. That is
// what keeps genuinely distinct BuildKit steps apart: "#30 DONE 0.0s" and
// "#31 DONE 0.1s" share only "#", so they are left alone.
func progressPrefix(line string) string {
	i := strings.IndexFunc(line, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 4 {
		return ""
	}
	prefix := line[:i]
	if !strings.ContainsFunc(prefix, unicode.IsLetter) {
		return ""
	}
	return prefix
}

// collapseInto replaces an earlier progress line from the same worker sharing
// this line's prefix, returning true when it did. A false result means the line
// is new output and should be appended.
func collapseInto(lines []logLine, worker int, text string) bool {
	prefix := progressPrefix(text)
	if prefix == "" {
		return false
	}
	start := len(lines) - collapseWindow
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		if lines[i].worker == worker && progressPrefix(lines[i].text) == prefix {
			lines[i].text = text
			return true
		}
	}
	return false
}

//go:build darwin

package metrics

import (
	"testing"
	"time"
)

func TestParseCPUTime(t *testing.T) {
	cases := map[string]time.Duration{
		"0:00.00":    0,
		"12:34.56":   754560 * time.Millisecond, // MM:SS.ss
		"1:02:03":    3723 * time.Second,        // HH:MM:SS
		"2-03:04:05": (2*86400 + 3*3600 + 4*60 + 5) * time.Second,
	}
	for in, want := range cases {
		if got := parseCPUTime(in); got != want {
			t.Errorf("parseCPUTime(%q) = %v, want %v", in, got, want)
		}
	}
}

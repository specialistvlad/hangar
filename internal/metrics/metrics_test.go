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

func TestSparkline(t *testing.T) {
	if got := Sparkline([]float64{0, 50, 100}, 3, 100); got != "▁▄█" {
		t.Errorf("got %q, want ▁▄█", got)
	}
	// Short histories right-align so the newest sample sits at the edge.
	if got := Sparkline([]float64{100}, 3, 100); got != "  █" {
		t.Errorf("got %q, want '  █'", got)
	}
	// Values above the ceiling clamp instead of panicking on an index.
	if got := Sparkline([]float64{500}, 1, 100); got != "█" {
		t.Errorf("clamp: got %q, want █", got)
	}
	// An all-zero window with autoscale must not divide by zero.
	if got := Sparkline([]float64{0, 0}, 2, 0); got != "▁▁" {
		t.Errorf("autoscale zero: got %q, want ▁▁", got)
	}
	if got := Sparkline(nil, 0, 100); got != "" {
		t.Errorf("zero width: got %q", got)
	}
}

func TestRingKeepsNewest(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.Push(float64(i))
	}
	got := r.Values()
	if len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("got %v, want [3 4 5]", got)
	}
}

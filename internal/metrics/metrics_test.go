package metrics

import (
	"testing"
)

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

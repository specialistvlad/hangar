package metrics

import "math"

// Ring is a fixed-size history of samples, oldest first once full.
type Ring struct {
	vals []float64
	size int
}

func NewRing(size int) *Ring { return &Ring{size: size} }

func (r *Ring) Push(v float64) {
	r.vals = append(r.vals, v)
	if len(r.vals) > r.size {
		r.vals = r.vals[len(r.vals)-r.size:]
	}
}

// Values copies the history out. The copy is the point: the sampler keeps
// pushing from its own goroutine while a reader is drawing the previous frame.
func (r *Ring) Values() []float64 { return append([]float64(nil), r.vals...) }

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders the most recent width samples. A fixed max keeps the graph
// comparable between frames; pass 0 to autoscale to the window's own peak,
// which is what you want when the natural ceiling is unknown.
func Sparkline(vals []float64, width int, max float64) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}

	if max <= 0 {
		for _, v := range vals {
			max = math.Max(max, v)
		}
	}
	if max <= 0 {
		max = 1
	}

	out := make([]rune, 0, width)
	for i := 0; i < width-len(vals); i++ {
		out = append(out, ' ') // right-align so the newest sample is at the edge
	}
	for _, v := range vals {
		frac := math.Max(0, math.Min(1, v/max))
		idx := int(frac * float64(len(sparkChars)-1))
		out = append(out, sparkChars[idx])
	}
	return string(out)
}

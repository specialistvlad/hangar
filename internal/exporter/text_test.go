package exporter

import (
	"strings"
	"testing"
)

// The histogram backs hangar_job_duration_seconds: a duration past every
// finite bucket must still land in +Inf and the total, and one exactly on a
// bound must land in that bucket rather than the next one up.
func TestHistogramBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bounds  []float64
		observe []float64
		sum     string
		want    []string
	}{
		{
			name:    "a duration past every bucket still counts",
			bounds:  []float64{60, 120},
			observe: []float64{90, 8000},
			sum:     `x_sum{result="r"} 8090`,
			want: []string{
				`x_bucket{result="r",le="60"} 0`,
				`x_bucket{result="r",le="120"} 1`,
				`x_bucket{result="r",le="+Inf"} 2`,
				`x_count{result="r"} 2`,
			},
		},
		{
			name:    "a duration exactly on a bound lands there, not the next bucket up",
			bounds:  []float64{60, 120, 300},
			observe: []float64{120},
			sum:     `x_sum{result="r"} 120`,
			want: []string{
				`x_bucket{result="r",le="60"} 0`,
				`x_bucket{result="r",le="120"} 1`,
				`x_bucket{result="r",le="300"} 1`,
				`x_bucket{result="r",le="+Inf"} 1`,
				`x_count{result="r"} 1`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHistogram(tc.bounds, "r")
			for _, v := range tc.observe {
				h.observe("r", v)
			}
			var b strings.Builder
			h.write(&b, "x", "result")
			mustHave(t, b.String(), append(tc.want, tc.sum)...)
		})
	}
}

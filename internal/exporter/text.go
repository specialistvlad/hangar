package exporter

import (
	"io"
	"sort"
	"strconv"
	"strings"
)

// The Prometheus text exposition format, written by hand: a few families of
// gauges, counters and one histogram need no client library, and hangar adds
// no dependency that a page of stdlib covers.

// label is one name="value" pair; order is kept as given.
type label struct{ name, value string }

// family writes a metric family's HELP and TYPE header.
func family(w io.Writer, name, typ, help string) {
	_, _ = io.WriteString(w, "# HELP "+name+" "+escapeHelp(help)+"\n# TYPE "+name+" "+typ+"\n")
}

// sample writes one sample line.
func sample(w io.Writer, name string, labels []label, v float64) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.name)
			b.WriteString(`="`)
			b.WriteString(escapeValue(l.value))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(formatValue(v))
	b.WriteByte('\n')
	_, _ = io.WriteString(w, b.String())
}

// formatValue prints integers without an exponent, so a unix timestamp reads
// as one; everything else in Go's shortest form, which Prometheus parses.
func formatValue(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var (
	valueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

func escapeValue(s string) string { return valueEscaper.Replace(s) }
func escapeHelp(s string) string  { return helpEscaper.Replace(s) }

// histogram is a fixed-bucket histogram keyed by one label value.
type histogram struct {
	bounds []float64
	series map[string]*histSeries
}

type histSeries struct {
	counts []uint64 // per bucket, not cumulative
	sum    float64
	count  uint64
}

func newHistogram(bounds []float64, keys ...string) *histogram {
	h := &histogram{bounds: bounds, series: map[string]*histSeries{}}
	for _, k := range keys {
		h.series[k] = &histSeries{counts: make([]uint64, len(bounds))}
	}
	return h
}

func (h *histogram) observe(key string, v float64) {
	s := h.series[key]
	if s == nil {
		s = &histSeries{counts: make([]uint64, len(h.bounds))}
		h.series[key] = s
	}
	for i, b := range h.bounds {
		if v <= b {
			s.counts[i]++
			break
		}
	}
	s.sum += v
	s.count++
}

// write emits the histogram's bucket, sum and count samples, cumulative as
// the format requires, with the +Inf bucket equal to the count.
func (h *histogram) write(w io.Writer, name, labelName string) {
	keys := make([]string, 0, len(h.series))
	for k := range h.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := h.series[k]
		var cum uint64
		for i, b := range h.bounds {
			cum += s.counts[i]
			sample(w, name+"_bucket", []label{{labelName, k}, {"le", formatValue(b)}}, float64(cum))
		}
		sample(w, name+"_bucket", []label{{labelName, k}, {"le", "+Inf"}}, float64(s.count))
		sample(w, name+"_sum", []label{{labelName, k}}, s.sum)
		sample(w, name+"_count", []label{{labelName, k}}, float64(s.count))
	}
}

//go:build linux

package metrics

import (
	"os"
	"strconv"
	"strings"
)

func loadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	return parseLoadavg(string(b))
}

func parseLoadavg(body string) float64 {
	f := strings.Fields(body)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// memUsed is MemTotal minus MemAvailable — the kernel's own estimate of what
// could be handed out without swapping. Counting page cache as used would make
// every build host look permanently out of memory.
func memUsed() uint64 {
	total, avail := readMeminfo()
	if avail > total {
		return 0
	}
	return total - avail
}

func memTotal() uint64 {
	total, _ := readMeminfo()
	return total
}

func readMeminfo() (total, avail uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	return parseMeminfo(string(b))
}

// parseMeminfo returns MemTotal and MemAvailable in bytes; the file states them
// in kB.
func parseMeminfo(body string) (total, avail uint64) {
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}

package logs

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// tailer follows whichever file matching glob was modified most recently, so
// job and step rotation is handled without tracking the runner's file naming.
type tailer struct {
	glob string
	path string
	off  int64
}

// byAge lists the matching files, newest first.
func (t *tailer) byAge() []string {
	matches, _ := filepath.Glob(t.glob)
	type entry struct {
		path string
		mod  time.Time
	}
	var es []entry
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			es = append(es, entry{m, fi.ModTime()})
		}
	}
	sort.Slice(es, func(i, j int) bool { return es[i].mod.After(es[j].mod) })
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.path
	}
	return out
}

func (t *tailer) newest() string {
	if all := t.byAge(); len(all) > 0 {
		return all[0]
	}
	return ""
}

func (t *tailer) seekEnd() {
	t.path = t.newest()
	if t.path == "" {
		return
	}
	if fi, err := os.Stat(t.path); err == nil {
		t.off = fi.Size()
	}
}

// read returns the complete lines written since the last call. When a newer
// file appears, the old one is read to its end first — the runner may have
// logged a job's end there just before rotating — and the new one from its
// start.
func (t *tailer) read() []string {
	newest := t.newest()
	if newest == "" {
		return nil
	}
	var lines []string
	if newest != t.path {
		if t.path != "" {
			lines, _ = readLines(t.path, t.off)
		}
		t.path, t.off = newest, 0
	}
	more, off := readLines(t.path, t.off)
	t.off = off
	return append(lines, more...)
}

// readLines reads the complete lines of path from off on and returns the
// offset just past the last one. A line still being written is left for the
// next read rather than returned in halves.
func readLines(path string, off int64) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, off
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, off
	}
	if fi.Size() < off { // truncated underneath us
		off = 0
	}
	if fi.Size() == off {
		return nil, off
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, off
	}
	buf, err := io.ReadAll(io.LimitReader(f, fi.Size()-off))
	if err != nil {
		return nil, off
	}
	end := strings.LastIndexByte(string(buf), '\n')
	if end < 0 {
		return nil, off
	}
	var lines []string
	for _, l := range strings.Split(string(buf[:end]), "\n") {
		// The runner writes these files with a UTF-8 BOM.
		lines = append(lines, strings.TrimSuffix(strings.TrimPrefix(l, "\ufeff"), "\r"))
	}
	return lines, off + int64(end) + 1
}

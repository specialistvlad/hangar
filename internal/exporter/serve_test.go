package exporter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
)

// The whole loop against a real directory: a job start written to a worker's
// runner log shows up as busy, a failed listing leaves the workers as they
// were, a worker that leaves the listing takes its series with it, and the
// server answers only /metrics and / and stops when told.
func TestServeLoop(t *testing.T) {
	root := t.TempDir()
	dir := func(n int) string { return filepath.Join(root, "workers", "w"+string(rune('0'+n))) }
	if err := os.MkdirAll(filepath.Join(dir(1), "_diag"), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	listing := []fleet.Worker{{Index: 1, Name: "box-w1", Dir: dir(1), Registered: true, Running: true}}
	var listErr error
	list := func() ([]fleet.Worker, error) {
		mu.Lock()
		defer mu.Unlock()
		return listing, listErr
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, NewCollector("v", "c"), list, dir, 20*time.Millisecond) }()

	// scrape returns the body and its Content-Type.
	scrape := func() (string, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/metrics", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp.Header.Get("Content-Type")
	}
	eventually := func(what string, ok func(string) bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if body, _ := scrape(); ok(body) {
				return
			}
		}
		body, _ := scrape()
		t.Fatalf("never saw %s:\n%s", what, body)
	}

	body, ct := scrape()
	if !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(body, `hangar_worker_up{worker="w1",runner="box-w1"} 1`) {
		t.Fatalf("worker not listed:\n%s", body)
	}

	line := "[2026-09-23 03:00:00Z INFO Terminal] WRITE LINE: 2026-09-23 03:00:00Z: Running job: a / build\n"
	if err := os.WriteFile(filepath.Join(dir(1), "_diag", "Runner_1.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	eventually("the job start", func(b string) bool {
		return strings.Contains(b, `hangar_worker_busy{worker="w1",runner="box-w1"} 1`)
	})

	mu.Lock()
	listErr = errors.New("systemctl: timeout")
	listing = []fleet.Worker{{Index: 1, Name: "box-w1", Dir: dir(1), Registered: true, Running: false}}
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if body, _ := scrape(); !strings.Contains(body, `hangar_worker_up{worker="w1",runner="box-w1"} 1`) {
		t.Errorf("a failed listing must leave the worker as it was:\n%s", body)
	}

	mu.Lock()
	listErr, listing = nil, nil
	mu.Unlock()
	eventually("the worker's series gone", func(b string) bool { return !strings.Contains(b, `worker="w1"`) })

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/nope", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("/nope: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v on shutdown, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

// A listener restart, and a worker the supervisor no longer runs, both end the
// job without counting it.
func TestIdleWithoutCounting(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(c *Collector)
	}{
		{"listener restarted", func(c *Collector) {
			c.Apply(logsListening(1))
		}},
		{"worker stopped", func(c *Collector) {
			c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: false}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector("v", "c")
			c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
			c.Apply(logsStart(1, time.Unix(1000, 0)))
			tc.stop(c)
			out := render(c)
			mustHave(t, out, `hangar_worker_busy{worker="w1",runner="w"} 0`,
				`hangar_jobs_total{worker="w1",runner="w",result="failed"} 0`)
			mustNotHave(t, out, "hangar_worker_job_info{", "hangar_worker_job_start_timestamp_seconds{")
		})
	}
}

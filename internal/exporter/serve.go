package exporter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/logs"
)

// pollInterval is how often the fleet is listed and pending job details are
// looked up. Job starts and ends arrive faster, straight from the log watchers.
const pollInterval = 5 * time.Second

const contentType = "text/plain; version=0.0.4; charset=utf-8"

// Serve answers /metrics on addr until ctx ends. It lists the fleet, follows
// every worker's runner log for job starts and ends, and looks up each running
// job's repository and run in the log the runner's worker process writes.
func Serve(ctx context.Context, f *fleet.Fleet, addr, version, commit string) error {
	c := NewCollector(version, commit)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler(c), ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	events := make(chan logs.Event, 64)
	go func() {
		for {
			select {
			case e := <-events:
				c.Apply(e)
			case <-ctx.Done():
				return
			}
		}
	}()

	watching := map[int]context.CancelFunc{}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		ws := f.List()
		c.SetFleet(ws)
		follow(ctx, ws, watching, events)
		for n, start := range c.PendingInfo() {
			info, ok := logs.FindJobInfo(f.Config().WorkerDir(n), start)
			c.SetJobInfo(n, start, info, ok)
		}
		select {
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shut)
		case err := <-served:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-tick.C:
		}
	}
}

// follow starts a job watcher for every worker that has none and stops the
// watchers of workers that are gone.
func follow(ctx context.Context, ws []fleet.Worker, watching map[int]context.CancelFunc, events chan<- logs.Event) {
	seen := map[int]bool{}
	for _, w := range ws {
		seen[w.Index] = true
		if watching[w.Index] != nil {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		watching[w.Index] = cancel
		go logs.WatchJobs(wctx, w.Index, w.Dir, events)
	}
	for n, cancel := range watching {
		if !seen[n] {
			cancel()
			delete(watching, n)
		}
	}
}

func handler(c *Collector) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		c.Render(w)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "hangar exporter — metrics at /metrics\n")
	})
	return mux
}

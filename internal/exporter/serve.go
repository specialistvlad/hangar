package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	// Said only once the port is ours: a service that cannot bind must not log
	// that it is serving while it restarts.
	fmt.Printf("  serving metrics on http://%s/metrics\n", ln.Addr())
	return serve(ctx, ln, NewCollector(version, commit), f.ListChecked, f.Config().WorkerDir, pollInterval)
}

// serve is Serve on a listener that is already open and a fleet given as
// functions, so a test can run it on a free port against a directory it made.
func serve(ctx context.Context, ln net.Listener, c *Collector, list func() ([]fleet.Worker, error),
	workerDir func(int) string, every time.Duration) error {
	srv := &http.Server{
		Handler: handler(c),
		// A scrape completes in well under a second; these bound how long a
		// client that never finishes reading or writing can tie up a
		// connection, so a stuck /metrics client cannot freeze the poll loop
		// through Render's lock (see Render's own comment).
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
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
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		// A listing the supervisor could not answer says nothing about which
		// workers are up; the previous state stands until the next poll. The
		// outcome is still recorded either way, so a supervisor that keeps
		// failing shows up in the metrics instead of only freezing the state.
		ws, err := list()
		c.ObserveListing(err)
		if err == nil {
			c.SetFleet(ws)
		}
		follow(ctx, ws, watching, events)
		for n, start := range c.PendingInfo() {
			info, ok := logs.FindJobInfo(workerDir(n), start)
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

// WaitServing polls addr until it answers /metrics with this build's own
// build_info line. Any 200 is not enough: another program may hold the port,
// or an exporter from before a rebuild may still be answering.
func WaitServing(ctx context.Context, addr, version, commit string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/metrics"
	want := fmt.Sprintf(`hangar_build_info{version="%s",commit="%s"} 1`, escapeValue(version), escapeValue(commit))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		if body, err := get(ctx, url); err != nil {
			last = err
		} else if strings.Contains(body, want) {
			return nil
		} else {
			last = fmt.Errorf("%s answers, but not as this build of hangar", url)
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

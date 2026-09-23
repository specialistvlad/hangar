package exporter

import (
	"sync"
	"testing"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
)

// blockingWriter reports the first Write it receives on started, then blocks
// until release is closed — standing in for a scrape client that reads the
// response slowly or not at all.
type blockingWriter struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

// Render builds the exposition under c.mu and releases it before writing to
// the caller's writer, so Apply and SetFleet proceed even while a slow
// scrape client is still being written to.
func TestRenderDoesNotBlockApplyOrSetFleet(t *testing.T) {
	c := NewCollector("v", "c")
	c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})

	w := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	go c.Render(w)
	select {
	case <-w.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Render never reached the writer")
	}

	done := make(chan struct{})
	go func() {
		c.Apply(logsStart(1, time.Unix(1000, 0)))
		c.SetFleet([]fleet.Worker{{Index: 1, Name: "w", Registered: true, Running: true}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Apply/SetFleet blocked on a slow Render writer")
	}
	close(w.release)
}

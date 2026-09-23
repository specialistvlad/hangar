package tui

import (
	"testing"
	"time"

	"github.com/specialistvlad/hangar/internal/logs"
)

// A watcher attaching mid-job replays the job's real start time; the elapsed
// time on screen must count from there, not from when the dashboard opened.
func TestApplyRecoveredJobStartUsesEventTime(t *testing.T) {
	m := model(t, 1)
	started := time.Now().Add(-45 * time.Minute)

	m.apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: "build", At: started, Recovered: true})

	if got := m.workers[1].Since; !got.Equal(started) {
		t.Errorf("Since = %v, want the recovered start time %v", got, started)
	}
}

// A live start carries no At — the runner log line is seen as it is written —
// so the worker's clock still starts now.
func TestApplyLiveJobStartUsesNow(t *testing.T) {
	m := model(t, 1)
	before := time.Now()

	m.apply(logs.Event{Worker: 1, Kind: logs.KindJobStart, Text: "build"})

	since := m.workers[1].Since
	if since.Before(before) || since.After(time.Now()) {
		t.Errorf("Since = %v, want a time around now (%v)", since, before)
	}
}

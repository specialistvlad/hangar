package fleet

import (
	"reflect"
	"testing"
)

// A force-kill exists for the case where GitHub cannot be reached at all, so it
// must not depend on the two sides of the fleet agreeing. A worker directory
// with no agent, and an agent whose directory was already deleted, are both
// leftovers a scale-down would have cleaned up, and both have to be torn down.
func TestKillTargetsCoverDisksAndAgents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		disk   []Worker
		loaded map[string]int
		want   []int
	}{
		{
			name:   "matched pairs are targeted once each",
			disk:   []Worker{{Index: 1}, {Index: 2}},
			loaded: map[string]int{"com.hangar.w1": 100, "com.hangar.w2": 101},
			want:   []int{1, 2},
		},
		{
			name:   "an agent whose directory is gone is still booted out",
			disk:   nil,
			loaded: map[string]int{"com.hangar.w3": 102},
			want:   []int{3},
		},
		{
			name:   "a directory with no agent is still deleted",
			disk:   []Worker{{Index: 4}},
			loaded: nil,
			want:   []int{4},
		},
		{
			name:   "targets come back in ascending order",
			disk:   []Worker{{Index: 10}, {Index: 2}},
			loaded: map[string]int{"com.hangar.w1": 0},
			want:   []int{1, 2, 10},
		},
		{
			name:   "jobs hangar does not own are left alone",
			disk:   nil,
			loaded: map[string]int{"com.apple.Finder": 1, "com.hangar.watchdog": 2},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := killTargets(tc.disk, tc.loaded)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("killTargets = %v, want %v", got, tc.want)
			}
		})
	}
}

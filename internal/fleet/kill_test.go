package fleet

import (
	"reflect"
	"testing"
)

// A force-kill exists for the case where GitHub cannot be reached at all, so it
// must not depend on the two sides of the fleet agreeing. A worker directory
// with no service, and a service whose directory was already deleted, are both
// leftovers a scale-down would have cleaned up, and both have to be torn down.
// Which services hangar owns is decided by each platform's parser, tested
// alongside it.
func TestKillTargetsCoverDisksAndServices(t *testing.T) {
	for _, tc := range []struct {
		name   string
		disk   []Worker
		loaded map[int]int
		want   []int
	}{
		{
			name:   "matched pairs are targeted once each",
			disk:   []Worker{{Index: 1}, {Index: 2}},
			loaded: map[int]int{1: 100, 2: 101},
			want:   []int{1, 2},
		},
		{
			name:   "a service whose directory is gone is still stopped",
			disk:   nil,
			loaded: map[int]int{3: 102},
			want:   []int{3},
		},
		{
			name:   "a directory with no service is still deleted",
			disk:   []Worker{{Index: 4}},
			loaded: nil,
			want:   []int{4},
		},
		{
			name:   "targets come back in ascending order",
			disk:   []Worker{{Index: 10}, {Index: 2}},
			loaded: map[int]int{1: 0},
			want:   []int{1, 2, 10},
		},
		{
			name:   "nothing on either side means nothing to kill",
			disk:   nil,
			loaded: map[int]int{},
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

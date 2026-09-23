package fleet

import "testing"

func TestUnderDir(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/srv/a/workers/w1", "/srv/a/workers", true},
		{"/srv/a/workers", "/srv/a/workers", true},
		{"/srv/a/workers/", "/srv/a/workers", true},
		{"/srv/b/workers/w1", "/srv/a/workers", false},
		{"/srv/a/workers2/w1", "/srv/a/workers", false}, // a sibling with the dir as a prefix, not inside it
		{"/srv/a", "/srv/a/workers", false},
	} {
		if got := underDir(tc.path, tc.dir); got != tc.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

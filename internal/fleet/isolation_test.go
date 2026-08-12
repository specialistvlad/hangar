package fleet

import (
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// The worker .env is the entire isolation mechanism, so its contents are pinned.
// A missing HOME here silently un-isolates every tool at once.
func TestWorkerEnvIsolatesHome(t *testing.T) {
	f := New(&config.Config{Root: "/r", DockerHost: "unix:///sock"})
	env := f.workerEnv(3)

	for _, want := range []string{
		"HOME=/r/workers/w3/home",
		"TMPDIR=/r/workers/w3/tmp",
		"DOCKER_HOST=unix:///sock",
		"HANGAR_WORKER=3",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("worker .env missing %q\ngot:\n%s", want, env)
		}
	}
}

// DOCKER_HOST is the one setting a private HOME cannot supply for itself: the
// fresh home has no docker contexts. It is omitted rather than written empty.
func TestWorkerEnvOmitsEmptyDockerHost(t *testing.T) {
	f := New(&config.Config{Root: "/r"})
	if strings.Contains(f.workerEnv(1), "DOCKER_HOST") {
		t.Error("DOCKER_HOST should be omitted when unset, not written empty")
	}
}

func TestCleanSharePath(t *testing.T) {
	ok := map[string]string{
		".npm":              ".npm",
		"~/.npm":            ".npm",
		"  .cache/go-build": ".cache/go-build",
		".config/gh":        ".config/gh",
	}
	for in, want := range ok {
		got, err := cleanSharePath(in)
		if err != nil {
			t.Errorf("cleanSharePath(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("cleanSharePath(%q) = %q, want %q", in, got, want)
		}
	}

	// A share entry names something inside the real home. Anything that escapes
	// it would quietly link a worker at an arbitrary path.
	for _, bad := range []string{"", "   ", "/etc/passwd", "..", "../..", "../.ssh", "a/../../b", "a/.."} {
		if got, err := cleanSharePath(bad); err == nil {
			t.Errorf("cleanSharePath(%q) should have failed, got %q", bad, got)
		}
	}
}

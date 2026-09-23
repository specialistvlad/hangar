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

// WORKER_TMP_ROOT moves only TMPDIR. HOME stays inside the worker, because the
// root is typically a RAM-backed tmpfs that a reboot wipes — and a home holds
// the runner's credentials and caches that must survive one.
func TestWorkerEnvHonorsTmpRoot(t *testing.T) {
	f := New(&config.Config{Root: "/r", TmpRoot: "/mnt/fast"})
	env := f.workerEnv(4)
	if !strings.Contains(env, "TMPDIR=/mnt/fast/w4\n") {
		t.Errorf("TMPDIR should move under WORKER_TMP_ROOT, got:\n%s", env)
	}
	if !strings.Contains(env, "HOME=/r/workers/w4/home\n") {
		t.Errorf("HOME must stay inside the worker directory, got:\n%s", env)
	}
}

func TestDefaultRunnerPath(t *testing.T) {
	mac := defaultRunnerPath("darwin", "/Users/me")
	for _, want := range []string{"/opt/homebrew/bin", "/usr/bin", "/Users/me/.docker/bin"} {
		if !strings.Contains(mac, want) {
			t.Errorf("darwin PATH missing %s: %s", want, mac)
		}
	}
	linux := defaultRunnerPath("linux", "/home/me")
	for _, want := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(linux, want) {
			t.Errorf("linux PATH missing %s: %s", want, linux)
		}
	}
	// Homebrew and Docker Desktop do not exist on Linux, and the real home must
	// not leak into a worker's PATH there.
	for _, bad := range []string{"homebrew", "/home/me"} {
		if strings.Contains(linux, bad) {
			t.Errorf("linux PATH should not contain %s: %s", bad, linux)
		}
	}
}

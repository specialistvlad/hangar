package config

import (
	"os"
	"path/filepath"
	"testing"
)

// DOCKER_HOST=none declares a fleet whose jobs get no docker daemon: nothing is
// detected (a socket the account cannot open would otherwise be found and checked),
// and the workers are handed no DOCKER_HOST.
func TestLoadDockerHostNone(t *testing.T) {
	load := func(v string) *Config {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DOCKER_HOST="+v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := load("none"); c.DockerHost != "" {
		t.Errorf("DOCKER_HOST=none: got %q, want no daemon", c.DockerHost)
	}
	if c := load("unix:///x/docker.sock"); c.DockerHost != "unix:///x/docker.sock" {
		t.Errorf("an explicit socket: got %q", c.DockerHost)
	}
}

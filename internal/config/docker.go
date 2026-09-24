package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// detectDockerHost finds the daemon socket. A worker with a private HOME has no
// docker contexts to resolve the daemon from, and Docker Desktop does not
// create /var/run/docker.sock, so an unset DOCKER_HOST would break every docker
// step with a confusing "cannot connect" rather than a clear error. Docker
// Desktop's socket wins when it exists, then rootless dockerd's well-known
// per-user socket, then the system socket a rootful Linux daemon listens on.
func detectDockerHost() string {
	for _, sock := range dockerHostCandidates(os.UserHomeDir, os.Getuid) {
		if _, err := os.Stat(sock); err == nil {
			return "unix://" + sock
		}
	}
	return ""
}

// dockerHostCandidates lists the sockets detectDockerHost probes, in order.
// It takes its inputs as functions so the ordering can be tested without
// touching the real home directory or process uid.
func dockerHostCandidates(userHomeDir func() (string, error), getuid func() int) []string {
	var candidates []string
	if home, err := userHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	candidates = append(candidates, filepath.Join("/run", "user", strconv.Itoa(getuid()), "docker.sock"))
	candidates = append(candidates, "/var/run/docker.sock")
	return candidates
}

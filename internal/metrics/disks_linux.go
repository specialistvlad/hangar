//go:build linux

package metrics

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// platformDisks adds where docker keeps its data: its data root, asked of the
// daemon itself, and — when docker uses the containerd image store — the
// containerd root, which then holds every image and build snapshot. With the
// classic store containerd's root holds only task state, so watching it would
// flag a small root disk no build writes to. The lookup is final once the
// daemon has answered.
func platformDisks(dockerHost string) ([]Disk, bool) {
	var env []string
	if dockerHost != "" {
		env = []string{"DOCKER_HOST=" + dockerHost}
	}
	out, err := dockerInfo(env)
	root, snapshotter := parseDockerInfo(out)
	final := err == nil && root != ""
	if !final {
		root = "/var/lib/docker"
	}
	disks := []Disk{{Label: "docker", Path: root}}
	if snapshotter {
		cfg, _ := os.ReadFile("/etc/containerd/config.toml")
		disks = append(disks, Disk{Label: "containerd", Path: containerdRoot(string(cfg))})
	}
	return disks, final
}

func dockerInfo(env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.DockerRootDir}}|{{json .DriverStatus}}")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	return string(out), err
}

// parseDockerInfo splits the data root from the driver status and reports
// whether images live in containerd's snapshotter.
func parseDockerInfo(out string) (root string, snapshotter bool) {
	root, status, _ := strings.Cut(strings.TrimSpace(out), "|")
	return root, strings.Contains(status, `"io.containerd.snapshotter.v1"`)
}

// containerdRoot reads the top-level root setting of containerd's config,
// which comes before any [section]; unset, containerd uses its default.
func containerdRoot(toml string) string {
	for _, line := range strings.Split(toml, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "root" {
			continue
		}
		if v = tomlString(v); v != "" {
			return v
		}
	}
	return "/var/lib/containerd"
}

// tomlString reads a TOML value that may be quoted and may be followed by an
// inline comment: the text inside the quotes, or up to the '#'.
func tomlString(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : end+1]
		}
		return ""
	}
	v, _, _ = strings.Cut(v, "#")
	return strings.TrimSpace(v)
}

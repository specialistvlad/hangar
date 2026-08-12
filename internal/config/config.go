// Package config loads hangar's settings from the repo-local .env file.
//
// Nothing here consults the ambient environment. hangar must behave identically
// no matter which shell launches it, so a value that is not in .env does not
// exist. The one exception is HANGAR_ROOT, which the Makefile sets to tell the
// binary where the repo is — that is location, not configuration.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxWorkers = 32

type Config struct {
	Root       string // repo root; everything hangar owns lives under it
	Token      string // GH_TOKEN — fine-grained, org "Self-hosted runners" write
	Org        string // GH_ORG
	Group      string // RUNNER_GROUP — runner group to register into
	Labels     string // RUNNER_LABELS — extra labels, may be empty
	NamePrefix string // RUNNER_NAME_PREFIX — worker N registers as <prefix>N
	DockerHost string // DOCKER_HOST handed to every worker
	RunnerPath string // RUNNER_PATH — PATH a worker's jobs run with
	Lang       string // RUNNER_LANG — locale for jobs; builds misbehave without one
	// SharePaths are home-relative paths symlinked from the real home into each
	// worker's private one. Isolation is the default; this is the opt-out, and
	// it stays short because sharing is the exception.
	SharePaths []string // SHARE_PATHS
}

func (c *Config) WorkersDir() string { return filepath.Join(c.Root, "workers") }
func (c *Config) LogsDir() string    { return filepath.Join(c.Root, "logs") }
func (c *Config) CacheDir() string   { return filepath.Join(c.Root, ".cache") }

// WorkerDir is where worker n's full runner install lives.
func (c *Config) WorkerDir(n int) string {
	return filepath.Join(c.WorkersDir(), fmt.Sprintf("w%d", n))
}

func (c *Config) WorkerName(n int) string { return fmt.Sprintf("%s%d", c.NamePrefix, n) }

// Label is the launchd job label for worker n. Namespaced under com.hangar so
// it can never collide with a runner installed by the vendor's own svc.sh.
func (c *Config) Label(n int) string { return fmt.Sprintf("com.hangar.w%d", n) }

// Load reads .env from root. Missing optional keys fall back to a default;
// missing required keys are reported together rather than one per run.
func Load(root string) (*Config, error) {
	env, err := parseEnvFile(filepath.Join(root, ".env"))
	if err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	host = strings.ToLower(strings.TrimSuffix(host, ".local"))
	if host == "" {
		host = "mac"
	}

	c := &Config{
		Root:       root,
		Token:      env["GH_TOKEN"],
		Org:        env["GH_ORG"],
		Group:      env["RUNNER_GROUP"],
		Labels:     env["RUNNER_LABELS"],
		NamePrefix: or(env["RUNNER_NAME_PREFIX"], host+"-w"),
		DockerHost: env["DOCKER_HOST"],
		RunnerPath: env["RUNNER_PATH"],
		Lang:       or(env["RUNNER_LANG"], "en_US.UTF-8"),
		SharePaths: splitList(env["SHARE_PATHS"]),
	}
	if c.DockerHost == "" {
		c.DockerHost = detectDockerHost()
	}

	return c, nil
}

// RequireGitHub is checked by the commands that actually talk to GitHub, so
// `update` still works from a half-filled .env while `scale` fails loudly.
func (c *Config) RequireGitHub() error {
	var missing []string
	if c.Token == "" {
		missing = append(missing, "GH_TOKEN")
	}
	if c.Org == "" {
		missing = append(missing, "GH_ORG")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s/.env is missing %s — see .env.example",
			c.Root, strings.Join(missing, " and "))
	}
	return nil
}

// ParseCount validates a worker count from the command line.
func ParseCount(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > MaxWorkers {
		return 0, fmt.Errorf("worker count must be 0-%d, got %q", MaxWorkers, s)
	}
	return n, nil
}

// FindRoot locates the repo root: HANGAR_ROOT when the Makefile set it,
// otherwise the nearest ancestor of the cwd holding a go.mod.
func FindRoot() (string, error) {
	if r := os.Getenv("HANGAR_ROOT"); r != "" {
		return r, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a hangar checkout (no go.mod found above %s)", dir)
		}
		dir = parent
	}
}

// parseEnvFile reads KEY=VALUE lines. Blank lines and # comments are skipped,
// and surrounding quotes are stripped so both KEY=v and KEY="v" work.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no .env at %s — copy .env.example to .env and fill it in", path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}

// detectDockerHost finds Docker Desktop's socket. A worker with a private HOME
// has no docker contexts to resolve the daemon from, and Docker Desktop does
// not create /var/run/docker.sock, so an unset DOCKER_HOST would break every
// docker step with a confusing "cannot connect" rather than a clear error.
func detectDockerHost() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	sock := filepath.Join(home, ".docker", "run", "docker.sock")
	if _, err := os.Stat(sock); err != nil {
		return ""
	}
	return "unix://" + sock
}

// splitList parses a comma-separated setting, dropping empty entries.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/specialistvlad/hangar/internal/credhelper"
)

// Isolation works by giving each worker its own HOME rather than by naming the
// tools to isolate.
//
// An earlier version enumerated DOCKER_CONFIG, CLOUDSDK_CONFIG and AWS_*. That
// list is unbounded and permanently one tool behind: npm, kubectl, ssh, git,
// cargo, terraform and helm all keep state in their own dotfiles, and some have
// no environment override at all. Since virtually every tool resolves its
// config relative to $HOME, moving HOME covers all of them at once — including
// the ones nobody has thought of yet.
//
// So the list is inverted. Everything is isolated by default and a short,
// explicit set of paths is shared back in (SHARE_PATHS), which is a bounded
// list because sharing is the exception.
//
// Two things HOME cannot cover, both handled below:
//
//   - The macOS keychain is per-user, not per-HOME. A config.json carrying
//     credsStore would still funnel every `docker login` into one shared
//     daemon, so each worker gets an empty one.
//   - Docker resolves its CLI plugins and contexts under $HOME/.docker. A fresh
//     home has neither, so buildx would vanish and the daemon socket would fall
//     back to /var/run/docker.sock, which does not exist on Docker Desktop.
const defaultPath = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// workerHome is the private home directory handed to worker n's jobs.
func (f *Fleet) workerHome(n int) string { return filepath.Join(f.cfg.WorkerDir(n), "home") }

// writeIsolation builds worker n's private home and the .env that points at it.
func (f *Fleet) writeIsolation(n int) error {
	home := f.workerHome(n)
	tmp := filepath.Join(f.cfg.WorkerDir(n), "tmp")
	dockerCfg := filepath.Join(home, ".docker")

	for _, d := range []string{home, tmp, dockerCfg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := f.fixupDocker(dockerCfg); err != nil {
		return err
	}
	if err := f.shareBack(home); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.cfg.WorkerDir(n), ".env"), []byte(f.workerEnv(n)), 0o600)
}

// fixupDocker repairs the two things a private HOME breaks for docker.
func (f *Fleet) fixupDocker(dockerCfg string) error {
	// An empty config is NOT enough. With credsStore unset the Docker CLI falls
	// back to a platform default — docker-credential-osxkeychain on macOS — and
	// a keychain write from a launchd agent raises a SecurityAgent dialog that
	// nobody can click, so every `docker login` blocks forever. Naming our own
	// helper removes the fallback entirely.
	cfg := fmt.Sprintf("{\n  \"credsStore\": %q\n}\n", credhelper.Name)
	if err := os.WriteFile(filepath.Join(dockerCfg, "config.json"), []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := f.linkCredHelper(dockerCfg); err != nil {
		return err
	}
	// buildx and compose are CLI plugins resolved under $HOME/.docker; without
	// this link the first `docker buildx build` fails with "unknown command".
	real, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return link(filepath.Join(real, ".docker", "cli-plugins"), filepath.Join(dockerCfg, "cli-plugins"))
}

// linkCredHelper places docker-credential-hangar on the worker's PATH, pointing
// at the running hangar binary so a rebuilt hangar fixes existing workers too.
func (f *Fleet) linkCredHelper(dockerCfg string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binDir := filepath.Join(dockerCfg, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	return link(self, filepath.Join(binDir, "docker-credential-"+credhelper.Name))
}

// shareBack symlinks the opt-in SHARE_PATHS from the real home into the
// worker's. Caches are the usual reason: isolating ~/.npm or a language module
// cache costs disk and cold-build time without protecting anything.
func (f *Fleet) shareBack(home string) error {
	if len(f.cfg.SharePaths) == 0 {
		return nil
	}
	real, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for _, raw := range f.cfg.SharePaths {
		rel, err := cleanSharePath(raw)
		if err != nil {
			return err
		}
		dst := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(real, rel), 0o755); err != nil {
			return err
		}
		if err := link(filepath.Join(real, rel), dst); err != nil {
			return err
		}
	}
	return nil
}

// cleanSharePath validates one SHARE_PATHS entry. Entries name a path inside
// the real home, so an absolute path or a .. escape is rejected rather than
// silently linking a worker at something outside it.
func cleanSharePath(raw string) (string, error) {
	rel := strings.TrimPrefix(strings.TrimSpace(raw), "~/")
	if rel == "" || strings.HasPrefix(rel, "/") || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") ||
		strings.HasSuffix(rel, "/..") {
		return "", fmt.Errorf("SHARE_PATHS entry %q must be a plain path relative to your home directory", raw)
	}
	return rel, nil
}

// link replaces dst with a symlink to src.
func link(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(src, dst)
}

// workerEnv builds the .env the runner injects into every job step. The runner
// reads it at service start (Runner.Listener.LoadAndSetEnv), so these three
// variables are the entire isolation mechanism — no wrappers, no interception.
func (f *Fleet) workerEnv(n int) string {
	env := []string{
		"HOME=" + f.workerHome(n),
		"TMPDIR=" + filepath.Join(f.cfg.WorkerDir(n), "tmp"),
		"HANGAR_WORKER=" + strconv.Itoa(n),
		// Declared rather than inherited: the runner's own env.sh would otherwise
		// capture whatever locale the invoking shell had.
		"LANG=" + f.cfg.Lang,
	}
	// Required, not optional: a private HOME has no docker contexts to resolve
	// the daemon from, and Docker Desktop does not create /var/run/docker.sock.
	if f.cfg.DockerHost != "" {
		env = append(env, "DOCKER_HOST="+f.cfg.DockerHost)
	}
	return strings.Join(env, "\n") + "\n"
}

// runnerPath is the PATH written to the worker's .path file. It is written out
// rather than inherited so a job's toolchain does not silently depend on
// whichever shell happened to run `make`.
func (f *Fleet) runnerPath(n int) string {
	base := f.cfg.RunnerPath
	if base == "" {
		real, _ := os.UserHomeDir()
		base = defaultPath + ":" + filepath.Join(real, ".docker", "bin")
	}
	// The credential helper must precede everything, or the CLI's platform
	// default wins and jobs deadlock on a keychain prompt.
	return filepath.Join(f.workerHome(n), ".docker", "bin") + ":" + base
}

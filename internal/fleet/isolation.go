package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
//     daemon, so each worker gets an empty one. Linux has the same trap in
//     the desktop secret service, which the same helper sidesteps.
//   - Docker resolves its CLI plugins and contexts under $HOME/.docker. A fresh
//     home has neither, so buildx would vanish and the daemon socket would fall
//     back to /var/run/docker.sock, which does not exist on Docker Desktop.

// defaultRunnerPath is the PATH a worker's jobs get when RUNNER_PATH is unset:
// the system directories plus, on macOS, Homebrew and Docker Desktop's CLI in
// the real home. Linux packages install docker and its plugins system-wide.
func defaultRunnerPath(goos, realHome string) string {
	if goos == "darwin" {
		return "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:" +
			filepath.Join(realHome, ".docker", "bin")
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin"
}

// workerHome is the private home directory handed to worker n's jobs.
func (f *Fleet) workerHome(n int) string { return filepath.Join(f.cfg.WorkerDir(n), "home") }

// workerTmp is worker n's TMPDIR: inside its directory by default, or under
// WORKER_TMP_ROOT when that is set — typically a size-capped tmpfs, so scratch
// files never reach the disk and a reboot clears them.
func (f *Fleet) workerTmp(n int) string {
	if f.cfg.TmpRoot != "" {
		return filepath.Join(f.cfg.TmpRoot, fmt.Sprintf("w%d", n))
	}
	return filepath.Join(f.cfg.WorkerDir(n), "tmp")
}

// writeIsolation builds worker n's private home, then writes the files that
// point the runner at it — its .env and .path — and the job hooks.
func (f *Fleet) writeIsolation(n int) error {
	home := f.workerHome(n)
	dockerCfg := filepath.Join(home, ".docker")

	for _, d := range []string{home, dockerCfg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := f.prepareTmp(n); err != nil {
		return err
	}
	if err := f.fixupDocker(dockerCfg); err != nil {
		return err
	}
	if err := f.shareBack(home); err != nil {
		return err
	}
	return f.writeWorkerFiles(n)
}

// fixupDocker repairs the two things a private HOME breaks for docker.
func (f *Fleet) fixupDocker(dockerCfg string) error {
	// An empty config is NOT enough. With credsStore unset the Docker CLI falls
	// back to a platform default — docker-credential-osxkeychain on macOS — and
	// a keychain write from a launchd agent raises a SecurityAgent dialog that
	// nobody can click, so every `docker login` blocks forever. On Linux the
	// default is pass or the desktop secret service, neither of which a
	// headless service has. Naming our own helper removes the fallback.
	cfg := fmt.Sprintf("{\n  \"credsStore\": %q\n}\n", credhelper.Name)
	if err := os.WriteFile(filepath.Join(dockerCfg, "config.json"), []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := f.linkCredHelper(dockerCfg); err != nil {
		return err
	}
	// buildx and compose are CLI plugins resolved under $HOME/.docker; without
	// this link the first `docker buildx build` fails with "unknown command" on
	// Docker Desktop. Linux packages install them system-wide instead, where the
	// CLI finds them regardless, and the real home usually has no such
	// directory. Linking to one that does not exist would leave a dangling
	// link, and every job that installs a plugin — setup-buildx-action with a
	// pinned version does — would fail to create the directory. So the worker
	// gets a private, empty one in that case.
	real, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	shared := filepath.Join(real, ".docker", "cli-plugins")
	private := filepath.Join(dockerCfg, "cli-plugins")
	if fi, err := os.Stat(shared); err == nil && fi.IsDir() {
		return link(shared, private)
	}
	if err := os.RemoveAll(private); err != nil {
		return err
	}
	return os.MkdirAll(private, 0o700)
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
	// Entries were validated when .env was loaded; see config.cleanSharePath.
	for _, rel := range f.cfg.SharePaths {
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

// link replaces dst with a symlink to src.
func link(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(src, dst)
}

// workerEnv builds the .env the runner injects into every job step. The runner
// reads it at service start (Runner.Listener.LoadAndSetEnv), so HOME, TMPDIR
// and DOCKER_HOST are the entire isolation mechanism — no wrappers, no
// interception. The job hooks that enforce the job time limit are named here
// too; see jobhook.go.
func (f *Fleet) workerEnv(n int) string {
	env := []string{
		"HOME=" + f.workerHome(n),
		"TMPDIR=" + f.workerTmp(n),
		"HANGAR_WORKER=" + strconv.Itoa(n),
		// Declared rather than inherited: the runner's own env.sh would otherwise
		// capture whatever locale the invoking shell had.
		"LANG=" + f.cfg.Lang,
	}
	// Required on macOS, not optional: a private HOME has no docker contexts to
	// resolve the daemon from, and Docker Desktop does not create
	// /var/run/docker.sock. On Linux it states the default socket outright.
	if f.cfg.DockerHost != "" {
		env = append(env, "DOCKER_HOST="+f.cfg.DockerHost)
	}
	env = append(env, f.hookEnv(n)...)
	return strings.Join(env, "\n") + "\n"
}

// runnerPath is the PATH written to the worker's .path file. It is written out
// rather than inherited so a job's toolchain does not silently depend on
// whichever shell happened to run `make`.
func (f *Fleet) runnerPath(n int) string {
	base := f.cfg.RunnerPath
	if base == "" {
		real, _ := os.UserHomeDir()
		base = defaultRunnerPath(runtime.GOOS, real)
	}
	// The credential helper must precede everything, or the CLI's platform
	// default wins and jobs deadlock on a keychain prompt.
	return filepath.Join(f.workerHome(n), ".docker", "bin") + ":" + base
}

# hangar

[![check](https://github.com/specialistvlad/hangar/actions/workflows/check.yml/badge.svg)](https://github.com/specialistvlad/hangar/actions/workflows/check.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run a fleet of GitHub Actions self-hosted runners on one machine — a Mac or a Linux
box — with a live dashboard.

```
make 4       # scale to 4 runners, then watch them
make watch   # dashboard only
make 0       # stop and unregister everything
```

Built for the case where one machine has far more capacity than one runner can use,
and Kubernetes is not worth standing up to fix that.

---

## The problem it solves

Copying a runner directory four times does not give you four runners. It gives you
four processes fighting over the same credential files:

- **`~/.docker/config.json` is shared.** Every `docker login` writes there, and with
  `credsStore: desktop` they all write into a single keychain-backed daemon. The
  `aws-actions/amazon-ecr-login` action runs `docker logout` in its post step — so
  job A's cleanup revokes the registry token job B is mid-push with.
- **`~/.config/gcloud` is shared.** Concurrent `gcloud auth` and
  `gcloud config set` races between jobs.

That is not a docker-and-gcloud problem, though — it is every tool. npm, kubectl,
ssh, git, cargo, terraform and helm all keep state in their own dotfiles, and a fix
that enumerates the ones you thought of is permanently one tool behind.

So hangar does not enumerate. **Each worker gets its own `HOME`**, which is where
virtually every tool resolves its config from, so all of them are separated at once
— including the ones nobody has thought of yet. The list is inverted: everything is
isolated by default, and a short, explicit `SHARE_PATHS` names what to share back.

The docker daemon and its BuildKit cache stay common, because that shared cache is
the entire reason building locally beats building on a hosted runner.

The mechanism is three lines in each worker's `.env`. The runner reads that file at
service start (`Runner.Listener.LoadAndSetEnv`) and injects it into every job step —
no wrapper scripts, no interception, nothing to keep in sync:

```
HOME=<worker>/home
TMPDIR=<worker>/tmp
DOCKER_HOST=unix:///Users/you/.docker/run/docker.sock    # or /var/run/docker.sock on Linux
```

### Why not actions-runner-controller?

[ARC](https://github.com/actions/actions-runner-controller) is the right answer when
you have a Kubernetes cluster and want runners that scale to zero on Linux. It is a
poor fit for the case this exists for — getting more out of one machine you already
own:

- **It needs a cluster.** ARC is a Kubernetes controller. If standing one up is the
  price of running four runners on a machine already sitting on your desk, the cure
  costs more than the disease.
- **Docker is not there by default.** Runner pods have no daemon unless you set
  `containerMode` to `dind` or `kubernetes`. The dind sidecar
  [requires `privileged: true`](https://github.com/actions/actions-runner-controller/blob/master/docs/deploying-alternative-runners.md)
  and runs the Docker daemon as root, which is a bigger grant than it sounds — and
  a nonstarter on plenty of clusters.
- **The build cache does not survive the job.** That dind daemon is per-pod, so its
  layer and BuildKit cache start cold on every run unless you attach persistent
  storage or a remote cache. hangar keeps one daemon and one warm cache across all
  workers, which is the entire reason building locally beats a hosted runner.
- **macOS is not really on the menu.** Apple Silicon nodes under Kubernetes are
  their own project, and Xcode and the macOS SDK do not containerize.

The tradeoff runs the other way too: hangar does not scale to zero, does not spread
across machines, and gives you isolated `HOME`s rather than isolated kernels. If you
need jobs that cannot see each other's processes, you want containers or VMs, not
this.

## Requirements

- **macOS or Linux.** On macOS, launchd supervises the workers and Docker Desktop
  runs the builds. On Linux, the user's systemd manager supervises them and a
  docker daemon, system-wide or rootless, runs the builds — see
  [Running on Linux](#running-on-linux) for the one-time setup. Anything else has
  no supervisor and does not build.
- **A GitHub token that can manage the org's runners** — see below.
- `make`, `curl`, `tar` and `git`. If a matching Go is already installed hangar uses
  it; otherwise it downloads a private one into `.toolchain/`, so a machine with no
  Go can still build. Either way nothing is written to `~/go` or the user's cache
  directory.

## Setup

```bash
git clone https://github.com/specialistvlad/hangar.git
cd hangar
make status     # first run creates .env from the template and stops
$EDITOR .env    # GH_TOKEN and GH_ORG are required
make 4
```

`GH_TOKEN` is a fine-grained token with one organization permission — see
[Getting a token](#getting-a-token) for the click-by-click, and run `make status`
to confirm it before scaling anything.

Send a job to the fleet with the `hangar` label, which every worker registers with:

```yaml
runs-on: [self-hosted, hangar]
```

Plain `runs-on: self-hosted` also works, but in an org where machines are
registered by hand as well as by hangar it will pick whichever is free — the
default labels, such as `self-hosted, macOS, ARM64` or `self-hosted, Linux, X64`,
describe the machine, not who manages it. Add `Linux` or `macOS` to `runs-on` to
pick a platform when the org has hangar fleets on both.
Labels are fixed at registration, so a fleet that predates this needs one
`make 0 && make <n>` to pick it up. The same holds for every per-worker setting —
`RUNNER_LABELS`, `RUNNER_PATH`, `RUNNER_LANG`, `SHARE_PATHS`, `WORKER_TMP_ROOT` — and for
the service definitions a newer hangar writes: they reach workers created after the
change. `make 0 && make <n>` while the fleet is idle applies them to all.

First run fetches the runner release, and a Go toolchain too unless a matching one
is already installed. Later runs build from cache.

Build state lives in `.gopath/`, `.gocache/` and `.toolchain/` and can reach a
couple of GB — the usual Go caches, relocated here rather than added. `make clean`
reclaims all of it.

## Running on Linux

Workers are systemd **user** units, run by the user's own service manager: no root,
no system units. Linux hosts need cgroup v2, which every current distribution
defaults to; on an older v1 host the fleet runs but the dashboard cannot sample
docker. Two things have to be true first, and both need root once:

```bash
sudo usermod -aG docker "$USER"                  # reach the docker daemon
sudo loginctl enable-linger "$USER"              # start at boot, survive logout
sudo systemctl restart "user@$(id -u).service"   # only if you were already logged in
```

Without lingering the user's manager — and every worker with it — stops at the last
logout and never starts at boot, which looks like a fleet that works only while
someone is watching it.

The restart matters because a running manager keeps the groups it started with, and
every worker inherits them. A manager that was already up when `usermod` ran never
gains `docker` — and once lingering is on, logging out and back in does not restart
it; a reboot works too. The restart stops every unit the manager runs: on a fleet
that is already up, every `hangar-w<n>.service` and `hangar-metrics.service`,
including any job mid-run. Wait until no worker is busy, or run `make 0` first,
before restarting a live fleet. `make <n>` checks both — lingering, and that the
manager can open the docker socket — and refuses to scale until they hold, printing
this same warning when the restart is still needed.

The runner itself needs the ICU library. Most distributions ship it; if registration
complains, install it as an administrator — the fleet's own account has no sudo — then
run `make <n>` again as the fleet's account. A worker that failed to register has
already been removed, so the retry starts clean. The runner's installer, taken from
the cached release, picks the right package for the distribution:

```bash
d=$(mktemp -d) && sudo tar xzf "$(sudo sh -c 'ls -t /srv/hangar/hangar/.cache/actions-runner-linux-*.tar.gz | head -1')" \
  -C "$d" ./bin/installdependencies.sh && sudo "$d/bin/installdependencies.sh"; sudo rm -rf "$d"
```

A dedicated account keeps the fleet away from everyone else's home, and a job's
reach down to what that account can touch. Make it a regular account rather than a
`--system` one: journald keeps per-user journals only for regular users, so
`journalctl --user` shows nothing for a system account.

```bash
sudo useradd --create-home --home-dir /srv/hangar --shell /bin/bash --groups docker hangar
sudo loginctl enable-linger hangar
sudo -iu hangar
git clone https://github.com/specialistvlad/hangar.git && cd hangar
make status     # first run creates .env from the template
$EDITOR .env
make 4
```

Operate it the same way afterwards — `sudo -iu hangar`, then `cd hangar && make watch`.
hangar talks to the systemd manager itself, so the session `sudo -i` opens is
enough; `systemctl --user` by hand needs `XDG_RUNTIME_DIR=/run/user/$(id -u)` set
in that shell.

Membership in `docker` is root-equivalent on the host, as it is for any self-hosted
runner that builds images. See [SECURITY.md](SECURITY.md).

Each worker's unit is `hangar-w<n>.service`:

```bash
systemctl --user status hangar-w1        # state, main pid, restarts and exit codes
journalctl --user -u hangar-w1           # start/stop history
tail -f logs/w1.out logs/w1.err          # the runner's own output
```

The runner writes its output to `logs/w<n>.out` and `logs/w<n>.err`, as on macOS,
not to the journal.

**Files a container wrote as root.** A job that bind-mounts part of its worker into a
container — `docker run -v "$GITHUB_WORKSPACE:/app" …`, or its `$HOME` or `$TMPDIR` —
leaves files owned by root, which the fleet's account cannot delete. Scaling down then
stops at that worker and prints the command that clears it through docker, which the
account can reach; run it and scale again. For worker 3:

```bash
docker run --rm -v "$PWD/workers:/w" alpine rm -rf /w/w3
```

**`WORKER_TMP_ROOT`** in `.env` moves every worker's `TMPDIR` to `<root>/w<n>`, for
example onto a size-capped tmpfs so build scratch never reaches the disk. Before every
start, on both platforms, each worker recreates its directory — so a reboot that
empties the tmpfs does no harm — and refuses to start unless both `<root>/w<n>` and the
root itself belong to the fleet's account and are not symlinks: whoever owns the root
could swap a worker's directory, and the scripts jobs write there, under a running
build. hangar also refuses a root that is itself, or sits inside, a directory every
account can write to — `/tmp`, `/var/tmp`, `/dev/shm`, or a tmpfs mounted with a
permissive mode — because the system's tmp cleaner sweeps those while workers run,
and anyone could recreate what it removed, or write there directly. Use a mount or
directory only the fleet's account can write to. hangar deletes `<root>/w<n>` when
the worker goes. On the host this was built for, fstab mounts it as
`tmpfs /srv/hangar/tmp tmpfs size=32G,mode=0700,uid=<hangar>,gid=<hangar>,nosuid,nodev 0 0`.

## Getting a token

The form is titled "repository-scoped token", but the part you need is the
**organization** section further down — hangar calls only org endpoints and
needs no repository access whatsoever.

Open <https://github.com/settings/personal-access-tokens/new> and:

1. **Token name** → `hangar`
2. **Resource owner** → your **organization**, not your personal account
3. **Expiration** → anything; hangar fails loudly when it lapses
4. **Repository access** → **Public repositories** (the smallest option; unused)
5. Scroll down to **Permissions**
6. **Repository permissions** → leave every row at *No access*
7. **Organization permissions** → **Self-hosted runners** → **Read and write**
8. **Generate token**
9. Copy the `github_pat_...` value — it is shown once

Then:

```bash
$EDITOR .env      # GH_TOKEN=github_pat_...
make status       # want: token: ok, can manage YOUR-ORG runners
```

`make status` calls the same endpoint `make 1` needs, so a green line here means
registration will work. It is the preflight — run it before scaling anything.

**If step 7 has no Organization permissions group,** step 2 did not take. That
group only renders once the resource owner is an organization.

**If the token comes back pending,** the org gates fine-grained tokens. An owner
approves at `https://github.com/organizations/YOUR-ORG/settings/personal-access-token-requests`.

### Why no repository access

Three separate mechanisms get confused here. Only the first is this token:

| what | controlled by |
|---|---|
| hangar adding and removing runners | this token |
| which repos may **use** the runners | the runner group, in org settings |
| a job cloning the repo | the per-job `GITHUB_TOKEN`, issued automatically |

This token never reaches a job. `actions/checkout` uses the short-lived
`GITHUB_TOKEN` GitHub mints per run, which is why a token with read-only public
repository access still builds private repositories fine.

### What the token is used for

Every GitHub call hangar makes, in full:

| endpoint | why |
|---|---|
| `POST /orgs/{org}/actions/runners/registration-token` | add a worker |
| `POST /orgs/{org}/actions/runners/remove-token` | drop a worker |
| `GET /orgs/{org}/actions/runners` | the `make status` preflight |
| `GET /repos/actions/runner/releases/latest` | public — sent without `GH_TOKEN` |

The release lookup is called anonymously, so `make update` keeps working while
the token is broken. `make 0` skips it for the same reason: tearing the fleet
down installs nothing.

You must be an owner/admin of the organization:

```bash
gh api /user/memberships/orgs/YOUR-ORG --jq .role   # must print "admin"
```

## Commands

| | |
|---|---|
| `make <0-32>` | Update the runner, scale the fleet to N, then open the dashboard |
| `make watch` | Dashboard — scaling with `+`/`-` is the only thing it changes |
| `make update` | Fetch the newest `actions/runner` release into `.cache/` |
| `make status` | One-shot summary, no TUI |
| `make kill` | Stop and delete every worker locally, without GitHub |
| `make metrics` / `make metrics-stop` | Run / remove the Prometheus exporter as a service |
| `make check` | vet + lint + file-length + tests, in parallel |
| `make test` / `make lint` | Individually |
| `make nuke` | Stop and delete every worker and all local state |

**Quitting the dashboard never stops a runner.** hangar does not own the runner
processes — launchd or systemd does — so `q` closes a viewer and nothing else. Stopping the
fleet is always the explicit `make 0`.

### `make kill` — stopping the fleet without a token

`make 0` unregisters each worker server-side before deleting it, and that needs
a removal token from GitHub. When `GH_TOKEN` has expired or was minted with the
wrong scope, that call fails and the fleet keeps running with no way to stop it.

`make kill` is the way out. It talks to nothing GitHub-side: it stops each
worker's service, removes its definition, and deletes the worker directory — for
every worker with a directory under `workers/`, a service the supervisor still
knows (a `com.hangar.w<n>` launchd agent, or a `hangar-w<n>.service` systemd
unit), or both.

When a worker's directory cannot be fully removed — typically files a job's
container wrote as root — `make kill` still stops and unregisters the rest, but
prints a distinct `killed N worker(s), M left on disk` summary and exits non-zero,
so a script or an operator running it unattended can tell a clean kill from one
that still needs finishing by hand.

What it trades away is the server side. The registrations stay, listed in the
org as **offline**, until a working token exists or an operator deletes them in
*Settings → Actions → Runners*. Prefer `make 0` whenever the token works.

## Metrics

`make metrics` runs `hangar serve` as one more supervised service next to the workers —
`hangar-metrics.service` on Linux, enabled for boot; the `com.hangar.metrics` agent on
macOS, loaded at login — restarted if it dies. It answers Prometheus scrapes at
`http://127.0.0.1:9151/metrics` (`METRICS_ADDR` in `.env`), and `make metrics` returns
only once that address answers with this build's own `hangar_build_info`, so a port
held by something else is reported rather than mistaken for success. `make status`
shows whether it is up, and both it and `make metrics` print the endpoint as a URL a
browser or curl can open — substituting `127.0.0.1` for an empty or unspecified host,
so a `METRICS_ADDR` of `:9151` (valid — it means every interface) reads as
`http://127.0.0.1:9151/metrics`. Re-run `make metrics` after rebuilding hangar to
restart it on the new binary. The endpoint has no authentication: loopback keeps it off the network,
not away from other accounts on the machine — see [SECURITY.md](SECURITY.md).

| Metric | Labels | Meaning |
|---|---|---|
| `hangar_worker_up` | `worker`, `runner` | 1 while the runner is registered and its listener runs |
| `hangar_worker_busy` | `worker`, `runner` | 1 while a job runs |
| `hangar_worker_job_info` | + `job_name`, `workflow`, `repo`, `run_id` | 1, only while busy |
| `hangar_worker_job_start_timestamp_seconds` | `worker`, `runner` | current job's start; absent while idle |
| `hangar_worker_last_job_end_timestamp_seconds` | `worker`, `runner` | when the last job ended |
| `hangar_jobs_total` | `worker`, `runner`, `result` | jobs finished: `succeeded`, `failed`, `canceled` |
| `hangar_job_duration_seconds` | `result` | histogram, 30 s to 2 h |
| `hangar_fleet_list_failures_total` | | fleet listings that failed to reach the supervisor, since the exporter started |
| `hangar_fleet_list_success_timestamp_seconds` | | unix time of the fleet listing that last reached the supervisor; 0 before the first one succeeds |
| `hangar_workers_configured` | | workers on this machine |
| `hangar_build_info` | `version`, `commit` | 1 |

Everything comes from what hangar already reads: the supervisor's view of the
workers, polled every 5 s, and the runner's own `_diag` logs — job starts and ends
from `Runner_*.log`, with the runner's timestamps, and each job's repository,
workflow and run id from the job message in its `Worker_*.log`. No GitHub access is
needed. Counters count from the moment the exporter started; after a restart it
restores what is running from the logs but does not count finished jobs again, so
query them with `rate()` or `increase()`. The text format is written without a client
library — hangar adds no dependency a page of stdlib covers.

`hangar_worker_job_info` adds one series per job: `job_name`, `workflow` and `repo`
change from one job to the next and `run_id` is unique to a single run, so a busy
fleet accumulates one time series per completed job for as long as Prometheus keeps
it. A `metric_relabel_configs` rule dropping the `run_id` label at scrape time keeps
the family bounded by workflow and repo instead, for a host running a long retention
window.

`hangar_fleet_list_failures_total` and `hangar_fleet_list_success_timestamp_seconds`
read whether that same poll's fleet listing reached the supervisor. When a listing
fails, every other worker gauge keeps its last value, so these two are how an alert
tells a fleet that is genuinely idle and static from one whose listing has simply
stopped answering.

## The dashboard

```
 hangar        4 workers → 6 · 3 busy · your-org/your-runner-group
 host    load ▁▂▃▅▇█▇▅ 6.20    mem ▃▄▅▅ 21G/32G   free 164G
 docker  cpu  ▂▄▆█▇▅▃ 340 %    mem 6G
────────────────────────────────────────────────────────────
 ● w1   release (web) / build                   4m12s
 ● w2   release (api) / build                   1m03s
 ● w3   release (docs) / build                    22s
 ○ w4   idle  last: Succeeded
 ◌ w5   provisioning…
 ◌ w6   queued
────────────────────────────────────────────────────────────
 w1│ #30 [release 3/4] COPY api/entrypoint.sh
 w2│ #12 [4/9] RUN npm ci
────────────────────────────────────────────────────────────
 → 6 workers · creating your-mac-w5 · 34s · +/- scale · 1-9 focus · a all · f follow · / fil
```

`+` and `-` add or remove a worker without leaving the dashboard — the same
reconcile `make N` runs, so a new one takes as long as registering a runner
does. The keys never block on it. A press moves a target and returns, and the
screen shows that target before any of it has happened: `→ 6` in the header,
a `◌` row per worker that does not exist yet, `removing…` on the ones on their
way out, and the live provisioning step in the footer.

Progress arrives as events rather than polling, so the footer moves when the
reconcile does. `-` refuses to remove a worker that is mid-job; `make N` still
forces it.

`1`–`9` focus one worker, `a` returns to all, `f` toggles follow, `/` filters,
scrolling up pauses follow automatically.

Docker gets its own row because that is where build CPU actually lands — the
runner processes themselves sit near idle while BuildKit does the work. On macOS
the row is Docker Desktop's virtual machine; on Linux it is the sum of the docker
daemon, containerd, every container and every BuildKit build step, read from their
cgroups — a system-wide daemon under `system.slice` and rootless docker under the
invoking user's own systemd instance (`user.slice/user-<uid>.slice/user@<uid>.service/…`)
both count, or `n/a` on a host without cgroup v2. Docker Desktop for Linux runs its
daemon inside a VM guest kernel, so no matching cgroup exists on the host and its
CPU is not counted.

Free disk shows the filesystem closest to running out among those a build writes to,
labeled with which one it is: the workers directory, `WORKER_TMP_ROOT` when set, and on
macOS the home volume holding Docker Desktop's disk image, on Linux docker's data root
and — when docker uses the containerd image store — containerd's root. Each is judged
against its own size: amber under a quarter free, red under a tenth, capped at 80GB and
30GB, which is roughly where parallel image builds start dying on ENOSPC. So a small
tmpfs reads fine while empty and does not hide a large volume that is filling. On Linux
a directory under an ext4 or XFS project quota reports its quota rather than the whole
disk — provided the directory carries the project-inherit flag (`chattr +P`); otherwise
statfs reports the whole filesystem.

## How it works

```
make N ──> update ──> scale ──> watch          watch is a READER, except for
              │        ▲ │         │           +/-, which re-enters scale
              │        └─│─── +/- ─┘
              ▼          ▼         ▼           tails files, samples the host
          .cache/    launchd /  _diag/*.log    Ctrl-C kills only the TUI
          tarball    systemd    (runner writes these itself)
```

There is no supervisor daemon and no log shipping, because neither is needed:

- **The OS supervises.** One launchd agent per worker on macOS, with `KeepAlive`
  for crash restart and `RunAtLoad` for reboot survival; one systemd user unit per
  worker on Linux, with `Restart=always` and an enable into `default.target`. This
  is also what makes "exit watch ≠ kill runners" fall out for free rather than
  needing a daemon.
- **The runner already writes the logs.** `_diag/Runner_*.log` carries job state
  transitions; `_diag/pages/*.log` carries live console output, BuildKit lines
  included. The dashboard tails those two files.
- **The filesystem is the state.** `workers/wN` directories *are* the fleet. There is
  no registry file that could drift out of sync with reality, and scaling is a diff:
  `make 4` twice does nothing the second time.

## What is isolated, and what is not

| Per worker | Shared |
|---|---|
| the whole of `$HOME` — every dotfile any tool writes | docker daemon + BuildKit cache |
| `$TMPDIR` | toolchains on `PATH` |
| `_work/`, `_diag/`, `.path`, `.credentials*` | anything named in `SHARE_PATHS` |

Two things a private `HOME` cannot cover, both handled during provisioning:

- **The macOS keychain is per-user, not per-`HOME`.** An empty `config.json` is not
  enough: with `credsStore` unset the Docker CLI falls back to a platform default,
  which on macOS is `docker-credential-osxkeychain` — and a keychain write from a
  launchd agent raises a SecurityAgent dialog nobody can click, so every
  `docker login` blocks forever. On Linux the default is `pass` or the desktop
  secret service, which a headless service has neither of. hangar therefore ships
  its own credential helper and names it explicitly, which removes the fallback.
  Credentials land in a plain file under the worker's own `HOME`.
- **Docker resolves CLI plugins and contexts under `$HOME/.docker`.** A fresh home
  has neither. When the real home has `~/.docker/cli-plugins` — Docker Desktop keeps
  buildx and compose there — it is symlinked in, or `docker buildx build` fails with
  *unknown command*. Otherwise, as on a Linux host with system-wide plugins, the worker
  gets a private, writable, empty one, and the CLI finds the system's plugins as usual.
  `DOCKER_HOST` is set explicitly, or hangar finds the socket itself — Docker
  Desktop's, then rootless Docker's per-user socket, then the system daemon's
  `/var/run/docker.sock`, which Docker Desktop never creates.

### Where credentials actually live

Isolation separates these per worker; it does not encrypt them. Worth knowing what
is on disk:

| secret | location | lifetime |
|---|---|---|
| runner identity RSA key | `workers/wN/.credentials_rsaparams`, mode 0600 | until unregistered |
| registry auths | `workers/wN/home/.docker/hangar-credentials.json`, mode 0600 | ECR ~12h, GAR ~1h |
| cloud credentials | `RUNNER_TEMP`, cleared per job | one job |
| `GH_TOKEN` | `.env`, mode 0600 | until revoked |

The RSA key is plaintext because that is how GitHub's runner stores it on macOS and
Linux — only Windows gets DPAPI. It is the same on a hand-installed runner. `GH_TOKEN` is
the one long-lived secret hangar itself chooses the storage for.

## Self-containment

Everything lives in this directory:

```
.toolchain/go/   pinned Go SDK           .cache/     runner tarballs
.gopath/         GOPATH + module cache   workers/    runner installs
.gocache/        build cache             logs/       worker stdout/stderr
.bin/            hangar, golangci-lint, gotestsum
```

There are three exceptions. The first is where the service manager loads workers
from: `~/Library/LaunchAgents/com.hangar.w*.plist` on macOS, because that is the only
location launchd loads login agents from, and `~/.config/systemd/user/hangar-w*.service`
on Linux, for the same reason. The second is opt-in: with `WORKER_TMP_ROOT` set, each
worker's `TMPDIR` lives at `<WORKER_TMP_ROOT>/w<n>`. `make 0` and `make kill` remove
both. The third is the exporter's own service, `com.hangar.metrics.plist` or
`hangar-metrics.service`, written by `make metrics`. `make 0` and `make kill` leave it
running on purpose — it then reports an empty fleet — and `make metrics-stop` or
`make nuke` removes it.

One account runs one hangar fleet. Every checkout on the account, whether another
worktree or a clone such as staging next to production, writes into the same
`~/Library/LaunchAgents` or `~/.config/systemd/user`, so a second checkout's worker
and exporter names collide with the first's. Before writing or restarting a unit or
plist, hangar reads back the `WorkingDirectory` of any that already exists. When it
falls outside this checkout's own tree, hangar refuses and names the checkout that
owns it. Stopping a worker, listing what runs and `make kill` apply the same check:
a unit or plist that belongs to another checkout stays running and untouched, and
this checkout leaves it out of its own fleet.

Settings come from `.env` and nowhere else — the ambient environment is never
consulted, so a fleet behaves the same from any shell, service manager or cron job.

## Migrating from a hand-installed runner

A runner installed the usual way (`./config.sh` + `./svc.sh`) still uses the shared
`~/.docker`, so leaving one registered alongside hangar's workers reintroduces
exactly the conflict this exists to remove. Retire it first:

```bash
cd /path/to/old/actions-runner
./svc.sh stop && ./svc.sh uninstall      # sudo ./svc.sh … on Linux
./config.sh remove --token <removal-token>
```

## Conventions

`make check` runs `go vet`, `golangci-lint`, a 250-line-per-file limit and the unit
tests in parallel, then prints a summary. Lint and test binaries are installed into
`.bin/` from this repo's own module cache, so checks never depend on a
system-installed tool. CI runs the same target on `macos-latest` and
`ubuntu-latest`, since each platform's supervisor and metrics only compile there.

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For anything
security-sensitive, [SECURITY.md](SECURITY.md) covers private reporting and the
tradeoffs that are deliberate.

## License

MIT — see [LICENSE](LICENSE).

# hangar

[![check](https://github.com/specialistvlad/hangar/actions/workflows/check.yml/badge.svg)](https://github.com/specialistvlad/hangar/actions/workflows/check.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run a fleet of GitHub Actions self-hosted runners on one Mac, with a live dashboard.

```
make 4       # scale to 4 runners, then watch them
make watch   # dashboard only
make 0       # stop and unregister everything
```

Built for the case where a Mac has far more capacity than one runner can use, and
Kubernetes is not worth standing up to fix that.

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
DOCKER_HOST=unix:///Users/you/.docker/run/docker.sock
```

### Why not actions-runner-controller?

[ARC](https://github.com/actions/actions-runner-controller) is the right answer when
you have a Kubernetes cluster and want runners that scale to zero on Linux. It is a
poor fit for the case this exists for — getting more out of one Mac you already own:

- **It needs a cluster.** ARC is a Kubernetes controller. If standing one up is the
  price of running four runners on a Mac already sitting on your desk, the cure
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

- **macOS on Apple Silicon.** launchd agents and `ps`/`vm_stat`/`sysctl` parsing make
  this genuinely darwin/arm64-only, not merely untested elsewhere.
- **A GitHub token that can manage the org's runners** — see below.
- Nothing else. If a matching Go is already installed hangar uses it; otherwise it
  downloads a private one into `.toolchain/`, so a machine with no Go can still
  build. Either way nothing is written to `~/go` or `~/Library/Caches`.

## Setup

```bash
git clone https://github.com/specialistvlad/hangar.git
cd hangar
make            # prints help, creates .env from the template on first run
$EDITOR .env    # GH_TOKEN and GH_ORG are required
make 4
```

`GH_TOKEN` is a fine-grained token with one organization permission — see
[Getting a token](#getting-a-token) for the click-by-click, and run `make status`
to confirm it before scaling anything.

First run fetches the runner release, and a Go toolchain too unless a matching one
is already installed. Later runs build from cache.

Build state lives in `.gopath/`, `.gocache/` and `.toolchain/` and can reach a
couple of GB — the usual Go caches, relocated here rather than added. `make clean`
reclaims all of it.

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
| `GET /repos/actions/runner/releases/latest` | public — needs no auth |

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
| `make check` | vet + lint + file-length + tests, in parallel |
| `make test` / `make lint` | Individually |
| `make nuke` | Stop and delete every worker and all local state |

**Quitting the dashboard never stops a runner.** hangar does not own the runner
processes — launchd does — so `q` closes a viewer and nothing else. Stopping the
fleet is always the explicit `make 0`.

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

Docker's VM gets its own row because that is where build CPU actually lands — the
runner processes themselves sit near idle while BuildKit does the work. Free disk
turns amber below 80GB and red below 30GB, which is roughly where parallel image
builds start dying on ENOSPC.

## How it works

```
make N ──> update ──> scale ──> watch          watch is a READER, except for
              │        ▲ │         │           +/-, which re-enters scale
              │        └─│─── +/- ─┘
              ▼          ▼         ▼           tails files, samples ps
          .cache/    launchd    _diag/*.log    Ctrl-C kills only the TUI
          tarball    plists     (runner writes these itself)
```

There is no supervisor daemon and no log shipping, because neither is needed:

- **launchd supervises.** One agent per worker, with `KeepAlive` for crash restart
  and `RunAtLoad` for reboot survival. This is also what makes "exit watch ≠ kill
  runners" fall out for free rather than needing a daemon.
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
  `docker login` blocks forever. hangar therefore ships its own credential helper
  and names it explicitly, which removes the fallback. Credentials land in a plain
  file under the worker's own `HOME`.
- **Docker resolves CLI plugins and contexts under `$HOME/.docker`.** A fresh home
  has neither, so `~/.docker/cli-plugins` is symlinked in (or `docker buildx build`
  fails with *unknown command*), and `DOCKER_HOST` is set explicitly (or the socket
  falls back to `/var/run/docker.sock`, which Docker Desktop never creates).

### Where credentials actually live

Isolation separates these per worker; it does not encrypt them. Worth knowing what
is on disk:

| secret | location | lifetime |
|---|---|---|
| runner identity RSA key | `workers/wN/.credentials_rsaparams`, mode 0600 | until unregistered |
| registry auths | `workers/wN/home/.docker/hangar-credentials.json`, mode 0600 | ECR ~12h, GAR ~1h |
| cloud credentials | `RUNNER_TEMP`, cleared per job | one job |
| `GH_TOKEN` | `.env`, mode 0600 | until revoked |

The RSA key is plaintext because that is how GitHub's runner stores it on macOS —
only Windows gets DPAPI. It is the same on a hand-installed runner. `GH_TOKEN` is
the one long-lived secret hangar itself chooses the storage for.

## Self-containment

Everything lives in this directory:

```
.toolchain/go/   pinned Go SDK           .cache/     runner tarballs
.gopath/         GOPATH + module cache   workers/    runner installs
.gocache/        build cache             logs/       launchd stdout/stderr
.bin/            hangar, golangci-lint, gotestsum
```

The single exception is `~/Library/LaunchAgents/com.hangar.w*.plist`, because that is
the only location launchd loads login agents from. `make 0` removes them.

Settings come from `.env` and nowhere else — the ambient environment is never
consulted, so a fleet behaves the same from any shell, launchd session or cron job.

## Migrating from a hand-installed runner

A runner installed the usual way (`./config.sh` + `./svc.sh`) still uses the shared
`~/.docker`, so leaving one registered alongside hangar's workers reintroduces
exactly the conflict this exists to remove. Retire it first:

```bash
cd /path/to/old/actions-runner
./svc.sh stop && ./svc.sh uninstall
./config.sh remove --token <removal-token>
```

## Conventions

`make check` runs `go vet`, `golangci-lint`, a 250-line-per-file limit and the unit
tests in parallel, then prints a summary. Lint and test binaries are installed into
`.bin/` from this repo's own module cache, so checks never depend on a
brew-installed tool. CI runs the same target on `macos-latest`.

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For anything
security-sensitive, [SECURITY.md](SECURITY.md) covers private reporting and the
tradeoffs that are deliberate.

## License

MIT — see [LICENSE](LICENSE).

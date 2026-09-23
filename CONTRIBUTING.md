# Contributing

Thanks for looking. hangar is small on purpose — the fastest way to get a change
merged is to keep it that way.

## Before you start

hangar runs on **macOS and Linux**. The platform-specific parts are split by build
tag and kept small: the supervisor (`internal/fleet/launchd.go` for launchd,
`systemd.go` and `systemd_unit.go` for systemd) and the host sampling
(`internal/metrics/metrics_darwin.go`, `metrics_linux.go`). Everything else —
isolation, registration, the credential helper, the TUI, log tailing — is shared.
A change to one platform's file usually wants the matching change in the other.
Support for another operating system needs a supervisor of its own, so please
open an issue before writing one.

You need a GitHub organization you own to exercise anything that registers a
runner. Most changes — the TUI, log tailing, metrics, the credential helper —
can be developed and tested without one.

## Getting set up

```bash
git clone https://github.com/specialistvlad/hangar.git
cd hangar
make status     # first run creates .env from the template and stops
make check      # vet + lint + file-length + tests
```

Everything builds into the repo: its own Go toolchain (if yours does not match
the pin), its own module cache, its own lint and test binaries. Nothing is
written to `~/go` or the user's cache directory. `make clean` reclaims all of it.

## The bar for a change

`make check` must pass. It runs `go vet`, `golangci-lint`, the unit tests, and a
**250-line-per-file limit** on non-test sources. CI runs the same target on
`macos-latest` and `ubuntu-latest`. A local run only compiles your own platform's
files, so for a change to shared code also run
`GOOS=linux go vet ./...` from a Mac, or `GOOS=darwin go vet ./...` from Linux.

A few conventions the existing code follows, worth matching:

- **Comments explain why, not what.** Most comments here exist because something
  non-obvious bit us — a keychain prompt that deadlocked four builds, an
  `EX_CONFIG` exit with an empty stdout. Those are the valuable ones.
- **No new dependencies without a reason** that a few lines of stdlib cannot
  cover. Dependencies are vendored; run `make vendor` after changing `go.mod`
  and commit the `vendor/` result.
- **Settings come from `.env` and nowhere else.** The ambient environment is
  deliberately never consulted, so a fleet behaves identically from any shell,
  service manager or cron job. Please do not add `os.Getenv` calls.
- **Non-trivial logic gets a test.** Nothing elaborate — the existing tests are
  plain table tests with no framework.

## Pull requests

Small and focused beats large and comprehensive. Describe what broke or what was
missing; if it was a bug, say how you reproduced it. If the change alters
behavior that the README describes, update the README in the same PR — a
mismatch there is treated as a bug in its own right.

## Reporting bugs

Include your OS and version, how docker is running (Docker Desktop, a system-wide
daemon, or rootless), and the relevant output from `logs/wN.err` or
`workers/wN/_diag/` — on Linux also `systemctl --user status hangar-wN`. **Scrub
tokens before pasting** — `_diag` logs and `.env` both contain credentials.

## Conduct

Be decent to other people. Harassment, personal attacks, and bad-faith argument
are not welcome, and maintainers may remove comments or block accounts over
them. That is the whole policy.

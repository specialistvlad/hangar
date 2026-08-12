# Contributing

Thanks for looking. hangar is small on purpose — the fastest way to get a change
merged is to keep it that way.

## Before you start

hangar is **macOS on Apple Silicon only**, and not incidentally: launchd agents,
the `ps`/`vm_stat`/`sysctl` parsing, and Docker Desktop's socket layout are all
darwin/arm64 specifics. Ports to Linux or Intel are not small changes, so please
open an issue before writing one.

You need a GitHub organization you own to exercise anything that registers a
runner. Most changes — the TUI, log tailing, metrics, the credential helper —
can be developed and tested without one.

## Getting set up

```bash
git clone https://github.com/specialistvlad/hangar.git
cd hangar
make            # prints help, creates .env from the template on first run
make check      # vet + lint + file-length + tests
```

Everything builds into the repo: its own Go toolchain (if yours does not match
the pin), its own module cache, its own lint and test binaries. Nothing is
written to `~/go` or `~/Library/Caches`. `make clean` reclaims all of it.

## The bar for a change

`make check` must pass. It runs `go vet`, `golangci-lint`, the unit tests, and a
**250-line-per-file limit** on non-test sources. CI runs the same target on
`macos-latest`, so a green local run is a green PR.

A few conventions the existing code follows, worth matching:

- **Comments explain why, not what.** Most comments here exist because something
  non-obvious bit us — a keychain prompt that deadlocked four builds, an
  `EX_CONFIG` exit with an empty stdout. Those are the valuable ones.
- **No new dependencies without a reason** that a few lines of stdlib cannot
  cover. Dependencies are vendored; run `make vendor` after changing `go.mod`
  and commit the `vendor/` result.
- **Settings come from `.env` and nowhere else.** The ambient environment is
  deliberately never consulted, so a fleet behaves identically from any shell,
  launchd session or cron job. Please do not add `os.Getenv` calls.
- **Non-trivial logic gets a test.** Nothing elaborate — the existing tests are
  plain table tests with no framework.

## Pull requests

Small and focused beats large and comprehensive. Describe what broke or what was
missing; if it was a bug, say how you reproduced it. If the change alters
behavior that the README describes, update the README in the same PR — a
mismatch there is treated as a bug in its own right.

## Reporting bugs

Include your macOS version, whether Docker Desktop is running, and the relevant
output from `logs/wN.err` or `workers/wN/_diag/`. **Scrub tokens before
pasting** — `_diag` logs and `.env` both contain credentials.

## Conduct

Be decent to other people. Harassment, personal attacks, and bad-faith argument
are not welcome, and maintainers may remove comments or block accounts over
them. That is the whole policy.

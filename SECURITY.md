# Security Policy

## Reporting a vulnerability

Please report privately through GitHub's
[private vulnerability reporting](https://github.com/specialistvlad/hangar/security/advisories/new)
— the **Security** tab on this repository — rather than opening a public issue.

Include what an attacker gains and how you reproduced it. Expect a first
response within a week. This is a small project maintained in spare time; there
is no bounty.

## What hangar is trusted with

hangar holds a GitHub token that can add and remove an organization's
self-hosted runners, and it provisions machines that execute workflow code.
Findings that matter most:

- The `GH_TOKEN` in `.env` reaching a job, a log, `ps` output, or the network
- One worker reading another worker's credentials
- The runner tarball or Go toolchain being installed without checksum
  verification
- Anything that lets a workflow escalate beyond the worker it runs in

## Known and accepted design tradeoffs

These are deliberate. Reporting them is welcome, but they are documented
positions rather than oversights:

- **Isolation separates credentials per worker; it does not encrypt them.**
  Each worker gets its own `HOME`, so every tool's dotfiles are separate — but
  what lands on disk is plaintext, mode 0600. The README's *Where credentials
  actually live* table is the full inventory.
- **Registry credentials are stored in a plain file, not the macOS keychain.**
  A keychain write from a launchd agent raises a SecurityAgent dialog nobody can
  click, which deadlocks every `docker login`. hangar ships its own credential
  helper specifically to avoid that fallback.
- **The runner identity RSA key is plaintext.** That is how GitHub's own runner
  stores it on macOS; only Windows gets DPAPI. It is identical on a
  hand-installed runner.
- **Workers share one Docker daemon and its BuildKit cache.** That shared cache
  is the reason to build locally at all. A job that can reach the daemon can
  reach the host — self-hosted runners are not a sandbox, which is why GitHub
  advises against using them on public repositories.

## Self-hosted runners and public repositories

Do not point hangar's runners at a public repository. Anyone who can open a pull
request could then run code on your Mac. This is GitHub's guidance for all
self-hosted runners, not a hangar-specific limitation.

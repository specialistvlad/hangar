# hangar — GitHub Actions runner fleet for one machine, macOS or Linux.
#
# Everything hangar needs lives inside this directory: its own Go toolchain, its
# own module cache, its own lint and test binaries, its own runner tarballs.
# Nothing is read from or written to the ambient system except where the
# service manager loads workers from — ~/Library/LaunchAgents for launchd on
# macOS, ~/.config/systemd/user for the systemd user manager on Linux — and,
# when .env sets WORKER_TMP_ROOT, the workers' TMPDIRs under it.

SHELL := /bin/bash
.DEFAULT_GOAL := help

ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

GO_VERSION        := 1.26.5
# The toolchain archive for this machine, named the way go.dev names it. On a
# Mac the architecture comes from the hardware rather than uname: a terminal
# running under Rosetta reports x86_64 on Apple Silicon, and hw.optional.arm64
# still reads 1 there. go.dev's armv6l build is the one for all 32-bit ARM.
GO_OS             := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ifeq ($(GO_OS),darwin)
GO_ARCH           := $(if $(filter 1,$(shell /usr/sbin/sysctl -n hw.optional.arm64 2>/dev/null)),arm64,amd64)
else
GO_ARCH           := $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/' -e 's/^armv[67]l$$/armv6l/')
endif
GO_PLATFORM       := $(GO_OS)-$(GO_ARCH)
# Checksums of each archive, published by go.dev alongside the release. Update
# them whenever GO_VERSION changes; the values are the sha256 fields from
# https://go.dev/dl/?mode=json&include=all for this version.
GO_SHA256_darwin-arm64 := efb87ff28af9a188d0536ef5d42e63dd52ba8263cd7344a993cc48dd11dedb6a
GO_SHA256_darwin-amd64 := 6231d8d3b8f5552ec6cbf6d685bdd5482e1e703214b120e89b3bf0d7bf1ef725
GO_SHA256_linux-amd64  := 5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053
GO_SHA256_linux-arm64  := fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49
GO_SHA256_linux-armv6l := 6dae9edab81c13bccf962dec15f1fd2ec26c14a6821b4d2c92dab4130c289d7a
GO_SHA256         := $(GO_SHA256_$(GO_PLATFORM))
# sha256sum on Linux, shasum on macOS; both read the same "<sum>  <file>" line.
SHA256_CHECK      := $(if $(shell command -v sha256sum 2>/dev/null),sha256sum -c -,shasum -a 256 -c -)
GOLANGCI_VERSION  := v2.12.2
GOTESTSUM_VERSION := v1.13.0
MAX_FILE_LINES    := 250

GO_DIR    := $(ROOT)/.toolchain/go

# Reuse an already-installed Go when it matches the pin exactly — version and
# the machine's own architecture — and download a private one only when it
# does not. A vendored toolchain is ~260MB to produce a ~10MB binary, so
# duplicating a Go that is already present buys nothing; the download still
# guarantees a machine with no Go at all can build. The architecture has to
# match too: an x86_64 Go on Apple Silicon would make every tool install below
# a cross-compile, which `go install` refuses with GOBIN set.
# GOTOOLCHAIN=local on the probes: without it, an ambient go run inside this
# module auto-switches to the toolchain go.mod asks for and reports *that*
# version, so a mismatched go looks like a match — and the build then runs the
# ambient binary with GOTOOLCHAIN=local, which refuses the very switch the probe
# relied on. Pinned to the binary's own version, the check means what it says.
GO_NATIVE_ARCH := $(patsubst armv6l,arm,$(GO_ARCH))
ifeq ($(shell GOTOOLCHAIN=local go env GOVERSION GOHOSTARCH 2>/dev/null | paste -sd' ' -),go$(GO_VERSION) $(GO_NATIVE_ARCH))
GO        := $(shell command -v go)
GOROOT_DIR := $(shell GOTOOLCHAIN=local go env GOROOT)
else
GO        := $(GO_DIR)/bin/go
GOROOT_DIR := $(GO_DIR)
endif
BIN       := $(ROOT)/.bin/hangar
# What hangar_build_info reports: the checkout's commit, marked dirty when it
# has uncommitted changes, and the nearest tag if there is one.
HANGAR_COMMIT  := $(shell git -C $(ROOT) rev-parse --short=12 HEAD 2>/dev/null || echo unknown)$(shell git -C $(ROOT) diff --quiet HEAD -- 2>/dev/null || echo -dirty)
HANGAR_VERSION := $(shell git -C $(ROOT) describe --tags --always 2>/dev/null || echo dev)
LDFLAGS   := -X main.version=$(HANGAR_VERSION) -X main.commit=$(HANGAR_COMMIT)
GOLANGCI  := $(ROOT)/.bin/golangci-lint
GOTESTSUM := $(ROOT)/.bin/gotestsum
# Only hangar's own sources. A bare find over $(ROOT) would also sweep up the
# module cache under .gopath/ once lint tools are installed, and make chokes on
# the testdata paths it finds there.
SRC       := $(ROOT)/main.go $(shell find $(ROOT)/internal -name '*.go' 2>/dev/null)

# Every Go path is redirected into the repo, so building hangar never touches
# ~/go, ~/Library/Caches, ~/.cache, or a Go the user happens to have installed.
export GOROOT      := $(GOROOT_DIR)
export GOPATH      := $(ROOT)/.gopath
export GOMODCACHE  := $(ROOT)/.gopath/pkg/mod
export GOCACHE     := $(ROOT)/.gocache
export GOTOOLCHAIN := local
export GOFLAGS     := -mod=vendor
export GOLANGCI_LINT_CACHE := $(ROOT)/.gocache/golangci-lint
export HANGAR_ROOT := $(ROOT)
# A Mac builds hangar for its hardware even from a terminal running under
# Rosetta, so the binary never runs translated. The Go chosen above is native,
# so this never turns a tool install into a cross-compile.
ifeq ($(GO_OS),darwin)
export GOARCH      := $(GO_ARCH)
endif
# The pinned toolchain has to lead PATH, not just GOROOT: golangci-lint and
# gotestsum shell out to whatever `go` they find, and finding an ambient one
# under GOTOOLCHAIN=local fails outright rather than switching.
export PATH        := $(GOROOT_DIR)/bin:$(PATH)

COUNTS := $(shell seq 0 32)

.PHONY: help build watch update status kill metrics metrics-stop check check-file-length lint test vendor stats clean nuke $(COUNTS)

# ─── Fleet ────────────────────────────────────────────────────────────────────

# `make <0-32>` — updates the runner, scales the fleet, then opens the dashboard.
# Documented by hand in help: the target names are generated, so the usual
# "target: ## description" convention cannot annotate them.
#
# `make 0` skips the update and the dashboard: a tear-down installs no runner,
# so making it wait on a download is a network round trip that can only fail.
$(COUNTS): $(BIN) $(ROOT)/.env
	@if [ "$@" != "0" ]; then $(BIN) update; fi
	@$(BIN) scale $@
	@if [ "$@" != "0" ]; then $(BIN) watch; fi

watch: $(BIN) $(ROOT)/.env ## Dashboard only — never starts, stops or changes anything
	@$(BIN) watch

update: $(BIN) $(ROOT)/.env ## Fetch the newest actions/runner release into .cache
	@$(BIN) update

status: $(BIN) $(ROOT)/.env ## One-shot fleet summary, no TUI
	@$(BIN) status

kill: $(BIN) $(ROOT)/.env ## Stop and delete every worker locally, without GitHub
	@$(BIN) kill

metrics: $(BIN) $(ROOT)/.env ## Run the Prometheus exporter as a service; re-run after a rebuild
	@$(BIN) metrics start

metrics-stop: $(BIN) $(ROOT)/.env ## Stop and remove the exporter service
	@$(BIN) metrics stop

# ─── Checks ───────────────────────────────────────────────────────────────────

check: $(GOLANGCI) $(GOTESTSUM) ## Run all checks in parallel (vet + lint + file-length + unit)
	@set -o pipefail; \
	_dir=$$(mktemp -d); trap 'rm -rf $$_dir' EXIT; \
	echo "▸ launching 4 checks in parallel (output captured per step, dumped on completion)"; \
	echo ""; \
	step() { \
		local id="$$1" label="$$2"; shift 2; \
		local start; start=$$(date +%s); \
		echo "[start] $$label"; \
		( "$$@" ) > "$$_dir/$$id.out" 2>&1; \
		local rc=$$?; \
		echo "$$rc"    > "$$_dir/$$id.rc"; \
		echo "$$label" > "$$_dir/$$id.label"; \
		echo "$$(($$(date +%s) - start))" > "$$_dir/$$id.dur"; \
		if [ "$$rc" -eq 0 ]; then echo "[done]  $$label ($$(cat $$_dir/$$id.dur)s)"; \
		else echo "[FAIL]  $$label ($$(cat $$_dir/$$id.dur)s, rc=$$rc)"; fi; \
	}; \
	step vet  "go vet"                                        $(GO) vet ./... & \
	step lint "golangci-lint"                                 $(GOLANGCI) run ./... & \
	step flen "file length ($(MAX_FILE_LINES) line hard limit)" $(MAKE) -s check-file-length & \
	step unit "unit"                                          $(GOTESTSUM) --format testdox -- ./... -count=1 & \
	wait; \
	echo ""; \
	for id in vet lint flen unit; do \
		echo "════════════════════════════════════════"; \
		echo "  $$(cat $$_dir/$$id.label)"; \
		echo "════════════════════════════════════════"; \
		cat "$$_dir/$$id.out"; \
		echo ""; \
	done; \
	_failed=0; \
	echo "═══════════════════════════════════════"; \
	echo "  make check — summary"; \
	echo "═══════════════════════════════════════"; \
	for id in vet lint flen unit; do \
		rc=$$(cat "$$_dir/$$id.rc"); dur=$$(cat "$$_dir/$$id.dur"); label=$$(cat "$$_dir/$$id.label"); \
		done_line=$$(grep '^DONE' "$$_dir/$$id.out" 2>/dev/null || true); \
		mark="✓"; if [ "$$rc" != "0" ]; then mark="✗"; _failed=1; fi; \
		printf "  %s %s (%ss)" "$$mark" "$$label" "$$dur"; \
		if [ -n "$$done_line" ]; then printf ": %s" "$$done_line"; fi; \
		echo ""; \
	done; \
	exit $$_failed

# Scoped to hangar's own sources. Searching $(ROOT) would also walk workers/,
# where running jobs check out whatever repository they are building — linting
# someone else's code and failing on it.
check-file-length: ## Fail if any Go source file exceeds the line limit (tests excluded)
	@violations=0; \
	for f in $(ROOT)/main.go $$(find $(ROOT)/internal -name '*.go' -not -name '*_test.go'); do \
		lines=$$(wc -l < "$$f"); \
		if [ "$$lines" -gt $(MAX_FILE_LINES) ]; then \
			echo "FAIL: $$f has $$lines lines (limit: $(MAX_FILE_LINES))"; \
			violations=$$((violations + 1)); \
		fi; \
	done; \
	if [ "$$violations" -gt 0 ]; then \
		echo "$$violations file(s) exceed the $(MAX_FILE_LINES)-line limit."; \
		exit 1; \
	fi; \
	echo "All files within $(MAX_FILE_LINES)-line limit."

lint: $(GOLANGCI) ## Run golangci-lint
	@$(GOLANGCI) run ./...

test: $(GOTESTSUM) ## Run unit tests
	@$(GOTESTSUM) --format testdox -- ./... -count=1 $(if $(FILTER),-run $(FILTER),)

stats: ## Show code statistics via scc, when it is installed
	@command -v scc >/dev/null 2>&1 \
		&& scc --no-cocomo $(ROOT)/internal $(ROOT)/main.go \
		|| echo "scc not installed (https://github.com/boyter/scc)"

# ─── Build ────────────────────────────────────────────────────────────────────

build: $(BIN) ## Compile the binary into .bin/

vendor: $(GO) ## Re-resolve and re-vendor dependencies (needs network)
	@GOFLAGS= $(GO) mod tidy
	@GOFLAGS= $(GO) mod vendor

clean: ## Drop build output and caches, keep workers and .env
	@rm -rf $(ROOT)/.bin $(ROOT)/.gocache $(ROOT)/.gopath $(ROOT)/.toolchain
	@echo "cleaned build output (workers untouched — use 'make 0' to stop them)"

nuke: $(BIN) ## Stop and delete every worker, then remove all local state
	@-$(BIN) scale 0
	@-$(BIN) metrics stop
	@rm -rf $(ROOT)/.bin $(ROOT)/.gocache $(ROOT)/.gopath $(ROOT)/.toolchain \
	        $(ROOT)/.cache $(ROOT)/workers $(ROOT)/logs
	@echo "removed everything except .env"

# ─── Help ─────────────────────────────────────────────────────────────────────

help: ## Show this help
	@echo "hangar — GitHub Actions runner fleet for one machine"
	@echo ""
	@printf "\033[36m%-20s\033[0m %s\n" "<0-32>" "Scale the fleet to N workers, then open the dashboard"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Quitting the dashboard leaves runners running. Use 'make 0' to stop them."

# ─── Plumbing ─────────────────────────────────────────────────────────────────

# Built beside the old binary and renamed over it: running workers execute this
# file as their docker credential helper, and a push that runs it mid-copy
# would fail. A rename swaps it in one step.
$(BIN): $(GO) $(SRC) go.mod
	@mkdir -p $(dir $(BIN))
	@$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN).new . && mv -f $(BIN).new $(BIN)

# A pinned toolchain rather than whatever `go` is on PATH: the build must be
# reproducible on a machine that has no Go at all.
#
# Downloaded to a file and verified before extraction, rather than piped
# straight into tar: a piped archive is unpacked as it arrives, so there is no
# point at which the contents could still be rejected.
$(GO):
	@test -n "$(GO_SHA256)" || { echo "no pinned go$(GO_VERSION) checksum for $(GO_PLATFORM) — add GO_SHA256_$(GO_PLATFORM) to the Makefile"; exit 1; }
	@[[ "$(GO_SHA256)" =~ ^[0-9a-f]{64}$$ ]] || { echo "GO_SHA256_$(GO_PLATFORM) is not a 64-character sha256 — refusing to trust it"; exit 1; }
	@echo "==> fetching go$(GO_VERSION) ($(GO_PLATFORM)) into .toolchain/"
	@mkdir -p $(ROOT)/.toolchain
	@curl -fsSL -o $(ROOT)/.toolchain/go.tar.gz "https://go.dev/dl/go$(GO_VERSION).$(GO_PLATFORM).tar.gz"
	@echo "$(GO_SHA256)  $(ROOT)/.toolchain/go.tar.gz" | $(SHA256_CHECK) \
		|| { rm -f $(ROOT)/.toolchain/go.tar.gz; echo "toolchain checksum mismatch — refusing to extract"; exit 1; }
	@tar xzf $(ROOT)/.toolchain/go.tar.gz -C $(ROOT)/.toolchain
	@rm -f $(ROOT)/.toolchain/go.tar.gz
	@test -x $(GO) || { echo "toolchain fetch failed"; exit 1; }

# Lint and test binaries are installed into .bin from the repo's own module
# cache, so `make check` never depends on a brew-installed tool.
$(GOLANGCI): $(GO)
	@echo "==> installing golangci-lint $(GOLANGCI_VERSION) into .bin/"
	@GOFLAGS= GOBIN=$(ROOT)/.bin $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

$(GOTESTSUM): $(GO)
	@echo "==> installing gotestsum $(GOTESTSUM_VERSION) into .bin/"
	@GOFLAGS= GOBIN=$(ROOT)/.bin $(GO) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

# First run bootstraps .env and stops, rather than failing deeper in with a
# confusing error about a missing token.
$(ROOT)/.env:
	@cp $(ROOT)/.env.example $(ROOT)/.env
	@chmod 600 $(ROOT)/.env
	@echo "created .env — fill in GH_TOKEN and GH_ORG, then re-run"
	@exit 1

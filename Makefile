# hangar — GitHub Actions runner fleet for one Mac.
#
# Everything hangar needs lives inside this directory: its own Go toolchain, its
# own module cache, its own lint and test binaries, its own runner tarballs.
# Nothing is read from or written to the ambient system except
# ~/Library/LaunchAgents, which is the only place launchd loads login agents from.

SHELL := /bin/bash
.DEFAULT_GOAL := help

ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

GO_VERSION        := 1.26.5
# Checksum of the darwin-arm64 archive, published by go.dev alongside the
# release. Update it whenever GO_VERSION changes; the value is the sha256 field
# from https://go.dev/dl/?mode=json&include=all for this version.
GO_SHA256         := efb87ff28af9a188d0536ef5d42e63dd52ba8263cd7344a993cc48dd11dedb6a
GOLANGCI_VERSION  := v2.12.2
GOTESTSUM_VERSION := v1.13.0
MAX_FILE_LINES    := 250

GO_DIR    := $(ROOT)/.toolchain/go

# Reuse an already-installed Go when it matches the pin exactly, and download a
# private one only when it does not. A vendored toolchain is ~260MB to produce a
# ~10MB binary, so duplicating a Go that is already present buys nothing; the
# download still guarantees a machine with no Go at all can build.
# GOTOOLCHAIN=local on the probes: without it, an ambient go run inside this
# module auto-switches to the toolchain go.mod asks for and reports *that*
# version, so a mismatched go looks like a match — and the build then runs the
# ambient binary with GOTOOLCHAIN=local, which refuses the very switch the probe
# relied on. Pinned to the binary's own version, the check means what it says.
ifeq ($(shell GOTOOLCHAIN=local go env GOVERSION 2>/dev/null),go$(GO_VERSION))
GO        := $(shell command -v go)
GOROOT_DIR := $(shell GOTOOLCHAIN=local go env GOROOT)
else
GO        := $(GO_DIR)/bin/go
GOROOT_DIR := $(GO_DIR)
endif
BIN       := $(ROOT)/.bin/hangar
GOLANGCI  := $(ROOT)/.bin/golangci-lint
GOTESTSUM := $(ROOT)/.bin/gotestsum
# Only hangar's own sources. A bare find over $(ROOT) would also sweep up the
# module cache under .gopath/ once lint tools are installed, and make chokes on
# the testdata paths it finds there.
SRC       := $(ROOT)/main.go $(shell find $(ROOT)/internal -name '*.go' 2>/dev/null)

# Every Go path is redirected into the repo, so building hangar never touches
# ~/go, ~/Library/Caches, or a Go the user happens to have installed.
export GOROOT      := $(GOROOT_DIR)
export GOPATH      := $(ROOT)/.gopath
export GOMODCACHE  := $(ROOT)/.gopath/pkg/mod
export GOCACHE     := $(ROOT)/.gocache
export GOTOOLCHAIN := local
export GOFLAGS     := -mod=vendor
export HANGAR_ROOT := $(ROOT)

COUNTS := $(shell seq 0 32)

.PHONY: help build watch update status check check-file-length lint test vendor stats clean nuke $(COUNTS)

# ─── Fleet ────────────────────────────────────────────────────────────────────

# `make <0-32>` — updates the runner, scales the fleet, then opens the dashboard.
# Documented by hand in help: the target names are generated, so the usual
# "target: ## description" convention cannot annotate them.
$(COUNTS): $(BIN) $(ROOT)/.env
	@$(BIN) update
	@$(BIN) scale $@
	@if [ "$@" != "0" ]; then $(BIN) watch; fi

watch: $(BIN) $(ROOT)/.env ## Dashboard only — never starts, stops or changes anything
	@$(BIN) watch

update: $(BIN) $(ROOT)/.env ## Fetch the newest actions/runner release into .cache
	@$(BIN) update

status: $(BIN) $(ROOT)/.env ## One-shot fleet summary, no TUI
	@$(BIN) status

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
		|| echo "scc not installed (brew install scc)"

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
	@rm -rf $(ROOT)/.bin $(ROOT)/.gocache $(ROOT)/.gopath $(ROOT)/.toolchain \
	        $(ROOT)/.cache $(ROOT)/workers $(ROOT)/logs
	@echo "removed everything except .env"

# ─── Help ─────────────────────────────────────────────────────────────────────

help: ## Show this help
	@echo "hangar — GitHub Actions runner fleet for one Mac"
	@echo ""
	@printf "\033[36m%-20s\033[0m %s\n" "<0-32>" "Scale the fleet to N workers, then open the dashboard"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Quitting the dashboard leaves runners running. Use 'make 0' to stop them."

# ─── Plumbing ─────────────────────────────────────────────────────────────────

$(BIN): $(GO) $(SRC) go.mod
	@mkdir -p $(dir $(BIN))
	@$(GO) build -o $(BIN) .

# A pinned toolchain rather than whatever `go` is on PATH: the build must be
# reproducible on a machine that has no Go at all.
#
# Downloaded to a file and verified before extraction, rather than piped
# straight into tar: a piped archive is unpacked as it arrives, so there is no
# point at which the contents could still be rejected.
$(GO):
	@echo "==> fetching go$(GO_VERSION) into .toolchain/"
	@mkdir -p $(ROOT)/.toolchain
	@curl -fsSL -o $(ROOT)/.toolchain/go.tar.gz "https://go.dev/dl/go$(GO_VERSION).darwin-arm64.tar.gz"
	@echo "$(GO_SHA256)  $(ROOT)/.toolchain/go.tar.gz" | shasum -a 256 -c - \
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

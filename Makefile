# =============================================================================
# ptrbox - development entry points.
#
#   make build   compile the CLI to dist/ptrbox
#   make lint    go vet + shell syntax/shellcheck, runs anywhere
#   make test    unit + simulation tests against a fake lima, runs anywhere
#                (no Mac, no VM, no network)
#   make smoke   the real VM cycle - macOS + lima only, destroys and recreates
#                a scratch VM, takes minutes
#
# lint and test are the automated feedback loop: they must pass before a
# commit. smoke is the only step that needs actual hardware.
#
# The host CLI is Go; the guest-side provisioning scripts stay bash, so both
# toolchains are linted.
# =============================================================================
SHELL := /bin/bash
GO ?= go
BIN := dist/ptrbox

.DEFAULT_GOAL := help

.PHONY: help build install lint govet shlint test gotest check golden smoke clean

build: ## Compile the CLI to dist/ptrbox
	@$(GO) build -o $(BIN) ./cmd/ptrbox

install: ## Install ptrbox into your Go bin directory
	@$(GO) install ./cmd/ptrbox

help: ## Show this help
	@grep -hE '^[a-z][a-z-]*:.*##' $(MAKEFILE_LIST) | sed 's/:[^#]*## /\t/' | expand -t 12

lint: govet shlint ## Vet the Go code and shellcheck the guest scripts

govet:
	@$(GO) vet ./...

shlint:
	@tests/lint.sh

test: gotest ## Run unit + simulation tests

gotest:
	@$(GO) test ./...

check: lint test ## Everything that runs without a Mac

golden: ## Regenerate the golden rendered VM configs - then READ THE DIFF
	@$(GO) test ./internal/render -run TestGolden -update
	@git diff --stat -- tests/golden || true

clean: ## Remove build output
	@rm -rf dist

smoke: build ## Real VM cycle on macOS (destroys/recreates the sandbox-test VM)
	@[ "$$(uname -s)" = "Darwin" ] || { echo "smoke: macOS only" >&2; exit 1; }
	$(BIN) install
	-$(BIN) rm sandbox-test
	$(BIN) new sandbox-test

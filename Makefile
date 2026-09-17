# =============================================================================
# ptrbox - development entry points.
#
#   make build   compile the CLI to dist/ptrbox
#   make lint    go vet + shell syntax/shellcheck, runs anywhere
#   make test    unit + simulation tests against a fake lima, runs anywhere
#                (no Mac, no VM, no network)
#   make cross   compile for Windows and macOS from wherever this is, and vet
#                the Windows build - the platform files are otherwise first
#                compiled on the machine they are for
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

.PHONY: help build install lint govet shlint cross test gotest check golden smoke clean windows-capture

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

# vet as well as build for Windows: vet compiles the tests too, and a test file
# that does not build there is one nobody can run on the PC. A plain build is
# enough for macOS, whose files are the unix ones the native run already vets
# (and on a Mac this line is the native build, which costs nothing).
cross: ## Build for Windows and macOS, vet the Windows build
	@GOOS=windows $(GO) build ./...
	@GOOS=windows $(GO) vet ./...
	@GOOS=darwin $(GO) build ./...

check: lint cross test ## Everything that runs without a Mac

golden: ## Regenerate the golden rendered VM configs - then READ THE DIFF
	@$(GO) test ./internal/render -run TestGolden -update
	@git diff --stat -- tests/golden || true

# The credential probe as a Windows test binary, so the PC needs neither Go nor
# a checkout: the capture folder is copied out of \\wsl$ with the .exe in it.
# GOARCH is the machine's - WSL runs on the PC it is building for.
windows-capture: ## Build the step-0 credential probe into tests/windows-capture
	@GOOS=windows $(GO) test -c -o tests/windows-capture/credread-probe.exe ./internal/cli
	@echo "built tests/windows-capture/credread-probe.exe - now copy that folder to Windows"

clean: ## Remove build output
	@rm -rf dist

smoke: build ## Real VM cycle on macOS (sandbox-test VM; removed on success, kept on failure)
	@[ "$$(uname -s)" = "Darwin" ] || { echo "smoke: macOS only" >&2; exit 1; }
	$(BIN) install
	-$(BIN) rm sandbox-test
	$(BIN) new sandbox-test
	$(BIN) rm sandbox-test

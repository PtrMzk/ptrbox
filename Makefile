# =============================================================================
# ptrbox - development entry points.
#
#   make lint    syntax + shellcheck, runs anywhere
#   make test    unit + simulation tests against stubbed lima/brew/keychain,
#                runs anywhere (no Mac, no VM, no network)
#   make smoke   the real VM cycle - macOS + lima only, destroys and recreates
#                a scratch VM, takes minutes
#
# lint and test are the automated feedback loop: they must pass before a
# commit. smoke is the only step that needs actual hardware.
# =============================================================================
SHELL := /bin/bash

.DEFAULT_GOAL := help

.PHONY: help lint test check smoke

help: ## Show this help
	@grep -hE '^[a-z][a-z-]*:.*##' $(MAKEFILE_LIST) | sed 's/:[^#]*## /\t/' | expand -t 12

lint: ## Syntax-check and shellcheck every shell file
	@tests/lint.sh

test: ## Run unit + simulation tests (bats)
	@tests/run.sh

check: lint test ## Everything that runs without a Mac

smoke: ## Real VM cycle on macOS (destroys/recreates the sandbox-test VM)
	@[ "$$(uname -s)" = "Darwin" ] || { echo "smoke: macOS only" >&2; exit 1; }
	bin/ptrbox install
	-bin/ptrbox rm sandbox-test
	bin/ptrbox new sandbox-test

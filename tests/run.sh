#!/usr/bin/env bash
# =============================================================================
# run.sh - run the bats test suite.
#
# Everything here runs against stubs (tests/stubs/), so it works on Linux, in
# CI, and on a Mac with no VM running. Requires bats-core:
#   macOS:  brew install bats-core
#   Linux:  apt install bats   (or: npm i -g bats)
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v bats >/dev/null 2>&1; then
  echo "test: bats not installed - brew install bats-core" >&2
  exit 1
fi

set -- tests/*.bats
if [ ! -e "$1" ]; then
  echo "test: no .bats files yet" >&2
  exit 0
fi

exec bats "$@"

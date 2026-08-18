#!/usr/bin/env bash
# =============================================================================
# lint.sh - syntax-check and shellcheck the GUEST-side shell scripts.
#
# The host CLI is Go; `go vet` covers it. What is left in bash is the code that
# runs inside the VMs - the provisioning steps and the verification script -
# because bash is the native language of a Debian guest and vm/ is the
# documented review surface for what a sandbox contains.
#
# `bash -n` always runs (it needs nothing but bash). shellcheck runs when it is
# installed and is skipped with a warning otherwise, so a missing optional tool
# never blocks a contributor:
#   macOS:  brew install shellcheck
#   Linux:  apt install shellcheck   (or: npm i -g shellcheck)
#
# These scripts are rendered into a Lima config before they run, so they carry
# double-underscore placeholders that neither tool minds.
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."

set -- vm/verify.sh vm/verify-proxy.sh vm/provision/*.sh vm/provision-proxy/*.sh
if [ "$#" -eq 0 ] || [ ! -e "$1" ]; then
  echo "lint: no guest scripts found" >&2
  exit 1
fi

status=0

echo "== bash -n ($# guest scripts)"
for f in "$@"; do
  bash -n "$f" || status=1
done

if command -v shellcheck >/dev/null 2>&1; then
  echo "== shellcheck ($(shellcheck --version | awk '/version:/ {print $2}'))"
  shellcheck "$@" || status=1
else
  echo "== shellcheck SKIPPED (not installed - brew install shellcheck)" >&2
fi

[ "$status" -eq 0 ] && echo "lint: OK"
exit "$status"

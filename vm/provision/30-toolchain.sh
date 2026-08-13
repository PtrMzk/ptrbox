#!/bin/bash
# =============================================================================
# 30-toolchain.sh - developer toolchain. Runs as the unprivileged agent user.
#
# Everything here downloads from hosts that are deliberately NOT on the Squid
# allowlist, which is exactly why it must happen on the pre-firewall boot -
# and why the done-marker guard is mandatory.
# =============================================================================
set -eux

if [ -f "$HOME/.ptrbox/toolchain.done" ]; then
  exit 0
fi

# Node LTS via nvm. nvm is a shell function, not a binary, so after installing
# we must source it into this script's shell before calling it. The version pin
# is the nvm installer, not node - node itself is "whatever LTS is current",
# part of the always-fresh design.
curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash
export NVM_DIR="$HOME/.nvm"
# shellcheck source=/dev/null
. "$NVM_DIR/nvm.sh"
nvm install --lts

# uv - Python package/project manager. Also installs Python versions on demand;
# those downloads come from GitHub releases, which IS on the allowlist, so
# `uv python install` keeps working after the firewall comes up.
curl -LsSf https://astral.sh/uv/install.sh | sh

# Claude Code. Installs the binary to ~/.local/bin.
curl -fsSL https://claude.ai/install.sh | bash

mkdir -p "$HOME/.ptrbox"
touch "$HOME/.ptrbox/toolchain.done"

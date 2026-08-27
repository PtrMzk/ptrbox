#!/bin/bash
# =============================================================================
# 30-toolchain.sh - developer toolchain. Runs as the unprivileged agent user.
#
# Which language runtimes land here is host-side config (one boolean per
# runtime: PTRBOX_GO, PTRBOX_NODE, PTRBOX_UV, all off by default), resolved
# into the list rendered in below and validated on the host first - the same
# rule as the apt
# list in 15-extra-packages.sh, and for the same reason: a runtime list read
# from the repo mount would let the agent decide what its own sandbox contains.
#
# Claude Code is not part of that list. It is installed unconditionally,
# because it is what a ptrbox VM exists to run, and it is a native binary that
# needs neither node nor Python.
#
# This must happen on the pre-firewall boot, and the done-marker guard is
# therefore mandatory. The reason is narrower than it looks: several of the
# downloads below are from hosts that ARE on the allowlist
# (.githubusercontent.com, astral.sh, claude.ai). The ones that are not are
# `nvm install`, which fetches the node tarball from nodejs.org, and Go's
# tarball from go.dev/dl.google.com - all deliberately absent from the
# allowlist, because provisioning is the only thing that needs them. So a
# post-firewall re-run would get through most of this file and then hang.
#
# The installer URLs and the nvm pin are deliberately NOT config. They are
# `curl | bash` sources; changing one should be an edit here, visible as a
# golden diff under tests/golden/, rather than a line in a config file.
# =============================================================================
set -eux

if [ -f "$HOME/.ptrbox/toolchain.done" ]; then
  exit 0
fi

# Rendered from host config; TOOLCHAIN is empty when neither runtime is wanted.
TOOLCHAIN="__TOOLCHAIN__"
NODE_VERSION="__NODE_VERSION__"

mkdir -p "$HOME/.ptrbox"

# What was ASKED FOR, recorded before anything is installed. vm/verify.sh reads
# this file and requires every name in it to be on PATH, so a runtime that
# failed to install is a failed `ptrbox new` rather than a tool someone
# discovers is missing a week later. Writing it first is what makes it an
# assertion instead of a self-report: if an install below dies, set -e stops
# the script here, and the record still says the runtime was wanted.
printf '%s\n' "$TOOLCHAIN" >"$HOME/.ptrbox/toolchain"

# Substring match on a padded list, so "node" cannot match "nodemon".
want() {
  case " $TOOLCHAIN " in
  *" $1 "*) return 0 ;;
  *) return 1 ;;
  esac
}

if want go; then
  # The official tarball rather than Debian's golang-go: this keeps Go
  # user-owned under $HOME, like nvm and uv, in a VM that has no root after
  # provisioning - and trixie's package trails upstream. The version is asked
  # for rather than pinned (the same always-fresh argument as `nvm install
  # --lts`), and the architecture is read rather than assumed.
  go_version="$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -n 1)"
  go_arch="$(dpkg --print-architecture)"
  mkdir -p "$HOME/.local/bin"
  curl -fsSL "https://dl.google.com/go/${go_version}.linux-${go_arch}.tar.gz" |
    tar -C "$HOME/.local" -xzf -
  # ~/.local/bin is on PATH for every login shell (40-userenv.sh), which is
  # what puts go where vm/verify.sh looks for it.
  ln -sf "$HOME/.local/go/bin/go" "$HOME/.local/bin/go"
  ln -sf "$HOME/.local/go/bin/gofmt" "$HOME/.local/bin/gofmt"
fi

if want node; then
  # nvm is a shell function, not a binary, so after installing we must source
  # it into this script's shell before calling it. The pin is on the nvm
  # installer; which node it then fetches is PTRBOX_NODE_VERSION, defaulting
  # to "lts" - part of the always-fresh design.
  curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash
  export NVM_DIR="$HOME/.nvm"
  # shellcheck source=/dev/null
  . "$NVM_DIR/nvm.sh"
  if [ "$NODE_VERSION" = lts ]; then
    nvm install --lts
  else
    nvm install "$NODE_VERSION"
  fi
fi

if want uv; then
  # uv - Python package/project manager. Also installs Python versions on
  # demand; those downloads come from GitHub releases, which IS on the
  # allowlist, so `uv python install` keeps working after the firewall is up.
  curl -LsSf https://astral.sh/uv/install.sh | sh
fi

# Claude Code. Installs the binary to ~/.local/bin.
curl -fsSL https://claude.ai/install.sh | bash

touch "$HOME/.ptrbox/toolchain.done"

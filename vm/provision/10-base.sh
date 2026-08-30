#!/bin/bash
# =============================================================================
# 10-base.sh - OS packages. Runs as root inside the guest, on EVERY boot.
#
# APT-FAMILY ONLY. This script is shared by every supported distro
# (PTRBOX_DISTRO: debian13, ubuntu2404) because both are apt-based and, since
# Debian's time_t transition, use identical package names - including the t64
# suffixes below, which are the same on trixie and noble. Supporting a dnf or
# pacman distro means a sibling script selected by distro, not edits here.
#
# Rendered into the generated Lima config; never executed on the host.
# Network is open while this runs (the firewall is enabled but not started
# until the post-provision reboot), which is why the done-marker guard matters:
# without it, post-firewall boots would hang here until cloud-init gives up.
# =============================================================================
set -eux

# -e: stop on first error. -u: error on unset variables. -x: echo each command
# (shows up in `journalctl -u cloud-final` inside the guest).

# An argument with the real path as its default, purely so the test suite can
# run this against a state directory it controls - ptrbox passes nothing.
state="${1:-/var/lib/ptrbox}"

if [ -f "$state/base.done" ]; then
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive # apt must never prompt in a script

apt-get update
# Apply current security patches at provision time. Lima's image cache is
# keyed by URL and never expires, so the cached base image goes stale - this
# line is what actually keeps fresh VMs patched.
apt-get upgrade -y

# curl/git: installers + cloning.  build-essential: gcc/make, needed when npm
# or pip packages compile native extensions.  ca-certificates: TLS trust roots
# so https works at all.  jq: JSON wrangling in shell.  nftables: the firewall
# itself.  tmux: long-running agent sessions that survive ssh disconnects.
apt-get install -y curl git build-essential ca-certificates jq nftables tmux

mkdir -p "$state"

# Chromium runtime dependencies (Playwright headless browser testing), only
# when PTRBOX_PLAYWRIGHT asked for them. This is what
# `npx playwright install-deps chromium` would install - listed explicitly
# because the VM has no root post-provision, so anything the agent's toolchain
# needs at the OS level must be baked in here. (t64 suffixes come from Debian's
# time_t transition; if apt errors on one, the non-t64 name is the fallback.)
#
# It is the largest package set in the guest by a wide margin, and nothing
# else in a sandbox uses GTK, X11 or desktop fonts - so a VM that will never
# drive a browser does not carry them. The same flag gates the Playwright CDN
# group in the egress allowlist, which is what keeps the two halves of this
# capability from disagreeing about whether the VM has it.
PLAYWRIGHT="__PLAYWRIGHT__"
if [ "$PLAYWRIGHT" = "true" ]; then
  playwright_packages="libnss3 libnspr4 libatk1.0-0t64 libatk-bridge2.0-0t64
    libcups2t64 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1
    libxfixes3 libxrandr2 libgbm1 libasound2t64 libatspi2.0-0t64
    libx11-xcb1 libxcursor1 libgtk-3-0t64 fonts-liberation"
  # Recorded BEFORE the install, so the record is a request rather than a
  # self-report: if apt dies, `set -e` stops the script here and the file
  # still says these were wanted. vm/verify.sh reads it and checks each one
  # actually arrived - the same shape as the toolchain record next door.
  printf '%s\n' $playwright_packages >"$state/playwright-packages"
  # shellcheck disable=SC2086 # deliberate word splitting: a package list
  apt-get install -y $playwright_packages
else
  rm -f "$state/playwright-packages"
fi

touch "$state/base.done"

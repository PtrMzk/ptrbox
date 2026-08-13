#!/bin/bash
# =============================================================================
# 10-base.sh - OS packages. Runs as root inside the guest, on EVERY boot.
#
# Rendered into the generated Lima config; never executed on the host.
# Network is open while this runs (the firewall is enabled but not started
# until the post-provision reboot), which is why the done-marker guard matters:
# without it, post-firewall boots would hang here until cloud-init gives up.
# =============================================================================
set -eux

# -e: stop on first error. -u: error on unset variables. -x: echo each command
# (shows up in `journalctl -u cloud-final` inside the guest).

if [ -f /var/lib/ptrbox/base.done ]; then
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

# Chromium runtime dependencies (Playwright headless browser testing). This is
# what `npx playwright install-deps chromium` would install - listed explicitly
# because the VM has no root post-provision, so anything the agent's toolchain
# needs at the OS level must be baked in here. (t64 suffixes come from Debian's
# time_t transition; if apt errors on one, the non-t64 name is the fallback.)
apt-get install -y libnss3 libnspr4 libatk1.0-0t64 libatk-bridge2.0-0t64 \
  libcups2t64 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 \
  libxfixes3 libxrandr2 libgbm1 libasound2t64 libatspi2.0-0t64 \
  libx11-xcb1 libxcursor1 libgtk-3-0t64 fonts-liberation

mkdir -p /var/lib/ptrbox
touch /var/lib/ptrbox/base.done

#!/bin/bash
# =============================================================================
# 15-extra-packages.sh - user-configured apt packages. Runs as root inside the
# guest, on EVERY boot.
#
# The list is substituted in at render time from HOST-side config
# (PTRBOX_EXTRA_PACKAGES in ~/.config/ptrbox/config, or the environment) and
# validated on the host first. Deliberately never read from a file at boot,
# and especially not from the repo mount: a repo-provided package list would
# let the agent install software into its own sandbox. Which packages exist in
# a VM stays a human, host-side decision.
#
# A separate script rather than an edit surface in 10-base.sh, so per-user
# additions never mean forking the shared base list. Changing the list means
# re-creating the VM, like every other provisioning change.
# =============================================================================
set -eux

if [ -f /var/lib/ptrbox/extra-packages.done ]; then
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive # apt must never prompt in a script

# Rendered from host config; empty when no extra packages are configured.
EXTRA_PACKAGES="__EXTRA_PACKAGES__"

if [ -n "$EXTRA_PACKAGES" ]; then
  # No apt-get update here: 10-base.sh ran one moments earlier in this same
  # boot (both scripts only do real work on boot 1, before their done markers
  # exist).
  # shellcheck disable=SC2086  # word splitting is the point: one arg per package
  apt-get install -y $EXTRA_PACKAGES
fi

mkdir -p /var/lib/ptrbox
touch /var/lib/ptrbox/extra-packages.done

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
#
# The host can only validate the SHAPE of a name; whether trixie has a package
# called that is a question only apt can answer, and only in here. A name that
# is well-formed but wrong - a typo, a package that exists on noble and not on
# trixie, a runtime whose Debian name is not what anyone guesses - used to fail
# inside this script on boot 1, where nobody is watching: `ptrbox new` still
# printed that the VM was ready, and the missing tool turned up days later as
# something that could not run. So each failure now leaves a marker file, and
# vm/verify.sh turns that marker into a failed check, which fails `ptrbox new`.
# =============================================================================
set -eux

# An argument with the real path as its default, purely so the test suite can
# run this for real against a stub apt - ptrbox passes nothing.
state="${1:-/var/lib/ptrbox}"
done_marker="$state/extra-packages.done"
fail_marker="$state/extra-packages.failed"

if [ -f "$done_marker" ]; then
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive # apt must never prompt in a script

# Rendered from host config; empty when no extra packages are configured.
EXTRA_PACKAGES="__EXTRA_PACKAGES__"

mkdir -p "$state"
# Cleared first, so the marker always describes THIS boot. The script has no
# done marker until it succeeds, so a failed boot 1 re-runs on the reboot that
# raises the firewall - and every step below works off the package lists on
# disk, so that re-run needs no network and reaches the same verdict.
rm -f "$fail_marker"

# fail records why, then stops the boot script. The marker is what survives to
# be read: this script's own exit status is lost in cloud-init's log.
fail() {
  printf '%s\n' "$1" >"$fail_marker"
  printf 'ptrbox: extra packages: %s\n' "$1" >&2
  exit 1
}

if [ -n "$EXTRA_PACKAGES" ]; then
  # Resolve every name BEFORE installing any of them, so a typo in the third
  # package does not leave the first two installed and the VM half-configured.
  # --simulate resolves against the lists on disk (no network) and covers more
  # than existence: a version pin that matches nothing, or a dependency that
  # cannot be satisfied on this suite, fails here too.
  #
  # No apt-get update here: 10-base.sh ran one moments earlier in this same
  # boot (both scripts only do real work on boot 1, before their done markers
  # exist).
  unresolved=""
  for pkg in $EXTRA_PACKAGES; do
    if ! apt-get install -y --simulate "$pkg" >/dev/null 2>&1; then
      unresolved="$unresolved $pkg"
    fi
  done
  if [ -n "$unresolved" ]; then
    fail "no such package on this distro:$unresolved (check the name for this suite)"
  fi

  # shellcheck disable=SC2086  # word splitting is the point: one arg per package
  if ! apt-get install -y $EXTRA_PACKAGES; then
    fail "apt-get install failed for: $EXTRA_PACKAGES"
  fi

  # Belt and braces: resolvable and apt-get happy still is not installed. This
  # is the check that holds whatever the reason - a mirror that went away
  # mid-install, a postinst that failed - because it asks dpkg the question the
  # user actually cares about.
  absent=""
  for pkg in $EXTRA_PACKAGES; do
    status="$(dpkg-query -W -f='${db:Status-Status}' "${pkg%%=*}" 2>/dev/null || true)"
    if [ "$status" != installed ]; then
      absent="$absent $pkg"
    fi
  done
  if [ -n "$absent" ]; then
    fail "installed by apt-get but missing afterwards:$absent"
  fi
fi

touch "$done_marker"

#!/bin/bash
# =============================================================================
# 05-apt.sh - make apt cheap before anything calls it. Runs as root, first.
#
# Boot 1 spends most of its minutes in apt (item 57 measures it), and some of
# that is work a disposable sandbox has no use for. Everything here is a
# setting apt and dpkg read on their next run, which is why this script is
# numbered ahead of 10-base.sh: a knob turned after the first apt-get is a
# knob turned for nothing.
#
# Two things this does NOT change. `apt-get upgrade -y` stays in 10-base.sh -
# it is what keeps a VM built from Lima's never-expiring image cache patched,
# and dropping it is a security decision, not a speedup. And the packages a
# user names in PTRBOX_EXTRA_PACKAGES keep apt's default recommends, because
# that list is theirs.
#
# Linted, not executed, by the suite: everything below writes under /etc.
# =============================================================================
set -eux

state="${1:-/var/lib/ptrbox}"

if [ -f "$state/apt.done" ]; then
  exit 0
fi

mkdir -p "$state"
# Timing record; see 10-base.sh.
ptrbox_t0="$(date +%s)"
trap 'printf "05-apt %s %s\n" "$ptrbox_t0" "$(date +%s)" >>"$state/timings" || true' EXIT

# --- the apt timers, off before the first apt-get -----------------------------

# Masked rather than disabled: these are the apt machinery, and a package
# upgrade is exactly the event that re-enables them. unattended-upgrades cannot
# reach the Debian mirrors once the firewall is up (they are not on the
# allowlist, and apt does not use the proxy), so it would wake, fail, and
# sleep - a root process doing nothing but retrying. Patching happens at
# provision time in 10-base.sh, and the image URLs track current builds.
#
# Here rather than in 25-services.sh, where the rest of the trim lives, because
# apt-daily.timer is Persistent= and fires on a fresh image's first boot - so
# on boot 1 it races the provisioning apt-get for the dpkg lock. --now stops a
# timer that has already fired; the explicit stop catches a service it already
# started.
systemctl mask --now unattended-upgrades.service apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
systemctl stop apt-daily.service apt-daily-upgrade.service 2>/dev/null || true

# --- dpkg: no docs, no fsync per file ----------------------------------------

# force-unsafe-io skips dpkg's per-file fsync. The cost is a torn install if
# the VM dies mid-unpack, and a VM that dies during boot 1 is re-created, not
# repaired. /usr/share/doc goes except copyright files, which is the one part
# of it anyone is obliged to keep. Man pages stay: the agent reads them.
mkdir -p /etc/dpkg/dpkg.cfg.d
cat >/etc/dpkg/dpkg.cfg.d/01-ptrbox <<'DPKG'
# Written by ptrbox: this VM is disposable and re-created, never repaired.
force-unsafe-io
path-exclude=/usr/share/doc/*
path-include=/usr/share/doc/*/copyright
DPKG

# man-db rebuilds its index in a dpkg trigger after every install - the
# classic apt slowdown, and item 50 only disabled the daily timer. Turning the
# debconf answer off skips the trigger while leaving `man` itself working;
# the index is simply not maintained, which for pages read by name is nothing.
echo 'man-db man-db/auto-update boolean false' | debconf-set-selections

# --- apt: no translations -----------------------------------------------------

mkdir -p /etc/apt/apt.conf.d
cat >/etc/apt/apt.conf.d/01-ptrbox <<'APT'
// Written by ptrbox: no package-description translations to download.
Acquire::Languages "none";
APT

touch "$state/apt.done"

#!/bin/bash
# =============================================================================
# 25-services.sh - turn off what a sandbox does not run. Runs as root.
#
# Attack surface reduction, not privilege: 90-harden.sh takes root away, this
# takes away things that listen or wake up. Nothing here is load-bearing for a
# sandbox, and each one is either unreachable by design or unable to do its job
# behind the firewall.
#
# Everything takes effect on the post-provision reboot rather than immediately,
# which is why nothing here restarts a unit. That is the same bargain
# 20-firewall.sh makes next door: `ptrbox new` reboots, and the reboot is when
# the guest becomes what the template says it is.
#
# NOT disabled, deliberately:
#   apparmor            the one service in the list actively helping
#   fstrim.timer        the diffdisk is sparse, and fstrim is what lets the Mac
#                       reclaim space the guest has freed
#   systemd-resolved    still the stub resolver; only its multicast protocols
#                       go (see below)
# =============================================================================
set -eux

state="${1:-/var/lib/ptrbox}"

if [ -f "$state/services.done" ]; then
  exit 0
fi

mkdir -p "$state"
# Timing record; see 10-base.sh.
ptrbox_t0="$(date +%s)"
trap 'printf "25-services %s %s\n" "$ptrbox_t0" "$(date +%s)" >>"$state/timings" || true' EXIT

# --- multicast name resolution ------------------------------------------------

# LLMNR and mDNS listen on every interface (udp/tcp 5355, udp 5353) and resolve
# names by shouting on the local segment. This VM resolves through pinned
# resolvers and reaches everything else through the proxy, so both protocols
# answer a question nobody asks - while LLMNR response spoofing is a standard
# lateral-movement technique, and both are parsers reachable from the network.
#
# Turning them off also removes the four `bind: address already in use`
# warnings for port 5355 that lima logs on every single boot: they are lima
# failing to forward a guest port that, after this, no longer exists.
mkdir -p /etc/systemd/resolved.conf.d
cat >/etc/systemd/resolved.conf.d/ptrbox-no-multicast.conf <<'RESOLVED'
# Written by ptrbox: a sandbox has no use for link-local name resolution.
[Resolve]
LLMNR=no
MulticastDNS=no
RESOLVED

# --- units with nothing to do -------------------------------------------------

# The apt timers and unattended-upgrades are masked in 05-apt.sh, ahead of the
# first apt-get, because on boot 1 they contend with it for the dpkg lock.

# Disabled rather than masked: nothing re-enables these, and masking would be
# a stronger claim than the reasoning supports.
#
#   systemd-pstore          firmware crash-dump collection, for hardware
#   uuidd.socket            UUID daemon nothing here asks for
#   cloud-init-hotplugd     device hotplug, in a VM whose devices are fixed
#   man-db / dpkg-db-backup / e2scrub_all
#                           housekeeping timers for a machine meant to live for
#                           years; this one is disposable and re-created
for unit in \
  systemd-pstore.service \
  uuidd.socket \
  cloud-init-hotplugd.socket \
  man-db.timer \
  dpkg-db-backup.timer \
  e2scrub_all.timer; do
  # Absent is fine and not an error: debian13 and ubuntu2404 ship slightly
  # different unit sets, and a unit that is not installed is already off.
  systemctl disable "$unit" 2>/dev/null || true
done

touch "$state/services.done"

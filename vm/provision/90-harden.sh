#!/bin/bash
# =============================================================================
# 90-harden.sh - take root away and keep it away. Runs as root in the guest.
#
# THE step that makes the firewall real. Cloud images grant the default user
# passwordless sudo (NOPASSWD:ALL) - convenient for normal use, fatal here:
# the agent could run `sudo nft flush ruleset` and walk straight past the
# wall. (The NAT would then hand it direct, unfiltered internet - Squid only
# filters what is forced through it.)
#
# Consequence: NO root access for the agent inside this VM, ever. Any
# root-level change (new apt package, firewall tweak) = edit the template,
# delete the VM, re-create. That is the intended workflow; the template stays
# the single source of truth.
#
# Two halves, and the second is why this file is no longer called 90-nosudo.sh:
# removing the sudoers entry takes away the PERMISSION, and stripping the
# setuid bits takes away the MECHANISM. A setuid-root binary is precisely the
# thing that turns "the agent has no root" back into "the agent has root", so
# in a VM whose whole model rests on that sentence, every one left executable
# is a standing bet on its CVE history.
#
# The one exception, and it is the whole of the two-user model: on a backend
# whose own daemon needs sudo in the guest on every boot (Multipass, for its
# sshfs mount and its snap), the account the daemon logs in as keeps its
# NOPASSWD grant and sudo keeps its bit. That account is DAEMON_USER below,
# rendered by the host; the agent runs as a different user with no grant, no
# password and no route to it, and vm/verify.sh asserts all three. On lima
# DAEMON_USER is empty: one account, and it loses root.
#
# Deliberately NOT guarded by a done-marker: this runs on every boot, so both
# halves are re-asserted even if something in the guest puts a sudoers file
# back - or, the case that actually happens, an apt upgrade reinstalls a
# package and restores its setuid bit.
# =============================================================================
set -eux

# Arguments with the real paths as their defaults, purely so the test suite
# can run this against directories it controls - ptrbox passes nothing. The
# third is a prefix for the binaries below, "" in a guest.
state="${1:-/var/lib/ptrbox}"
sudoers_dir="${2:-/etc/sudoers.d}"
root="${3:-}"

# Empty on a single-account backend. Fixed on the host, never read from the
# guest: a value the guest could supply is a value the agent could supply.
DAEMON_USER="__DAEMON_USER__"

# Timing record; see 10-base.sh. No guard here, so this is written on every
# boot; the host sums the lines for one name, and this one is milliseconds.
mkdir -p "$state"
ptrbox_t0="$(date +%s)"
trap 'printf "90-harden %s %s\n" "$ptrbox_t0" "$(date +%s)" >>"$state/timings" || true' EXIT

# --- the permission ----------------------------------------------------------

# Every drop-in that grants NOPASSWD to anyone but the daemon user goes. With
# no daemon user that is every one of them, cloud-init's 90-cloud-init-users
# included; with one, a drop-in survives only if each of its NOPASSWD lines
# starts with that account's name - a file mixing the daemon user with anyone
# else is removed whole, which costs the daemon its sudo rather than handing
# root to the someone else. Drop-ins without NOPASSWD are not this script's
# business: the default sudoers still asks for a password, and the agent has
# none.
for dropin in "$sudoers_dir"/*; do
  [ -f "$dropin" ] || continue
  grep -q 'NOPASSWD' "$dropin" || continue
  if [ -n "$DAEMON_USER" ] && ! grep 'NOPASSWD' "$dropin" | grep -vqE "^[[:space:]]*${DAEMON_USER}[[:space:]]"; then
    continue
  fi
  rm -f "$dropin"
done

# --- the mechanism -----------------------------------------------------------

# Every binary below was checked against a live guest and has no caller here.
# The package stays installed in each case; only the bit goes, which is what
# keeps cloud-init's dependency on sudo satisfied while making the binary
# unable to escalate. `sudo -n true` still fails, so vm/verify.sh's existing
# check reads the same.
#
#   sudo            no sudoers entry, and cloud-init runs as root - it depends
#                   on the package, never invokes the command. KEPT when there
#                   is a daemon user: it is that account's only channel to
#                   root, and its grant is the one this script just preserved.
#   su              nothing switches users in a sandbox
#   passwd          sshd is PasswordAuthentication no
#   chfn chsh       finger info and login shell
#   gpasswd newgrp  group management
#   chage expiry    password aging (setgid shadow)
#   ssh-keysign     host-based auth, which sshd is not configured for
#   polkit-agent-helper-1
#                   headless VM; nothing raises a polkit prompt
#   mount umount    the repo mount arrives as root - via fstab and a systemd
#                   mount unit, or via the daemon user's sudo - not through
#                   the setuid binary. The agent loses manual mounting, which
#                   it has no business doing.
#
# Deliberately LEFT setuid, because each has a caller or a cost:
#   unix_chkpwd     PAM's password checker, on the login path; small upside
#   dbus-daemon-launch-helper
#                   systemd-logind depends on dbus, and breaking activation
#                   risks ssh sessions
#   ssh-agent       setgid _ssh is anti-ptrace hardening, not a privilege
for binary in \
  /usr/bin/sudo \
  /usr/bin/su \
  /usr/bin/passwd \
  /usr/bin/chfn \
  /usr/bin/chsh \
  /usr/bin/gpasswd \
  /usr/bin/newgrp \
  /usr/bin/chage \
  /usr/bin/expiry \
  /usr/lib/openssh/ssh-keysign \
  /usr/lib/polkit-1/polkit-agent-helper-1 \
  /usr/bin/mount \
  /usr/bin/umount; do
  if [ -n "$DAEMON_USER" ] && [ "$binary" = /usr/bin/sudo ]; then
    continue
  fi
  # Absent is fine and not an error: the two distros ship slightly different
  # sets, and a binary that is not there cannot be escalated through.
  [ -e "$root$binary" ] || continue
  chmod u-s,g-s "$root$binary"
done

exit 0

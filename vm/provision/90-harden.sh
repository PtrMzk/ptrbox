#!/bin/bash
# =============================================================================
# 90-harden.sh - take root away and keep it away. Runs as root in the guest.
#
# THE step that makes the firewall real. Lima images grant the default user
# passwordless sudo (NOPASSWD:ALL) - convenient for normal Lima use, fatal
# here: the agent could run `sudo nft flush ruleset` and walk straight past the
# wall. (vzNAT would then hand it direct, unfiltered internet - Squid only
# filters what is forced through it.)
#
# Consequence: NO root access inside this VM, ever. Any root-level change (new
# apt package, firewall tweak) = edit the template, delete the VM, re-create.
# That is the intended workflow; the template stays the single source of truth.
#
# Two halves, and the second is why this file is no longer called 90-nosudo.sh:
# removing the sudoers entry takes away the PERMISSION, and stripping the
# setuid bits takes away the MECHANISM. A setuid-root binary is precisely the
# thing that turns "the agent has no root" back into "the agent has root", so
# in a VM whose whole model rests on that sentence, every one left executable
# is a standing bet on its CVE history.
#
# Deliberately NOT guarded by a done-marker: this runs on every boot, so both
# halves are re-asserted even if something in the guest puts a sudoers file
# back - or, the case that actually happens, an apt upgrade reinstalls a
# package and restores its setuid bit.
# =============================================================================
set -eux

# --- the permission ----------------------------------------------------------

rm -f /etc/sudoers.d/90-cloud-init-users
# Belt-and-suspenders: some cloud images name the sudoers drop-in differently;
# remove anything granting NOPASSWD.
grep -rl 'NOPASSWD' /etc/sudoers.d/ 2>/dev/null | xargs -r rm -f
# grep exits 1 when it matches nothing, which is the normal steady state.

# --- the mechanism -----------------------------------------------------------

# Every binary below was checked against a live guest and has no caller here.
# The package stays installed in each case; only the bit goes, which is what
# keeps cloud-init's dependency on sudo satisfied while making the binary
# unable to escalate. `sudo -n true` still fails, so vm/verify.sh's existing
# check reads the same.
#
#   sudo            no sudoers entry, and cloud-init runs as root - it depends
#                   on the package, never invokes the command
#   su              nothing switches users in a sandbox
#   passwd          sshd is PasswordAuthentication no
#   chfn chsh       finger info and login shell
#   gpasswd newgrp  group management
#   chage expiry    password aging (setgid shadow)
#   ssh-keysign     host-based auth, which sshd is not configured for
#   polkit-agent-helper-1
#                   headless VM; nothing raises a polkit prompt
#   mount umount    the repo mount arrives via fstab and a systemd mount unit,
#                   as root - not through the setuid binary. The agent loses
#                   manual mounting, which it has no business doing.
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
  # Absent is fine and not an error: the two distros ship slightly different
  # sets, and a binary that is not there cannot be escalated through.
  [ -e "$binary" ] || continue
  chmod u-s,g-s "$binary"
done

exit 0

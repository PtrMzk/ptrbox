#!/bin/bash
# =============================================================================
# 90-nosudo.sh - remove passwordless root. Runs as root inside the guest.
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
# Deliberately NOT guarded by a done-marker: this runs on every boot, so the
# removal is re-asserted even if something in the guest puts a sudoers file
# back.
# =============================================================================
set -eux

rm -f /etc/sudoers.d/90-cloud-init-users
# Belt-and-suspenders: some cloud images name the sudoers drop-in differently;
# remove anything granting NOPASSWD.
grep -rl 'NOPASSWD' /etc/sudoers.d/ 2>/dev/null | xargs -r rm -f
# grep exits 1 when it matches nothing, which is the normal steady state.
exit 0

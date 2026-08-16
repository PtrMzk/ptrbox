#!/bin/bash
# =============================================================================
# 10-squid.sh - the proxy VM's only provisioning step. Runs as root inside
# the guest, on EVERY boot (Lima installs provision scripts per-boot), hence
# the done-marker guard: without it a later boot would hang on apt until
# cloud-init gives up.
#
# Installs squid and NOTHING else - no sandbox toolchain, no extras. The real
# squid.conf and allowlist are pushed by lib/proxy.sh after boot; they are
# host-side artifacts, which is what keeps this VM disposable.
# =============================================================================
set -eux

if [ -f /var/lib/ptrbox/squid.done ]; then
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive

apt-get update
# This VM terminates traffic from every sandbox, so of all the guests it is
# the one that must not boot with known-patched holes. Lima's image cache
# never expires; this line is what keeps a fresh proxy current.
apt-get upgrade -y

apt-get install -y squid

# The allowlist must exist before any config referencing it can parse. Empty
# on purpose - with the distro's stock config still active, squid serves
# nothing until ptrbox pushes the real config and allowlist.
touch /etc/squid/allowed_domains.txt

mkdir -p /var/lib/ptrbox
touch /var/lib/ptrbox/squid.done

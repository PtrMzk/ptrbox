#!/bin/bash
# =============================================================================
# verify.sh - assert a sandbox VM's security properties. Runs INSIDE the guest
# (ptrbox pipes it in after creating a VM); never run this on the host.
#
# Exits non-zero if any check fails. That matters: the original setup printed
# FAIL lines and still reported success, so a VM with the wall down looked
# fine as long as nobody read the output carefully.
#
# Note on mounts: virtiofs mounts don't show host paths in `mount` output (they
# appear as opaque lima-<hash> tags), so host exposure is checked by the
# absence of the /Users path Lima would mirror a home mount to, plus a strict
# count of virtiofs mounts.
# =============================================================================
set -u

fails=0

ok() { printf '  %-22s OK\n' "$1"; }
bad() {
  printf '  %-22s FAIL - %s\n' "$1" "$2"
  fails=$((fails + 1))
}

# --- isolation ---------------------------------------------------------------

if [ ! -e /Users ]; then
  ok "host home hidden"
else
  bad "host home hidden" "/Users is visible in the guest"
fi

mounts="$(mount -t virtiofs | wc -l | tr -d ' ')"
if [ "$mounts" -eq 1 ]; then
  ok "exactly one mount"
else
  bad "exactly one mount" "found $mounts virtiofs mounts, expected 1"
fi

if [ -d /workspace ] && [ -w /workspace ]; then
  ok "project writable"
else
  bad "project writable" "/workspace missing or read-only"
fi

# --- privilege ---------------------------------------------------------------

if sudo -n true 2>/dev/null; then
  bad "sudo removed" "the agent user still has root"
else
  ok "sudo removed"
fi

# --- egress ------------------------------------------------------------------

if systemctl is-active --quiet sandbox-firewall.service; then
  ok "firewall active"
else
  bad "firewall active" "sandbox-firewall.service is not active"
fi

# Bypassing the proxy must fail: that is the kernel-level wall doing its job.
if curl -sm 5 --noproxy '*' https://pypi.org -o /dev/null; then
  bad "direct egress blocked" "reached the internet without the proxy"
else
  ok "direct egress blocked"
fi

# Through the proxy, an allowlisted domain must work.
if curl -sm 15 https://pypi.org -o /dev/null; then
  ok "proxy egress works"
else
  bad "proxy egress works" "allowlisted domain unreachable (check the squid log)"
fi

# A domain that is not on the allowlist must be refused by Squid.
if curl -sm 15 https://example.com -o /dev/null; then
  bad "blocked domain denied" "reached a non-allowlisted domain"
else
  ok "blocked domain denied"
fi

# --- toolchain ---------------------------------------------------------------

missing=""
for tool in node uv claude git; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
if [ -z "$missing" ]; then
  ok "toolchain"
else
  bad "toolchain" "missing:$missing"
fi

if [ "$fails" -ne 0 ]; then
  printf '\n%s check(s) FAILED - do not trust this VM\n' "$fails" >&2
  exit 1
fi

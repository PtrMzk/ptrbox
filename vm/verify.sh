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

# An argument with the real path as its default, purely so the test suite can
# run this against a state directory it controls - ptrbox passes nothing.
state="${1:-/var/lib/ptrbox}"

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

# The other half of "no root": 90-harden.sh strips the setuid bit from every
# binary that has no caller in a sandbox, because a setuid-root binary is the
# mechanism that turns "no root" back into "root" whatever the sudoers file
# says. This lists what is left and fails on anything unexpected.
#
# The real regression this catches is not somebody editing 90-harden.sh - it is
# an apt upgrade reinstalling a package and restoring its bit. That is also why
# 90-harden.sh has no done-marker: it re-strips on every boot, and this check
# is what notices when a boot did not happen in between.
#
# Space-padded matching, the same idiom 30-toolchain.sh uses for its runtime
# list: a bare substring test would accept /usr/bin/ssh because the expected
# list happens to contain /usr/bin/ssh-agent. No path here contains a space.
expected_setuid=" /usr/lib/dbus-1.0/dbus-daemon-launch-helper /usr/bin/ssh-agent /usr/sbin/unix_chkpwd "
unexpected=""
for binary in $(find / -xdev \( -perm -4000 -o -perm -2000 \) -type f 2>/dev/null | sort); do
  case "$expected_setuid" in
  *" $binary "*) ;;
  *) unexpected="$unexpected $binary" ;;
  esac
done
if [ -z "$unexpected" ]; then
  ok "setuid stripped"
else
  bad "setuid stripped" "unexpected setuid/setgid:$unexpected"
fi

# --- egress ------------------------------------------------------------------

if systemctl is-active --quiet sandbox-firewall.service; then
  ok "firewall active"
else
  bad "firewall active" "sandbox-firewall.service is not active"
fi

# The probe domain is api.anthropic.com because it is the one entry every
# seeded allowlist keeps: the runtime-gated groups (pypi, npm, go) exist only
# in VMs that asked for the runtime, but a sandbox that cannot reach Anthropic
# cannot run the agent the token below is about to be injected for.

# Bypassing the proxy must fail: that is the kernel-level wall doing its job.
if curl -sm 5 --noproxy '*' https://api.anthropic.com -o /dev/null; then
  bad "direct egress blocked" "reached the internet without the proxy"
else
  ok "direct egress blocked"
fi

# Through the proxy, an allowlisted domain must work.
if curl -sm 15 https://api.anthropic.com -o /dev/null; then
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

# claude and git are unconditional: the first is what the sandbox exists to
# run, the second is how work leaves it. The language runtimes are whatever
# the PTRBOX_<runtime> keys asked for, which 30-toolchain.sh recorded BEFORE it
# installed any of them - so this compares the request against PATH rather
# than against itself.
#
# A missing record is a failure, not an empty list. Without that, a
# 30-toolchain.sh that never ran would quietly reduce this check to "claude
# and git", which is the shape of hole this file exists to close.
if [ ! -r "$HOME/.ptrbox/toolchain" ]; then
  bad "toolchain" "30-toolchain.sh left no record of what was requested"
else
  missing=""
  for tool in claude git $(cat "$HOME/.ptrbox/toolchain"); do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
  done
  if [ -z "$missing" ]; then
    ok "toolchain"
  else
    bad "toolchain" "missing:$missing"
  fi
fi

# The packages a user asked for by name. 15-extra-packages.sh writes this
# marker when one of them does not resolve or does not end up installed; its
# own exit status is lost inside cloud-init, so the marker is how a failure on
# boot 1 reaches anyone. Without this check the VM comes up "ready" and the
# missing tool is found days later by whatever needed it.
if [ -e "$state/extra-packages.failed" ]; then
  bad "extra packages" "$(cat "$state/extra-packages.failed")"
else
  ok "extra packages"
fi

# Playwright's OS libraries, when PTRBOX_PLAYWRIGHT asked for them.
# 10-base.sh writes the list before installing it, so this compares the
# request against what dpkg actually holds rather than against itself. No
# record means the flag was off, which is the default and not a failure.
if [ -r "$state/playwright-packages" ]; then
  missing=""
  while read -r pkg; do
    [ -n "$pkg" ] || continue
    status="$(dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null || true)"
    case "$status" in
    *"install ok installed"*) ;;
    *) missing="$missing $pkg" ;;
    esac
  done <"$state/playwright-packages"
  if [ -z "$missing" ]; then
    ok "playwright libraries"
  else
    bad "playwright libraries" "missing:$missing"
  fi
fi

if [ "$fails" -ne 0 ]; then
  printf '\n%s check(s) FAILED - do not trust this VM\n' "$fails" >&2
  exit 1
fi

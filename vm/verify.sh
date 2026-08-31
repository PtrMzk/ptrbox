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

# Arguments with the real paths as their defaults, purely so the test suite can
# run this against directories it controls - ptrbox passes neither.
state="${1:-/var/lib/ptrbox}"
# Where the setuid sweep starts. It has to be / in a guest, and it must NOT be
# / under test: the suite runs this script on the developer machine, where a
# sweep of the whole root filesystem takes minutes on a Mac rather than the
# second it takes in a small VM.
scan_root="${2:-/}"

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
for binary in $(find "$scan_root" -xdev \( -perm -4000 -o -perm -2000 \) -type f 2>/dev/null | sort); do
  # Compared as an absolute path. Under test the sweep starts somewhere in
  # /tmp, so the prefix comes off first and the list above stays the real one
  # rather than something the test could satisfy by accident.
  case "$scan_root" in
  /) found="$binary" ;;
  *) found="/${binary#"$scan_root"/}" ;;
  esac
  case "$expected_setuid" in
  *" $found "*) ;;
  *) unexpected="$unexpected $found" ;;
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

# 25-services.sh turns off LLMNR and mDNS, which otherwise listen on every
# interface and resolve names by shouting on the local segment. Checked as a
# live socket rather than as a config file, because the drop-in taking effect
# depends on the reboot having happened - and this runs after it.
#
# A missing `ss` is a failure, not a pass: a check that quietly succeeds when
# it cannot look is worse than no check.
if ! command -v ss >/dev/null 2>&1; then
  bad "no multicast dns" "ss is not installed, so the listeners cannot be checked"
elif ss -tulnH 2>/dev/null | grep -qE ':(5353|5355) '; then
  bad "no multicast dns" "LLMNR or mDNS is still listening"
else
  ok "no multicast dns"
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

# --- credentials ---------------------------------------------------------------

# Exactly one credential belongs in a sandbox: the OAuth token ptrbox injects
# into ~/.profile from the Keychain, and only after everything above passes.
# These two checks say that no OTHER one arrived.
#
# The one that matters is .credentials.json. Nothing in ptrbox creates it - it
# is what Claude Code writes after an INTERACTIVE login, and it holds a refresh
# token, which is a longer-lived and higher-value secret than the injected
# access token. Two things make it reachable: ~/.claude exists and is
# agent-writable, and the allowlist grants the OAuth domains, so a `claude
# /login` run inside the VM completes over the proxy. Nothing else in this
# project would ever notice, and 40-userenv.sh is done-guarded, so nothing
# would clean it up either.
if [ -e "$HOME/.claude/.credentials.json" ]; then
  bad "no stored login" "an interactive login left a refresh token in this VM"
else
  ok "no stored login"
fi

# And nothing smuggled a second secret into the shell environment. Only the
# Claude token may be exported from ~/.profile; anything else key-shaped is
# either a credential ptrbox did not put there or one it put there twice.
strays="$(grep -cE '^[[:space:]]*export[[:space:]]+[A-Za-z_]*(TOKEN|SECRET|PASSWORD|API_KEY)' \
  "$HOME/.profile" 2>/dev/null || true)"
tokens="$(grep -cE '^[[:space:]]*export[[:space:]]+CLAUDE_CODE_OAUTH_TOKEN=' \
  "$HOME/.profile" 2>/dev/null || true)"
# Note the token is injected AFTER this script runs on a fresh create, so zero
# is the expected count then and one on any later run. More than one, or a
# key-shaped export that is not the token, is what this is looking for.
if [ "$strays" -eq "$tokens" ] && [ "$tokens" -le 1 ]; then
  ok "one credential only"
else
  bad "one credential only" "$HOME/.profile exports $strays key-shaped variables, $tokens of them the Claude token"
fi

if [ "$fails" -ne 0 ]; then
  printf '\n%s check(s) FAILED - do not trust this VM\n' "$fails" >&2
  exit 1
fi

#!/usr/bin/env bash
# =============================================================================
# preflight.sh - host dependency and state checks.
#
# Separate from cmd_install.sh because a future `ptrbox doctor` needs exactly
# these checks; one copy, not two.
#
# ptrbox never installs packages on your behalf. Running a package manager as
# a side effect of "set this up for me" should be your keystroke, not ours -
# so a missing dependency prints the command to run and stops.
# =============================================================================

# Required commands, paired with the Homebrew formula that provides them.
# limactl comes from the `lima` formula, which is the one that trips people up.
#
# Only list what ptrbox actually runs on the host - every entry here is a hard
# blocker on install. (jq used to be listed and never called; squid used to be
# listed and ran on the host, but now lives inside the proxy VM and is only
# ever invoked through limactl shell.) tests/invariants.bats enforces that
# each entry is really used.
PTRBOX_DEPS="limactl:lima git:git"

ptrbox_preflight_deps() {
  local entry tool formula missing_tools="" missing_formulae=""

  for entry in $PTRBOX_DEPS; do
    tool="${entry%%:*}"
    formula="${entry#*:}"
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing_tools="$missing_tools $tool"
      missing_formulae="$missing_formulae $formula"
    fi
  done

  if [ -n "$missing_tools" ]; then
    ptrbox_say "missing dependencies:$missing_tools"
    ptrbox_say "install them with:"
    ptrbox_say "  brew install$missing_formulae"
    return 1
  fi
  return 0
}

# Non-fatal environment reports: things worth knowing about that should not
# stop an install.
ptrbox_preflight_report() {
  local token

  if ! command -v security >/dev/null 2>&1; then
    ptrbox_warn "no macOS Keychain (\`security\`) - VMs will need CLAUDE_CODE_OAUTH_TOKEN set by hand"
  else
    token="$(security find-generic-password -s "$PTRBOX_KEYCHAIN_SERVICE" -w 2>/dev/null || true)"
    if [ -z "$token" ]; then
      ptrbox_warn "no Keychain entry '$PTRBOX_KEYCHAIN_SERVICE' - new VMs will be unauthenticated. Create one with:"
      ptrbox_warn "  claude setup-token"
      ptrbox_warn "  security add-generic-password -a \"\$USER\" -s $PTRBOX_KEYCHAIN_SERVICE -w"
    fi
  fi

  # A foreign listener on the proxy port means VMs would talk to whatever that
  # is - worth flagging, not worth blocking on (it is usually the proxy VM's
  # own port forward, or a leftover host squid from the pre-proxy-VM setup).
  if command -v lsof >/dev/null 2>&1; then
    if lsof -nP -iTCP:"$PTRBOX_PROXY_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
      ptrbox_say "something is listening on port $PTRBOX_PROXY_PORT (expected: the ptrbox-proxy port forward)"
    fi
  fi
}

#!/usr/bin/env bash
# =============================================================================
# harness.bash - shared setup for stub-driven tests.
#
# Gives each test a throwaway HOME, a stubbed PATH (lima and security are
# fakes; git is real, because its behaviour is part of what we assert), and
# helpers for reading the stub call log. The limactl stub also emulates each
# VM's filesystem, which is how the proxy VM's squid is simulated.
# =============================================================================

harness_setup() {
  # shellcheck disable=SC2034  # both are read by the .bats files that load this
  # -P: bin/ptrbox resolves its own location physically, so tests that compare
  # paths against REPO_ROOT must too, or they fail when run via a symlinked
  # checkout.
  REPO_ROOT="$(cd -P "$BATS_TEST_DIRNAME/.." && pwd -P)"
  # shellcheck disable=SC2034
  PTRBOX="$REPO_ROOT/bin/ptrbox"
  TMP="$BATS_TEST_TMPDIR"

  # A stray PTRBOX_* in the developer's environment outranks the config file,
  # so clear the lot.
  unset PTRBOX_REPO_ROOT PTRBOX_CPUS PTRBOX_MEMORY PTRBOX_DISK PTRBOX_PORT_MIN \
    PTRBOX_PORT_MAX PTRBOX_PROXY_HOST PTRBOX_PROXY_PORT PTRBOX_PROXY_CPUS \
    PTRBOX_PROXY_MEMORY PTRBOX_PROXY_DISK PTRBOX_DNS_SERVERS \
    PTRBOX_CLAUDE_MODEL PTRBOX_KEYCHAIN_SERVICE \
    PTRBOX_SQUID_LOG PTRBOX_GIT_USER_NAME PTRBOX_GIT_USER_EMAIL PTRBOX_DISTRO \
    PTRBOX_IMAGE_URL PTRBOX_BIN_DIR PTRBOX_EXTRA_PACKAGES

  export HOME="$TMP/home"
  mkdir -p "$HOME"

  export PTRBOX_STUB_DIR="$TMP/stubs"
  mkdir -p "$PTRBOX_STUB_DIR"
  export PATH="$REPO_ROOT/tests/stubs:$PATH"

  export PTRBOX_CONFIG="$TMP/ptrbox.conf"
  : >"$PTRBOX_CONFIG"


  # Deterministic git identity: without this, `new` picks up whatever the
  # machine running the tests has configured.
  export PTRBOX_GIT_USER_NAME="Test Dev"
  export PTRBOX_GIT_USER_EMAIL="test@example.com"
  export GIT_CONFIG_NOSYSTEM=1
}

# The stub call log, one invocation per line.
calls() {
  cat "$PTRBOX_STUB_DIR/calls" 2>/dev/null || true
}

# Assert a call matching a grep -E pattern happened.
assert_called() {
  if ! calls | grep -qE "$1"; then
    printf 'expected a call matching: %s\ngot:\n%s\n' "$1" "$(calls)" >&2
    return 1
  fi
}

assert_not_called() {
  if calls | grep -qE "$1"; then
    printf 'unexpected call matching: %s\ngot:\n%s\n' "$1" "$(calls)" >&2
    return 1
  fi
}

# 1-based index of the first call matching a pattern; 0 if absent. Used to
# assert ordering, e.g. that validate precedes start.
call_index() {
  calls | grep -nE "$1" | head -1 | cut -d: -f1
}

assert_order() {
  local first second i j
  first="$1"
  second="$2"
  i="$(call_index "$first")"
  j="$(call_index "$second")"
  if [ -z "$i" ] || [ -z "$j" ] || [ "$i" -ge "$j" ]; then
    printf 'expected %s before %s\ngot:\n%s\n' "$first" "$second" "$(calls)" >&2
    return 1
  fi
}

# Everything the stubs captured from stdin, concatenated.
captured_stdin() {
  cat "$PTRBOX_STUB_DIR"/stdin.* 2>/dev/null || true
}

# Everything passed as an over-long argv element (e.g. the verify script).
captured_scripts() {
  cat "$PTRBOX_STUB_DIR"/script.* 2>/dev/null || true
}

# --- the proxy VM, as the limactl stub simulates it --------------------------

# A path inside the proxy VM's fake filesystem, e.g.
# "$(proxy_fs)/etc/squid/squid.conf".
proxy_fs() {
  printf '%s/vmfs-ptrbox-proxy%s\n' "$PTRBOX_STUB_DIR" "${1:-}"
}

# Declare a VM to the limactl stub without going through ptrbox.
stub_add_vm() {
  printf '%s %s\n' "$1" "${2:-Running}" >>"$PTRBOX_STUB_DIR/vms"
}

# The state the stub holds for a VM ("Running", "Stopped", empty if absent).
stub_vm_status() {
  awk -v n="$1" '$1 == n { print $2 }' "$PTRBOX_STUB_DIR/vms" 2>/dev/null || true
}

# Flip a stub VM's status without going through ptrbox (e.g. to simulate a
# proxy that was stopped out-of-band).
stub_set_vm_status() {
  awk -v n="$1" -v s="$2" '$1 == n { print n, s; next } { print }' \
    "$PTRBOX_STUB_DIR/vms" >"$PTRBOX_STUB_DIR/vms.tmp"
  mv "$PTRBOX_STUB_DIR/vms.tmp" "$PTRBOX_STUB_DIR/vms"
}

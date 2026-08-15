#!/usr/bin/env bash
# =============================================================================
# harness.bash - shared setup for stub-driven tests.
#
# Gives each test a throwaway HOME, a stubbed PATH (lima, squid, brew, security
# are fakes; git is real, because its behaviour is part of what we assert), and
# helpers for reading the stub call log.
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
    PTRBOX_PORT_MAX PTRBOX_PROXY_HOST PTRBOX_PROXY_PORT PTRBOX_VM_SUBNET \
    PTRBOX_DNS_SERVERS PTRBOX_CLAUDE_MODEL PTRBOX_KEYCHAIN_SERVICE \
    PTRBOX_BREW_PREFIX PTRBOX_SQUID_LOG PTRBOX_GIT_USER_NAME PTRBOX_GIT_USER_EMAIL

  export HOME="$TMP/home"
  mkdir -p "$HOME"

  export PTRBOX_STUB_DIR="$TMP/stubs"
  mkdir -p "$PTRBOX_STUB_DIR"
  export PATH="$REPO_ROOT/tests/stubs:$PATH"

  export PTRBOX_CONFIG="$TMP/ptrbox.conf"
  : >"$PTRBOX_CONFIG"

  # Fake Homebrew prefix so nothing reaches for a real /opt/homebrew.
  export PTRBOX_STUB_PREFIX="$TMP/brew"
  mkdir -p "$PTRBOX_STUB_PREFIX/etc/squid" "$PTRBOX_STUB_PREFIX/var/logs"

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

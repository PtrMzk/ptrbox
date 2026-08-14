#!/usr/bin/env bats
# =============================================================================
# ptrbox install against stubbed brew/squid/lima.
#
# The squid config is the security-relevant artifact here, so most of these
# tests are about NOT clobbering it: an unmanaged config survives, a candidate
# that squid rejects is never installed, and every overwrite leaves a backup.
# =============================================================================

load lib/harness

setup() {
  harness_setup
  export PTRBOX_REPO_ROOT="$TMP/code"
  SQUID_CONF="$PTRBOX_STUB_PREFIX/etc/squid.conf"
  ALLOWLIST="$PTRBOX_STUB_PREFIX/etc/squid/allowed_domains.txt"
}

# --- fresh install -----------------------------------------------------------

@test "fresh install writes the squid config and the allowlist" {
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [ -f "$SQUID_CONF" ]
  [ -f "$ALLOWLIST" ]
}

@test "the installed squid config is rendered for the detected prefix" {
  "$PTRBOX" install
  grep -q "http_port 8888" "$SQUID_CONF"
  grep -q "acl vmnet src 192.168.5.0/24" "$SQUID_CONF"
  grep -q "$PTRBOX_STUB_PREFIX/etc/squid/allowed_domains.txt" "$SQUID_CONF"
  run grep -c '__[A-Z][A-Z0-9_]*__' "$SQUID_CONF"
  [ "$status" -ne 0 ]
}

@test "install creates the directories ptrbox needs" {
  "$PTRBOX" install
  [ -d "$TMP/code" ]
  [ -d "$HOME/.lima/_generated" ]
  [ -d "$HOME/.ssh/config.d" ]
}

@test "install restarts squid once it has written a config" {
  "$PTRBOX" install
  assert_called "brew services restart squid"
}

# --- idempotence -------------------------------------------------------------

@test "a second install changes nothing and says so" {
  "$PTRBOX" install
  rm -f "$PTRBOX_STUB_DIR/calls"
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [[ "$output" == *"already set up"* ]]
  assert_not_called "brew services restart"
}

# --- validation --------------------------------------------------------------

@test "validates the candidate config before installing it" {
  "$PTRBOX" install
  # -f names the candidate: the live path must never be the thing under test.
  assert_called "squid -f .*squid.conf.ptrbox-new -k parse"
  assert_order "squid -f .* -k parse" "brew services restart squid"
}

@test "a config squid rejects is never installed" {
  export PTRBOX_STUB_SQUID_PARSE=fail
  run "$PTRBOX" install
  [ "$status" -ne 0 ]
  [[ "$output" == *"cannot parse"* ]]
  [ ! -f "$SQUID_CONF" ]
  # And no half-written candidate left lying around.
  [ ! -f "$SQUID_CONF.ptrbox-new" ]
  assert_not_called "brew services restart"
}

# --- an existing, unmanaged config -------------------------------------------

@test "an unmanaged squid config is not overwritten without consent" {
  printf '# hand written\nhttp_access deny all\n' >"$SQUID_CONF"
  run "$PTRBOX" install
  # Non-interactive: declines rather than clobbering.
  [[ "$output" == *"not managed by ptrbox"* ]]
  [ "$(head -1 "$SQUID_CONF")" = "# hand written" ]
  run bash -c "ls $PTRBOX_STUB_PREFIX/etc/squid.conf.pre-ptrbox.* 2>/dev/null"
  [ "$status" -ne 0 ]
}

@test "--yes replaces an unmanaged config and keeps a backup" {
  printf '# hand written\nhttp_access deny all\n' >"$SQUID_CONF"
  run "$PTRBOX" install --yes
  [ "$status" -eq 0 ]
  grep -q "ptrbox-managed" "$SQUID_CONF"
  run bash -c "cat $PTRBOX_STUB_PREFIX/etc/squid.conf.pre-ptrbox.*"
  [ "$status" -eq 0 ]
  [[ "$output" == *"# hand written"* ]]
}

@test "--no-input declines instead of hanging on a prompt" {
  printf '# hand written\n' >"$SQUID_CONF"
  run "$PTRBOX" install --no-input
  [[ "$output" == *"declining"* ]]
  [ "$(head -1 "$SQUID_CONF")" = "# hand written" ]
}

# --- an existing, ptrbox-managed config --------------------------------------

@test "an older ptrbox-managed config is updated without prompting" {
  printf '# ptrbox-managed v0\n# stale\n' >"$SQUID_CONF"
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [[ "$output" == *"v0 -> v1"* ]]
  grep -q "http_port 8888" "$SQUID_CONF"
}

# --- the allowlist -----------------------------------------------------------

@test "an existing allowlist is never overwritten" {
  "$PTRBOX" install
  printf 'my.private.registry\n' >"$ALLOWLIST"
  run "$PTRBOX" install
  [ "$(cat "$ALLOWLIST")" = "my.private.registry" ]
  [[ "$output" == *"differs from the shipped allowlist"* ]]
}

@test "an allowlist-only change reloads rather than restarting squid" {
  "$PTRBOX" install
  rm -f "$ALLOWLIST" "$PTRBOX_STUB_DIR/calls"
  export PTRBOX_STUB_SQUID_STATE=started
  run "$PTRBOX" install
  # A restart severs every live VM tunnel; reconfigure drops nothing.
  assert_called "squid -k reconfigure"
  assert_not_called "brew services restart"
}

# --- ssh ---------------------------------------------------------------------

@test "install adds the ssh Include line" {
  "$PTRBOX" install
  grep -qF 'Include config.d/*' "$HOME/.ssh/config"
}

@test "the ssh Include is added exactly once" {
  "$PTRBOX" install
  "$PTRBOX" install
  [ "$(grep -cF 'Include config.d/*' "$HOME/.ssh/config")" = "1" ]
}

@test "the ssh Include survives an existing config and goes first" {
  mkdir -p "$HOME/.ssh"
  printf 'Host example\n  User me\n' >"$HOME/.ssh/config"
  "$PTRBOX" install
  [ "$(head -1 "$HOME/.ssh/config")" = "Include config.d/*" ]
  grep -q "Host example" "$HOME/.ssh/config"
}

@test "the ssh Include handles an empty config file" {
  # The sed one-liner this replaces silently did nothing on a zero-line file.
  mkdir -p "$HOME/.ssh"
  : >"$HOME/.ssh/config"
  "$PTRBOX" install
  grep -qF 'Include config.d/*' "$HOME/.ssh/config"
}

# --- PATH symlink ------------------------------------------------------------

@test "--yes symlinks ptrbox onto PATH" {
  "$PTRBOX" install --yes
  [ -L "$HOME/bin/ptrbox" ]
  [ "$(readlink "$HOME/bin/ptrbox")" = "$REPO_ROOT/bin/ptrbox" ]
}

@test "the symlink is not created without consent" {
  # No tty in tests, so the prompt declines by default.
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [ ! -e "$HOME/bin/ptrbox" ]
  [[ "$output" == *"symlink ptrbox into"* ]]
}

@test "an existing correct symlink is left alone silently" {
  "$PTRBOX" install --yes
  run "$PTRBOX" install --yes
  [ "$status" -eq 0 ]
  [[ "$output" != *"linked"* ]]
}

@test "a foreign file at the target is not clobbered without consent" {
  mkdir -p "$HOME/bin"
  printf '#!/bin/sh\necho someone elses ptrbox\n' >"$HOME/bin/ptrbox"
  run "$PTRBOX" install --no-input
  [ ! -L "$HOME/bin/ptrbox" ]
  grep -q "someone elses" "$HOME/bin/ptrbox"
  [[ "$output" == *"already exists and is not this checkout"* ]]
}

@test "--yes replaces a foreign file at the target" {
  mkdir -p "$HOME/bin"
  printf '#!/bin/sh\n' >"$HOME/bin/ptrbox"
  "$PTRBOX" install --yes
  [ -L "$HOME/bin/ptrbox" ]
}

@test "the symlink target honours PTRBOX_BIN_DIR" {
  export PTRBOX_BIN_DIR="$TMP/somewhere/bin"
  "$PTRBOX" install --yes
  [ -L "$TMP/somewhere/bin/ptrbox" ]
}

@test "a bin dir that is not on PATH is called out" {
  export PTRBOX_BIN_DIR="$TMP/not-on-path"
  run "$PTRBOX" install --yes
  [[ "$output" == *"not on your PATH"* ]]
}

# --- dependencies ------------------------------------------------------------

@test "a missing dependency stops the install and names the formula" {
  # A PATH with every stub except limactl. _lib.sh comes along because the
  # stubs source it from their own directory.
  mkdir -p "$TMP/partial"
  ln -s "$REPO_ROOT/tests/stubs/_lib.sh" "$TMP/partial/_lib.sh"
  for t in squid brew security; do
    ln -s "$REPO_ROOT/tests/stubs/$t" "$TMP/partial/$t"
  done
  PATH="$TMP/partial:/usr/bin:/bin" run "$PTRBOX" install
  [ "$status" -ne 0 ]
  [[ "$output" == *"missing dependencies: limactl"* ]]
  [[ "$output" == *"brew install lima"* ]]
  # ptrbox never installs packages itself.
  assert_not_called "brew install"
  [ ! -f "$SQUID_CONF" ]
}

# --- manifest ----------------------------------------------------------------

@test "install records what it touched" {
  "$PTRBOX" install
  grep -q "wrote $SQUID_CONF" "$(dirname "$PTRBOX_CONFIG")/install-manifest"
}

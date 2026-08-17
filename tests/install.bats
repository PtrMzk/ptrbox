#!/usr/bin/env bats
# =============================================================================
# ptrbox install against stubbed lima.
#
# Install's job since the proxy moved into a VM: seed the host-side allowlist,
# provision/start the ptrbox-proxy VM, push a validated squid config into it,
# and wire up ssh + PATH. The security-relevant parts are the ones about the
# pushed config: it is validated as a candidate before activation, a rejected
# config never lands, and the user's allowlist is never overwritten.
# =============================================================================

load lib/harness

setup() {
  harness_setup
  export PTRBOX_REPO_ROOT="$TMP/code"
  ALLOWLIST="$(dirname "$PTRBOX_CONFIG")/allowed_domains.txt"
  VM_CONF="$(proxy_fs)/etc/squid/squid.conf"
  VM_ALLOWLIST="$(proxy_fs)/etc/squid/allowed_domains.txt"
}

# --- fresh install -----------------------------------------------------------

@test "fresh install seeds the allowlist and provisions the proxy VM" {
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [ -f "$ALLOWLIST" ]
  assert_called "limactl start --name ptrbox-proxy"
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

@test "install pushes the rendered squid config into the proxy VM" {
  "$PTRBOX" install
  grep -q "http_port 8888" "$VM_CONF"
  grep -q "/etc/squid/allowed_domains.txt" "$VM_CONF"
  run grep -c '__[A-Z][A-Z0-9_]*__' "$VM_CONF"
  [ "$status" -ne 0 ]
}

@test "install pushes the host allowlist into the proxy VM" {
  "$PTRBOX" install
  cmp -s "$ALLOWLIST" "$VM_ALLOWLIST"
  grep -q "api.anthropic.com" "$VM_ALLOWLIST"
}

@test "install creates the directories ptrbox needs" {
  "$PTRBOX" install
  [ -d "$TMP/code" ]
  [ -d "$HOME/.lima/_generated" ]
  [ -d "$HOME/.ssh/config.d" ]
}

@test "the generated proxy VM config has no mounts and a loopback forward" {
  "$PTRBOX" install
  grep -q '^mounts: \[\]' "$HOME/.lima/_generated/ptrbox-proxy.yaml"
  grep -q 'hostIP: "127.0.0.1"' "$HOME/.lima/_generated/ptrbox-proxy.yaml"
}

@test "install restarts squid in the VM after pushing a new config" {
  "$PTRBOX" install
  assert_called "sudo systemctl restart squid"
}

# --- idempotence -------------------------------------------------------------

@test "a second install changes nothing and says so" {
  "$PTRBOX" install
  rm -f "$PTRBOX_STUB_DIR/calls"
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [[ "$output" == *"already set up"* ]]
  assert_not_called "limactl start"
  assert_not_called "systemctl restart"
  assert_not_called "squid -k reconfigure"
}

# --- validation --------------------------------------------------------------

@test "validates the candidate config in the VM before activating it" {
  "$PTRBOX" install
  # -f names the candidate: the live path must never be the thing under test.
  assert_called "sudo squid -f /etc/squid/squid.conf.ptrbox-new -k parse"
  assert_order "squid -f .* -k parse" "sudo mv /etc/squid/squid.conf.ptrbox-new /etc/squid/squid.conf"
  assert_order "sudo mv .*squid.conf" "sudo systemctl restart squid"
}

@test "a config squid rejects is never activated" {
  export PTRBOX_STUB_SQUID_PARSE=fail
  run "$PTRBOX" install
  [ "$status" -ne 0 ]
  [[ "$output" == *"squid rejected"* ]]
  [ ! -f "$VM_CONF" ]
  # And no half-pushed candidate left lying around in the VM.
  [ ! -f "$VM_CONF.ptrbox-new" ]
  assert_not_called "systemctl restart"
}

# --- the allowlist -----------------------------------------------------------

@test "an existing allowlist is never overwritten" {
  "$PTRBOX" install
  printf 'my.private.registry\n' >"$ALLOWLIST"
  run "$PTRBOX" install
  [ "$(cat "$ALLOWLIST")" = "my.private.registry" ]
  [[ "$output" == *"differs from the shipped allowlist"* ]]
  # ...and the user's version is what reaches the proxy VM.
  grep -qx "my.private.registry" "$VM_ALLOWLIST"
}

@test "an allowlist-only change reloads rather than restarting squid" {
  "$PTRBOX" install
  printf 'my.private.registry\n' >>"$ALLOWLIST"
  rm -f "$PTRBOX_STUB_DIR/calls"
  run "$PTRBOX" install
  # A restart severs every live VM tunnel; reconfigure drops nothing.
  assert_called "sudo squid -k reconfigure"
  assert_not_called "systemctl restart"
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
  ln -s "$REPO_ROOT/tests/stubs/security" "$TMP/partial/security"
  PATH="$TMP/partial:/usr/bin:/bin" run "$PTRBOX" install
  [ "$status" -ne 0 ]
  [[ "$output" == *"missing dependencies: limactl"* ]]
  # ptrbox never installs packages itself - it prints the command and stops.
  [[ "$output" == *"brew install lima"* ]]
  [ ! -f "$ALLOWLIST" ]
}

@test "host squid is no longer a dependency" {
  # The proxy VM apt-installs its own squid; requiring one on the Mac would
  # make people set up a daemon nothing uses.
  run "$PTRBOX" install
  [ "$status" -eq 0 ]
  [[ "$output" != *"missing dependencies"* ]]
}

# --- manifest ----------------------------------------------------------------

@test "install records what it touched" {
  "$PTRBOX" install
  grep -q "wrote $ALLOWLIST" "$(dirname "$PTRBOX_CONFIG")/install-manifest"
}

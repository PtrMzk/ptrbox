#!/usr/bin/env bats
# =============================================================================
# Full lifecycle against stubs: create a VM, verify it, tear it down - with no
# Lima, no Squid, no Keychain and no Mac anywhere in sight.
#
# These are the tests that stand in for "did provisioning work", so they assert
# the things a human would otherwise have to eyeball: the order of operations,
# what got written where, that credentials travel on stdin, and that teardown
# removes the VM without touching the repo.
# =============================================================================

load lib/harness

setup() {
  harness_setup
  export PTRBOX_REPO_ROOT="$TMP/code"
}

# --- new ---------------------------------------------------------------------

@test "new creates the repo directory and git-inits it" {
  run "$PTRBOX" new demo
  [ "$status" -eq 0 ]
  [ -d "$TMP/code/demo/.git" ]
}

@test "new neutralises git hooks on the host clone" {
  "$PTRBOX" new demo
  # Agent-written hooks execute on the HOST when you run git there.
  [ "$(git -C "$TMP/code/demo" config core.hooksPath)" = "/dev/null" ]
}

@test "new writes a generated config and validates it before starting" {
  "$PTRBOX" new demo
  [ -f "$HOME/.lima/_generated/demo.yaml" ]
  assert_order "limactl validate" "limactl start --name demo"
}

@test "the generated config carries this repo's path and no placeholders" {
  "$PTRBOX" new demo
  grep -q "location: \"$TMP/code/demo\"" "$HOME/.lima/_generated/demo.yaml"
  run grep -c '__[A-Z][A-Z0-9_]*__' "$HOME/.lima/_generated/demo.yaml"
  [ "$status" -ne 0 ]
}

@test "new reboots the VM so the firewall clamps" {
  "$PTRBOX" new demo
  # Boot 1 provisions over an open network; the wall only goes up on reboot.
  assert_order "limactl start --name demo" "limactl stop demo"
  assert_order "limactl stop demo" "limactl start demo$"
}

@test "new links the VM's ssh config into ~/.ssh/config.d" {
  "$PTRBOX" new demo
  [ -L "$HOME/.ssh/config.d/lima-demo" ]
  [ -f "$HOME/.ssh/config.d/lima-demo" ]
}

@test "new runs the verification script inside the VM" {
  "$PTRBOX" new demo
  assert_called "limactl shell demo -- bash -lc"
  # It is really vm/verify.sh that runs, not something improvised.
  captured_scripts | grep -q "assert a sandbox VM's security properties"
  captured_scripts | grep -q "sudo removed"
}

@test "new refuses to sandbox the ptrbox checkout itself" {
  # An agent that can edit its own sandbox's provisioning code is not
  # sandboxed - and that code later runs on the host.
  run "$PTRBOX" new "$REPO_ROOT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"contains the ptrbox checkout"* ]]
  assert_not_called "limactl start"
}

@test "new refuses a symlinked route to the ptrbox checkout" {
  # The containment check compares physical paths: two names for one directory
  # must not be a way around it.
  ln -s "$REPO_ROOT" "$TMP/alias"
  run "$PTRBOX" new "$TMP/alias"
  [ "$status" -ne 0 ]
  [[ "$output" == *"contains the ptrbox checkout"* ]]
  assert_not_called "limactl start"
}

@test "new refuses a parent directory of the ptrbox checkout" {
  # Mounting the parent hands the agent write access to ptrbox's own code.
  # Uses a relocated checkout under the test tmpdir - a real bin/ptrbox so
  # PTRBOX_ROOT resolves to it, with lib/ and vm/ symlinked alongside - so the
  # test never operates on a directory outside its sandbox.
  mkdir -p "$TMP/parent/co/bin"
  cp "$PTRBOX" "$TMP/parent/co/bin/ptrbox"
  ln -s "$REPO_ROOT/lib" "$TMP/parent/co/lib"
  ln -s "$REPO_ROOT/vm" "$TMP/parent/co/vm"

  run "$TMP/parent/co/bin/ptrbox" new "$TMP/parent"
  [ "$status" -ne 0 ]
  [[ "$output" == *"contains the ptrbox checkout"* ]]
  assert_not_called "limactl start"
}

@test "new refuses to double-provision an existing VM" {
  "$PTRBOX" new demo
  run "$PTRBOX" new demo
  [ "$status" -ne 0 ]
  [[ "$output" == *"already exists"* ]]
}

@test "new requires an argument" {
  run "$PTRBOX" new
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage:"* ]]
}

@test "new uses an existing repo as-is when given a path" {
  mkdir -p "$TMP/elsewhere/thing"
  git -C "$TMP/elsewhere/thing" init -q
  run "$PTRBOX" new "$TMP/elsewhere/thing"
  [ "$status" -eq 0 ]
  assert_called "limactl start --name thing"
}

# --- auth --------------------------------------------------------------------

@test "the auth token travels on stdin and never through argv" {
  export PTRBOX_STUB_TOKEN="sk-ant-oat-EXAMPLE"
  "$PTRBOX" new demo
  captured_stdin | grep -q "CLAUDE_CODE_OAUTH_TOKEN=\"sk-ant-oat-EXAMPLE\""
  # ps and shell history must never see it.
  assert_not_called "sk-ant-oat-EXAMPLE"
  # Nor may it be written into the generated config, which persists on disk.
  run grep -r "sk-ant-oat-EXAMPLE" "$HOME/.lima/_generated"
  [ "$status" -ne 0 ]
}

@test "a missing Keychain entry warns but still leaves a usable VM" {
  unset PTRBOX_STUB_TOKEN
  run "$PTRBOX" new demo
  [ "$status" -eq 0 ]
  [[ "$output" == *"no Keychain entry"* ]]
}

@test "verification failure blocks the token and flags the VM" {
  export PTRBOX_STUB_TOKEN="sk-ant-oat-EXAMPLE"
  export PTRBOX_STUB_VERIFY=fail
  run "$PTRBOX" new demo
  [ "$status" -ne 0 ]
  [[ "$output" == *"verification FAILED"* ]]
  # An unverified VM does not get credentials.
  [ -z "$(captured_stdin)" ]
}

# --- rm ----------------------------------------------------------------------

@test "rm deletes the VM and its artifacts" {
  "$PTRBOX" new demo
  run "$PTRBOX" rm demo
  [ "$status" -eq 0 ]
  assert_called "limactl delete -f demo"
  [ ! -f "$HOME/.lima/_generated/demo.yaml" ]
  [ ! -L "$HOME/.ssh/config.d/lima-demo" ]
}

@test "rm never touches the repo on the host" {
  "$PTRBOX" new demo
  printf 'work\n' >"$TMP/code/demo/file.txt"
  "$PTRBOX" rm demo
  [ -f "$TMP/code/demo/file.txt" ]
  [ -d "$TMP/code/demo/.git" ]
}

@test "rm accepts a repo path as well as a VM name" {
  "$PTRBOX" new demo
  run "$PTRBOX" rm "$TMP/code/demo"
  [ "$status" -eq 0 ]
  assert_called "limactl delete -f demo"
}

@test "rm refuses to guess when the VM does not exist" {
  run "$PTRBOX" rm nosuchvm
  [ "$status" -ne 0 ]
  [[ "$output" == *"no VM named"* ]]
  assert_not_called "limactl delete"
}

@test "rm requires an argument" {
  run "$PTRBOX" rm
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage:"* ]]
}

# --- new then rm then new ----------------------------------------------------

@test "a repo can be re-sandboxed after teardown" {
  "$PTRBOX" new demo
  "$PTRBOX" rm demo
  run "$PTRBOX" new demo
  [ "$status" -eq 0 ]
  [ -f "$HOME/.lima/_generated/demo.yaml" ]
}

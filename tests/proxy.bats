#!/usr/bin/env bats
# =============================================================================
# The proxy VM's lifecycle coupling: any sandbox coming up starts it, the
# last one going away stops it - and every ambiguity errs toward the proxy
# LINGERING, because a proxy that is down under a live sandbox bricks the
# agent's network while a lingering one only costs idle RAM.
# =============================================================================

load lib/harness

setup() {
  harness_setup
  export PTRBOX_REPO_ROOT="$TMP/code"
}

# --- new brings the proxy up -------------------------------------------------

@test "new provisions the proxy VM before the sandbox" {
  "$PTRBOX" new demo
  # Order matters: from the post-provision reboot on, the proxy is the
  # sandbox's only way out.
  assert_order "limactl start --name ptrbox-proxy" "limactl start --name demo"
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

@test "new syncs the squid config into a proxy it just created" {
  "$PTRBOX" new demo
  grep -q "http_port 8888" "$(proxy_fs)/etc/squid/squid.conf"
  grep -q "api.anthropic.com" "$(proxy_fs)/etc/squid/allowed_domains.txt"
}

@test "new with the proxy already running does not restart it" {
  "$PTRBOX" install >/dev/null 2>&1
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" new demo
  assert_not_called "limactl start --name ptrbox-proxy"
  assert_not_called "limactl start ptrbox-proxy"
}

@test "new restarts a stopped proxy" {
  "$PTRBOX" install >/dev/null 2>&1
  stub_set_vm_status ptrbox-proxy Stopped
  "$PTRBOX" new demo
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

@test "the sandbox template still points at the host-side proxy address" {
  # The re-homing of squid must be invisible to the sandboxes: same gateway
  # address, same port, same firewall rule.
  "$PTRBOX" new demo
  grep -q 'HTTPS_PROXY="http://192.168.5.2:8888"' "$HOME/.lima/_generated/demo.yaml"
  grep -q 'ip daddr 192.168.5.2 tcp dport 8888 accept' "$HOME/.lima/_generated/demo.yaml"
}

@test "new refuses the reserved proxy VM name" {
  run "$PTRBOX" new ptrbox-proxy
  [ "$status" -ne 0 ]
  [[ "$output" == *"reserved"* ]]
  assert_not_called "limactl start"
}

# --- rm stops the proxy with the last sandbox --------------------------------

@test "rm of the last sandbox stops the proxy but keeps it around" {
  "$PTRBOX" new demo
  "$PTRBOX" rm demo
  assert_called "limactl stop ptrbox-proxy"
  # Stopped, not deleted: the next start must not pay provisioning again.
  [ "$(stub_vm_status ptrbox-proxy)" = "Stopped" ]
  assert_not_called "limactl delete .*ptrbox-proxy"
}

@test "rm with another sandbox still running leaves the proxy alone" {
  "$PTRBOX" new demo
  "$PTRBOX" new other
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" rm demo
  assert_not_called "limactl stop ptrbox-proxy"
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

@test "a stopped sandbox does not keep the proxy up" {
  "$PTRBOX" new demo
  "$PTRBOX" new other
  "$PTRBOX" stop other
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" rm demo
  # demo was the last RUNNING sandbox; stopped ones need no proxy.
  assert_called "limactl stop ptrbox-proxy"
}

@test "rm refuses to resolve the proxy VM" {
  "$PTRBOX" install >/dev/null 2>&1
  run "$PTRBOX" rm ptrbox-proxy
  [ "$status" -ne 0 ]
  [[ "$output" == *"not a sandbox"* ]]
  assert_not_called "limactl delete"
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

# --- start / stop ------------------------------------------------------------

@test "start brings the proxy up before the sandbox" {
  "$PTRBOX" new demo
  "$PTRBOX" stop demo # proxy stops too: demo was the last one
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" start demo
  assert_order "limactl start ptrbox-proxy" "limactl start demo"
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
  [ "$(stub_vm_status demo)" = "Running" ]
}

@test "start pushes allowlist edits made while the proxy was down" {
  "$PTRBOX" new demo
  "$PTRBOX" stop demo
  "$PTRBOX" allow deferred.example.com # saved host-side only
  run grep -qx "deferred.example.com" "$(proxy_fs)/etc/squid/allowed_domains.txt"
  [ "$status" -ne 0 ]
  "$PTRBOX" start demo
  grep -qx "deferred.example.com" "$(proxy_fs)/etc/squid/allowed_domains.txt"
}

@test "stop of the last sandbox stops the proxy" {
  "$PTRBOX" new demo
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" stop demo
  assert_called "limactl stop demo"
  assert_called "limactl stop ptrbox-proxy"
}

@test "stop with another sandbox running leaves the proxy up" {
  "$PTRBOX" new demo
  "$PTRBOX" new other
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" stop demo
  assert_not_called "limactl stop ptrbox-proxy"
}

@test "start and stop refuse the proxy VM name" {
  "$PTRBOX" install >/dev/null 2>&1
  run "$PTRBOX" start ptrbox-proxy
  [ "$status" -ne 0 ]
  run "$PTRBOX" stop ptrbox-proxy
  [ "$status" -ne 0 ]
  [ "$(stub_vm_status ptrbox-proxy)" = "Running" ]
}

@test "start of an unknown VM points at new" {
  run "$PTRBOX" start nosuchvm
  [ "$status" -ne 0 ]
  [[ "$output" == *"ptrbox new"* ]]
}

@test "start and stop require an argument" {
  run "$PTRBOX" start
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage:"* ]]
  run "$PTRBOX" stop
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage:"* ]]
}

@test "stop of an already stopped sandbox still reaps an idle proxy" {
  # A proxy left over from a crash gets cleaned up on the next explicit stop
  # rather than lingering forever (there is deliberately no janitor).
  "$PTRBOX" new demo
  stub_set_vm_status demo Stopped
  rm -f "$PTRBOX_STUB_DIR/calls"
  run "$PTRBOX" stop demo
  [ "$status" -eq 0 ]
  assert_called "limactl stop ptrbox-proxy"
}

# --- erring toward lingering -------------------------------------------------

@test "a foreign lima VM does not count, but a ptrbox config with a live VM does" {
  # Only VMs with a rendered config in the generated dir count as sandboxes;
  # a hand-made lima VM neither holds the proxy up nor gets torn down.
  "$PTRBOX" new demo
  stub_add_vm somebody-elses-vm Running
  rm -f "$PTRBOX_STUB_DIR/calls"
  "$PTRBOX" rm demo
  assert_called "limactl stop ptrbox-proxy"
}

@test "the proxy sync is rolled back when squid rejects the config" {
  "$PTRBOX" install >/dev/null 2>&1
  printf 'bad domain entry\n' >"$(dirname "$PTRBOX_CONFIG")/allowed_domains.txt"
  export PTRBOX_STUB_SQUID_PARSE=fail
  run "$PTRBOX" new demo
  [ "$status" -ne 0 ]
  [[ "$output" == *"squid rejected"* ]]
  # The VM still serves what it served before the bad push.
  grep -q "api.anthropic.com" "$(proxy_fs)/etc/squid/allowed_domains.txt"
  run grep -q "bad domain entry" "$(proxy_fs)/etc/squid/allowed_domains.txt"
  [ "$status" -ne 0 ]
}

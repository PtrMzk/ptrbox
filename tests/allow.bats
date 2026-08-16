#!/usr/bin/env bats
# ptrbox allow - managing the egress allowlist.
#
# The allowlist lives on the host and is pushed into the proxy VM, where squid
# validates it. The interesting cases are the ones where bad input or a broken
# edit could take the proxy down - and, since the proxy became a VM, the ones
# where it is not running.

load lib/harness

setup() {
  harness_setup
  export PTRBOX_REPO_ROOT="$TMP/code"
  ALLOWLIST="$(dirname "$PTRBOX_CONFIG")/allowed_domains.txt"
  VM_ALLOWLIST="$(proxy_fs)/etc/squid/allowed_domains.txt"
  "$PTRBOX" install >/dev/null 2>&1
  rm -f "$PTRBOX_STUB_DIR/calls"
}

# --- appending ---------------------------------------------------------------

@test "allow appends a domain and pushes it to the proxy VM" {
  run "$PTRBOX" allow files.example.com
  [ "$status" -eq 0 ]
  grep -qx "files.example.com" "$ALLOWLIST"
  grep -qx "files.example.com" "$VM_ALLOWLIST"
}

@test "allow accepts several domains at once" {
  "$PTRBOX" allow one.example.com two.example.com
  grep -qx "one.example.com" "$ALLOWLIST"
  grep -qx "two.example.com" "$ALLOWLIST"
}

@test "allow accepts a leading dot for subdomains" {
  "$PTRBOX" allow .cdn.example.com
  grep -qx ".cdn.example.com" "$ALLOWLIST"
}

@test "adding a domain twice does not duplicate it" {
  "$PTRBOX" allow files.example.com
  run "$PTRBOX" allow files.example.com
  [ "$status" -eq 0 ]
  [[ "$output" == *"already allowed"* ]]
  [ "$(grep -cx "files.example.com" "$ALLOWLIST")" -eq 1 ]
}

@test "a domain already shipped in the allowlist is recognised" {
  run "$PTRBOX" allow pypi.org
  [[ "$output" == *"already allowed"* ]]
  assert_not_called "squid -k reconfigure"
}

# --- reloading ---------------------------------------------------------------

@test "a change reloads squid without restarting it" {
  "$PTRBOX" allow files.example.com
  # A restart severs every live VM tunnel, including Claude's request.
  assert_called "sudo squid -k reconfigure"
  assert_not_called "systemctl restart"
}

@test "the new allowlist is validated before it is kept" {
  "$PTRBOX" allow files.example.com
  assert_order "sudo squid -k parse" "sudo squid -k reconfigure"
}

@test "an allowlist squid rejects is rolled back on host and in the VM" {
  export PTRBOX_STUB_SQUID_PARSE=fail
  run "$PTRBOX" allow files.example.com
  [ "$status" -ne 0 ]
  [[ "$output" == *"restored the previous one"* ]]
  # The live file is the old one. Whole-line match: the shipped allowlist
  # mentions example domains in its comments.
  run grep -qx "files.example.com" "$ALLOWLIST"
  [ "$status" -ne 0 ]
  # The proxy VM was restored too - a later squid restart there must not trip
  # over a file we knew was bad.
  run grep -qx "files.example.com" "$VM_ALLOWLIST"
  [ "$status" -ne 0 ]
  # ...and the rejected version is kept rather than thrown away.
  grep -qx "files.example.com" "$ALLOWLIST.rejected"
  assert_not_called "squid -k reconfigure"
}

# --- the proxy VM is down ----------------------------------------------------

@test "with the proxy stopped the edit is saved and deferred" {
  stub_set_vm_status ptrbox-proxy Stopped
  rm -f "$PTRBOX_STUB_DIR/calls"
  run "$PTRBOX" allow files.example.com
  [ "$status" -eq 0 ]
  [[ "$output" == *"applied when it next starts"* ]]
  grep -qx "files.example.com" "$ALLOWLIST"
  # Nothing was pushed at a VM that cannot answer.
  assert_not_called "sudo tee"
  assert_not_called "squid -k"
}

# --- validation --------------------------------------------------------------

@test "a domain with shell or squid metacharacters is refused" {
  run "$PTRBOX" allow 'evil.com all'
  [ "$status" -ne 0 ]
  [[ "$output" == *"is not a domain"* ]]
  assert_not_called "squid -k reconfigure"
}

@test "an http:// URL is refused" {
  run "$PTRBOX" allow "https://example.com/path"
  [ "$status" -ne 0 ]
}

@test "a bare name with no dot is refused" {
  run "$PTRBOX" allow localhost
  [ "$status" -ne 0 ]
  [[ "$output" == *"no dot"* ]]
}

@test "a rejected domain never reaches the file" {
  run "$PTRBOX" allow 'bad;domain'
  [ "$status" -ne 0 ]
  run grep -q "bad" "$ALLOWLIST"
  [ "$status" -ne 0 ]
}

# --- editor mode -------------------------------------------------------------

@test "with no arguments it opens \$EDITOR and reloads" {
  export EDITOR="$TMP/fake-editor"
  cat >"$EDITOR" <<'ED'
#!/bin/bash
printf 'edited.example.com\n' >> "$1"
ED
  chmod +x "$EDITOR"
  run "$PTRBOX" allow
  [ "$status" -eq 0 ]
  grep -qx "edited.example.com" "$ALLOWLIST"
  grep -qx "edited.example.com" "$VM_ALLOWLIST"
  assert_called "sudo squid -k reconfigure"
}

@test "an editor session that changes nothing does not reload squid" {
  export EDITOR=true
  run "$PTRBOX" allow
  [ "$status" -eq 0 ]
  [[ "$output" == *"unchanged"* ]]
  assert_not_called "squid -k reconfigure"
}

# --- listing -----------------------------------------------------------------

@test "--list prints domains without comments" {
  run "$PTRBOX" allow --list
  [ "$status" -eq 0 ]
  [[ "$output" == *"api.anthropic.com"* ]]
  [[ "$output" != *"#"* ]]
}

# --- preconditions -----------------------------------------------------------

@test "a missing allowlist points at install" {
  rm -f "$ALLOWLIST"
  run "$PTRBOX" allow files.example.com
  [ "$status" -ne 0 ]
  [[ "$output" == *"ptrbox install"* ]]
}

@test "an unknown option is rejected" {
  run "$PTRBOX" allow --deny-everything
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown option"* ]]
}

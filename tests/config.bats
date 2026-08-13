#!/usr/bin/env bats
# Tests for lib/common.sh: config precedence, validation, name resolution.

setup() {
  REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
  TMP="$BATS_TEST_TMPDIR"

  # Isolate from the developer's real environment: an exported PTRBOX_* here
  # would silently win over everything these tests set up.
  unset PTRBOX_REPO_ROOT PTRBOX_CPUS PTRBOX_MEMORY PTRBOX_DISK PTRBOX_PORT_MIN \
    PTRBOX_PORT_MAX PTRBOX_PROXY_HOST PTRBOX_PROXY_PORT PTRBOX_VM_SUBNET \
    PTRBOX_DNS_SERVERS PTRBOX_CLAUDE_MODEL PTRBOX_KEYCHAIN_SERVICE \
    PTRBOX_BREW_PREFIX PTRBOX_SQUID_LOG PTRBOX_GIT_USER_NAME PTRBOX_GIT_USER_EMAIL

  HOME="$TMP/home"
  mkdir -p "$HOME"
  export HOME
  export PTRBOX_CONFIG="$TMP/config"

  # shellcheck source=lib/common.sh
  . "$REPO_ROOT/lib/common.sh"
}

# --- precedence --------------------------------------------------------------

@test "defaults apply with no config file" {
  ptrbox_load_config
  [ "$PTRBOX_REPO_ROOT" = "$HOME/code" ]
  [ "$PTRBOX_CPUS" = "4" ]
  [ "$PTRBOX_PROXY_HOST" = "192.168.5.2" ]
  [ "$PTRBOX_DNS_SERVERS" = "9.9.9.9 1.1.1.1" ]
}

@test "config file overrides defaults" {
  printf 'PTRBOX_CPUS=8\nPTRBOX_MEMORY=16GiB\n' >"$PTRBOX_CONFIG"
  ptrbox_load_config
  [ "$PTRBOX_CPUS" = "8" ]
  [ "$PTRBOX_MEMORY" = "16GiB" ]
}

@test "environment overrides the config file" {
  printf 'PTRBOX_CPUS=8\n' >"$PTRBOX_CONFIG"
  export PTRBOX_CPUS=2
  ptrbox_load_config
  [ "$PTRBOX_CPUS" = "2" ]
}

@test "environment overrides defaults with no file" {
  export PTRBOX_REPO_ROOT=/tmp/elsewhere
  ptrbox_load_config
  [ "$PTRBOX_REPO_ROOT" = "/tmp/elsewhere" ]
}

@test "config file may reference \$HOME" {
  printf 'PTRBOX_REPO_ROOT="$HOME/projects"\n' >"$PTRBOX_CONFIG"
  ptrbox_load_config
  [ "$PTRBOX_REPO_ROOT" = "$HOME/projects" ]
}

# --- validation --------------------------------------------------------------
# These values are interpolated into a guest firewall ruleset, so bad input
# must stop the run rather than produce a VM with a subtly wrong wall.

@test "rejects a non-numeric cpu count" {
  export PTRBOX_CPUS=many
  run ptrbox_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"PTRBOX_CPUS must be a number"* ]]
}

@test "rejects malformed memory" {
  export PTRBOX_MEMORY=8GB
  run ptrbox_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"PTRBOX_MEMORY"* ]]
}

@test "rejects a non-IPv4 proxy host" {
  export PTRBOX_PROXY_HOST="evil.example.com"
  run ptrbox_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"PTRBOX_PROXY_HOST must be an IPv4"* ]]
}

@test "rejects a DNS server that is not an address" {
  export PTRBOX_DNS_SERVERS="9.9.9.9 ; rm -rf /"
  run ptrbox_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"PTRBOX_DNS_SERVERS"* ]]
}

@test "rejects an inverted port range" {
  export PTRBOX_PORT_MIN=9000 PTRBOX_PORT_MAX=3000
  run ptrbox_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"above"* ]]
}

@test "strips quotes and backslashes from the git identity" {
  export PTRBOX_GIT_USER_NAME='Ex "quoted" \name'
  ptrbox_load_config
  [ "$PTRBOX_GIT_USER_NAME" = 'Ex quoted name' ]
}

# --- derived values ----------------------------------------------------------

@test "builds the nftables DNS set" {
  ptrbox_load_config
  [ "$(ptrbox_dns_nft_set)" = "9.9.9.9, 1.1.1.1" ]
}

@test "nftables DNS set handles a single resolver" {
  export PTRBOX_DNS_SERVERS="9.9.9.9"
  ptrbox_load_config
  [ "$(ptrbox_dns_nft_set)" = "9.9.9.9" ]
}

@test "squid log defaults under the brew prefix" {
  export PTRBOX_BREW_PREFIX=/usr/local
  ptrbox_load_config
  [ "$PTRBOX_SQUID_LOG" = "/usr/local/var/logs/access.log" ]
}

# --- names and paths ---------------------------------------------------------

@test "bare repo names land under the repo root" {
  ptrbox_load_config
  [ "$(ptrbox_repo_dir my-api)" = "$HOME/code/my-api" ]
}

@test "repo paths are used literally" {
  ptrbox_load_config
  [ "$(ptrbox_repo_dir /src/thing)" = "/src/thing" ]
  [ "$(ptrbox_repo_dir ./thing)" = "./thing" ]
}

@test "VM names are lowercased and stripped" {
  ptrbox_load_config
  [ "$(ptrbox_vm_name "My.Repo")" = "myrepo" ]
  [ "$(ptrbox_vm_name "/Users/x/code/Some_Repo")" = "somerepo" ]
  [ "$(ptrbox_vm_name "already-fine")" = "already-fine" ]
}

@test "VM name derivation is identical for a path and its basename" {
  ptrbox_load_config
  [ "$(ptrbox_vm_name "$HOME/code/My.Api")" = "$(ptrbox_vm_name "My.Api")" ]
}

@test "rejects a repo name with no usable characters" {
  ptrbox_load_config
  run ptrbox_vm_name "!!!"
  [ "$status" -ne 0 ]
  [[ "$output" == *"no usable characters"* ]]
}

@test "rejects a name that would start with a dash" {
  ptrbox_load_config
  run ptrbox_vm_name "-lead"
  [ "$status" -ne 0 ]
}

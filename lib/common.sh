#!/usr/bin/env bash
# =============================================================================
# common.sh - logging, configuration and name resolution. Sourced, never run.
#
# Bash 3.2 compatible (macOS /bin/bash): no associative arrays, no mapfile,
# no ${var,,}.
# =============================================================================

# shellcheck disable=SC2034  # read by bin/ptrbox, which sources this file
PTRBOX_VERSION="0.1.0"

# --- output ------------------------------------------------------------------
# Everything informational goes to stderr so command output stays pipeable.

ptrbox_say() { printf 'ptrbox: %s\n' "$*" >&2; }
ptrbox_warn() { printf 'ptrbox: warning: %s\n' "$*" >&2; }
ptrbox_die() {
  printf 'ptrbox: error: %s\n' "$*" >&2
  exit 1
}

# --- configuration -----------------------------------------------------------
#
# Precedence, lowest to highest: built-in defaults < config file < environment.
# The config file is plain shell (KEY=value), sourced - so the environment
# snapshot has to be re-applied afterwards, or an exported override would be
# silently clobbered by the file.
#
# Config path: $PTRBOX_CONFIG, else $XDG_CONFIG_HOME/ptrbox/config, else
# ~/.config/ptrbox/config.

PTRBOX_KEYS="REPO_ROOT CPUS MEMORY DISK PORT_MIN PORT_MAX PROXY_HOST \
PROXY_PORT VM_SUBNET DNS_SERVERS CLAUDE_MODEL KEYCHAIN_SERVICE BREW_PREFIX \
SQUID_LOG GIT_USER_NAME GIT_USER_EMAIL"

ptrbox_config_path() {
  if [ -n "${PTRBOX_CONFIG:-}" ]; then
    printf '%s\n' "$PTRBOX_CONFIG"
  else
    printf '%s/ptrbox/config\n' "${XDG_CONFIG_HOME:-$HOME/.config}"
  fi
}

ptrbox_load_config() {
  local key var file

  # 1. Snapshot whatever the environment already set. eval rather than ${!var}
  # indirection: the indirect form's interaction with ${+set} is not something
  # to bet macOS's bash 3.2 on.
  for key in $PTRBOX_KEYS; do
    var="PTRBOX_$key"
    if eval "[ -n \"\${$var+set}\" ]"; then
      eval "_ptrbox_env_$key=\${$var}"
    fi
  done

  # 2. Defaults.
  : "${PTRBOX_REPO_ROOT:=$HOME/code}"
  : "${PTRBOX_CPUS:=4}"
  : "${PTRBOX_MEMORY:=8GiB}"
  : "${PTRBOX_DISK:=50GiB}"
  : "${PTRBOX_PORT_MIN:=3000}"
  : "${PTRBOX_PORT_MAX:=9000}"
  # Lima vzNAT's conventional gateway and subnet. If `ip route | grep default`
  # inside a VM shows something else, override both here and in the squid ACL.
  : "${PTRBOX_PROXY_HOST:=192.168.5.2}"
  : "${PTRBOX_PROXY_PORT:=8888}"
  : "${PTRBOX_VM_SUBNET:=192.168.5.0/24}"
  # Quad9 + Cloudflare. Quad9 also blocks known-malicious domains at resolution
  # time, a bonus filter layer.
  : "${PTRBOX_DNS_SERVERS:=9.9.9.9 1.1.1.1}"
  : "${PTRBOX_CLAUDE_MODEL:=opus}"
  : "${PTRBOX_KEYCHAIN_SERVICE:=claude-sandbox-token}"

  # 3. Config file.
  file="$(ptrbox_config_path)"
  if [ -f "$file" ]; then
    # shellcheck source=/dev/null
    . "$file"
  fi

  # 4. Environment wins.
  for key in $PTRBOX_KEYS; do
    var="_ptrbox_env_$key"
    if eval "[ -n \"\${$var+set}\" ]"; then
      eval "PTRBOX_$key=\${$var}"
    fi
  done

  # 5. Derived defaults, resolved after the file has had its say.
  if [ -z "${PTRBOX_BREW_PREFIX:-}" ]; then
    if command -v brew >/dev/null 2>&1; then
      PTRBOX_BREW_PREFIX="$(brew --prefix)"
    else
      PTRBOX_BREW_PREFIX="/opt/homebrew"
    fi
  fi
  : "${PTRBOX_SQUID_LOG:=$PTRBOX_BREW_PREFIX/var/logs/access.log}"

  # Git identity for in-VM commits, taken from the host unless configured.
  # Double quotes and backslashes are stripped: these values are interpolated
  # into a shell assignment inside the guest's provisioning script.
  # Empty is a valid answer: the host has no identity either, and
  # 40-userenv.sh then leaves git unconfigured rather than inventing one.
  : "${PTRBOX_GIT_USER_NAME:=$(git config --global user.name 2>/dev/null || true)}"
  : "${PTRBOX_GIT_USER_EMAIL:=$(git config --global user.email 2>/dev/null || true)}"
  # shellcheck disable=SC1003  # '"\\' is the two characters " and \, as intended
  PTRBOX_GIT_USER_NAME="$(printf '%s' "$PTRBOX_GIT_USER_NAME" | tr -d '"\\')"
  # shellcheck disable=SC1003
  PTRBOX_GIT_USER_EMAIL="$(printf '%s' "$PTRBOX_GIT_USER_EMAIL" | tr -d '"\\')"

  ptrbox_validate_config
}

# These values are interpolated into a guest firewall ruleset and a Lima
# config, so they are validated rather than trusted - a typo in a config file
# should fail here, not produce a VM with a subtly wrong wall.
ptrbox_validate_config() {
  local ns

  ptrbox_assert_number CPUS "$PTRBOX_CPUS"
  ptrbox_assert_number PORT_MIN "$PTRBOX_PORT_MIN"
  ptrbox_assert_number PORT_MAX "$PTRBOX_PORT_MAX"
  ptrbox_assert_number PROXY_PORT "$PTRBOX_PROXY_PORT"

  [ "$PTRBOX_PORT_MIN" -le "$PTRBOX_PORT_MAX" ] ||
    ptrbox_die "PTRBOX_PORT_MIN ($PTRBOX_PORT_MIN) is above PTRBOX_PORT_MAX ($PTRBOX_PORT_MAX)"

  case "$PTRBOX_MEMORY" in
    [0-9]*GiB | [0-9]*MiB) ;;
    *) ptrbox_die "PTRBOX_MEMORY must look like 8GiB, got '$PTRBOX_MEMORY'" ;;
  esac
  case "$PTRBOX_DISK" in
    [0-9]*GiB | [0-9]*MiB) ;;
    *) ptrbox_die "PTRBOX_DISK must look like 50GiB, got '$PTRBOX_DISK'" ;;
  esac

  ptrbox_assert_ipv4 PROXY_HOST "$PTRBOX_PROXY_HOST"
  for ns in $PTRBOX_DNS_SERVERS; do
    ptrbox_assert_ipv4 DNS_SERVERS "$ns"
  done
  [ -n "$PTRBOX_DNS_SERVERS" ] || ptrbox_die "PTRBOX_DNS_SERVERS is empty"

  case "$PTRBOX_VM_SUBNET" in
    *[!0-9./]*) ptrbox_die "PTRBOX_VM_SUBNET must be a CIDR, got '$PTRBOX_VM_SUBNET'" ;;
  esac
}

ptrbox_assert_number() {
  case "$2" in
    '' | *[!0-9]*) ptrbox_die "PTRBOX_$1 must be a number, got '$2'" ;;
  esac
}

ptrbox_assert_ipv4() {
  case "$2" in
    '' | *[!0-9.]*) ptrbox_die "PTRBOX_$1 must be an IPv4 address, got '$2'" ;;
  esac
}

# The DNS list as an nftables set body: "9.9.9.9 1.1.1.1" -> "9.9.9.9, 1.1.1.1"
ptrbox_dns_nft_set() {
  printf '%s\n' "$PTRBOX_DNS_SERVERS" | tr -s ' ' | sed 's/ /, /g'
}

# --- names and paths ---------------------------------------------------------

# Repo argument -> absolute host path. A bare name lands under the configured
# repo root; anything containing a slash is taken literally.
ptrbox_repo_dir() {
  case "$1" in
    */*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$PTRBOX_REPO_ROOT" "$1" ;;
  esac
}

# Repo path or name -> Lima VM name. Lowercased, stripped to [a-z0-9-], which
# is what Lima accepts. `new` and `rm` share this so the two cannot drift.
ptrbox_vm_name() {
  local name
  name="$(basename "$1" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-')"
  case "$name" in
    '') ptrbox_die "'$1' has no usable characters for a VM name" ;;
    -*) ptrbox_die "'$1' would produce a VM name starting with '-'" ;;
  esac
  printf '%s\n' "$name"
}

ptrbox_generated_dir() { printf '%s/.lima/_generated\n' "$HOME"; }
ptrbox_generated_config() { printf '%s/%s.yaml\n' "$(ptrbox_generated_dir)" "$1"; }
ptrbox_ssh_config_link() { printf '%s/.ssh/config.d/lima-%s\n' "$HOME" "$1"; }

ptrbox_vm_exists() {
  limactl list -q 2>/dev/null | grep -qx "$1"
}

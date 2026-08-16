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
PROXY_PORT PROXY_CPUS PROXY_MEMORY PROXY_DISK DNS_SERVERS CLAUDE_MODEL \
KEYCHAIN_SERVICE BREW_PREFIX SQUID_LOG GIT_USER_NAME GIT_USER_EMAIL DISTRO \
IMAGE_URL BIN_DIR EXTRA_PACKAGES"

# Guest images, one per supported distro. Both are apt-based on purpose: the
# provisioning scripts install Debian package names, and since the time_t
# transition those names are identical on trixie and noble. A dnf or pacman
# distro would need its own base script, not just a URL here.
#
# Always-current URLs (no pinned build), so fresh VMs pick up security updates.
PTRBOX_IMAGE_debian13="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2"
PTRBOX_IMAGE_ubuntu2404="https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img"
PTRBOX_DISTROS="debian13 ubuntu2404"

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
  # Where a sandbox VM reaches the proxy: Lima usernet's conventional gateway
  # address, which relays to 127.0.0.1 on the host - where the proxy VM's port
  # forward listens. If `ip route | grep default` inside a VM shows something
  # else, override here.
  : "${PTRBOX_PROXY_HOST:=192.168.5.2}"
  : "${PTRBOX_PROXY_PORT:=8888}"
  # The proxy VM runs squid and nothing else; it stays deliberately tiny.
  : "${PTRBOX_PROXY_CPUS:=1}"
  : "${PTRBOX_PROXY_MEMORY:=512MiB}"
  : "${PTRBOX_PROXY_DISK:=4GiB}"
  # Quad9 + Cloudflare. Quad9 also blocks known-malicious domains at resolution
  # time, a bonus filter layer.
  : "${PTRBOX_DNS_SERVERS:=9.9.9.9 1.1.1.1}"
  : "${PTRBOX_CLAUDE_MODEL:=opus}"
  : "${PTRBOX_KEYCHAIN_SERVICE:=claude-sandbox-token}"
  : "${PTRBOX_DISTRO:=debian13}"
  # Where `ptrbox install` offers to symlink the CLI.
  : "${PTRBOX_BIN_DIR:=$HOME/bin}"
  # Extra apt packages for sandbox VMs, space separated. Host-side by design:
  # the list is rendered into the generated config at `ptrbox new` time, never
  # read from inside a VM (a repo-provided list would let an agent install
  # into its own sandbox).
  : "${PTRBOX_EXTRA_PACKAGES:=}"

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
  # The brew prefix is only consulted to find leftovers of the pre-proxy-VM
  # setup (squid used to run on the host via Homebrew).
  if [ -z "${PTRBOX_BREW_PREFIX:-}" ]; then
    # `|| true` plus the emptiness check: a brew that exists but errors would
    # otherwise abort the whole run here, before anything has been printed.
    PTRBOX_BREW_PREFIX="$(brew --prefix 2>/dev/null || true)"
    if [ -z "$PTRBOX_BREW_PREFIX" ]; then
      PTRBOX_BREW_PREFIX="/opt/homebrew"
    fi
  fi
  # A path INSIDE the proxy VM (Debian squid's default), read via limactl shell.
  : "${PTRBOX_SQUID_LOG:=/var/log/squid/access.log}"

  # Distro -> image URL, unless an explicit URL was configured. The override is
  # an escape hatch for pinning a build or trying another apt-based image; you
  # are on your own for package names if the image is not Debian-family.
  if [ -z "${PTRBOX_IMAGE_URL:-}" ]; then
    eval "PTRBOX_IMAGE_URL=\${PTRBOX_IMAGE_$PTRBOX_DISTRO:-}"
    if [ -z "$PTRBOX_IMAGE_URL" ]; then
      ptrbox_die "unknown PTRBOX_DISTRO '$PTRBOX_DISTRO' (supported: $PTRBOX_DISTROS)"
    fi
  fi

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

  # The package list is substituted into a single line of the generated
  # config, so collapse any whitespace (a config file may wrap it across
  # lines) to plain spaces and trim the ends.
  PTRBOX_EXTRA_PACKAGES="$(printf '%s\n' "$PTRBOX_EXTRA_PACKAGES" |
    tr -s '[:space:]' ' ')"
  PTRBOX_EXTRA_PACKAGES="${PTRBOX_EXTRA_PACKAGES# }"
  PTRBOX_EXTRA_PACKAGES="${PTRBOX_EXTRA_PACKAGES% }"

  ptrbox_validate_config
}

# These values are interpolated into a guest firewall ruleset and a Lima
# config, so they are validated rather than trusted - a typo in a config file
# should fail here, not produce a VM with a subtly wrong wall.
ptrbox_validate_config() {
  local ns pkg

  ptrbox_assert_number CPUS "$PTRBOX_CPUS"
  ptrbox_assert_number PROXY_CPUS "$PTRBOX_PROXY_CPUS"
  ptrbox_assert_number PORT_MIN "$PTRBOX_PORT_MIN"
  ptrbox_assert_number PORT_MAX "$PTRBOX_PORT_MAX"
  ptrbox_assert_number PROXY_PORT "$PTRBOX_PROXY_PORT"

  [ "$PTRBOX_PORT_MIN" -le "$PTRBOX_PORT_MAX" ] ||
    ptrbox_die "PTRBOX_PORT_MIN ($PTRBOX_PORT_MIN) is above PTRBOX_PORT_MAX ($PTRBOX_PORT_MAX)"

  ptrbox_assert_size MEMORY "$PTRBOX_MEMORY"
  ptrbox_assert_size DISK "$PTRBOX_DISK"
  ptrbox_assert_size PROXY_MEMORY "$PTRBOX_PROXY_MEMORY"
  ptrbox_assert_size PROXY_DISK "$PTRBOX_PROXY_DISK"

  ptrbox_assert_ipv4 PROXY_HOST "$PTRBOX_PROXY_HOST"
  for ns in $PTRBOX_DNS_SERVERS; do
    ptrbox_assert_ipv4 DNS_SERVERS "$ns"
  done
  [ -n "$PTRBOX_DNS_SERVERS" ] || ptrbox_die "PTRBOX_DNS_SERVERS is empty"

  # Each package name is interpolated into a root shell script inside the
  # guest, so hold it to Debian's package-name charset (plus '=' for version
  # pins). Anything else - shell metacharacters, an option-like leading dash -
  # must stop the run here.
  for pkg in $PTRBOX_EXTRA_PACKAGES; do
    ptrbox_assert_package "$pkg"
  done

  # The guest image is downloaded and booted with no signature check beyond
  # TLS, so plain http would be a supply-chain hole.
  case "$PTRBOX_IMAGE_URL" in
    https://*) ;;
    *) ptrbox_die "PTRBOX_IMAGE_URL must be https, got '$PTRBOX_IMAGE_URL'" ;;
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

ptrbox_assert_size() {
  case "$2" in
    [0-9]*GiB | [0-9]*MiB) ;;
    *) ptrbox_die "PTRBOX_$1 must look like 8GiB or 512MiB, got '$2'" ;;
  esac
}

# Debian package names: lowercase alphanumerics plus . + -, starting with an
# alphanumeric (a leading dash would read as an apt option). '=' and the
# version charset (adds : ~) are allowed for pins like pkg=1.2-3.
ptrbox_assert_package() {
  case "$1" in
    [a-z0-9]*) ;;
    *) ptrbox_die "PTRBOX_EXTRA_PACKAGES: '$1' is not a valid apt package name" ;;
  esac
  case "$1" in
    *[!a-z0-9.+=:~-]*)
      ptrbox_die "PTRBOX_EXTRA_PACKAGES: '$1' is not a valid apt package name"
      ;;
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

# "Running", "Stopped", ... - or empty if the VM does not exist.
ptrbox_vm_status() {
  limactl list --format '{{.Name}} {{.Status}}' 2>/dev/null |
    awk -v n="$1" '$1 == n { print $2 }'
}

# --- install manifest --------------------------------------------------------
# What ptrbox wrote outside its own checkout, so a future `ptrbox uninstall`
# does not have to guess. Shared by cmd_install.sh and proxy.sh.

ptrbox_record_manifest() {
  local dir
  dir="$(dirname "$(ptrbox_config_path)")"
  mkdir -p "$dir"
  printf '%s\n' "$1" >>"$dir/install-manifest"
}

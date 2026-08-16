#!/usr/bin/env bash
# =============================================================================
# proxy.sh - the shared egress proxy VM. Sourced, never run.
#
# Squid runs inside a dedicated Lima VM ("ptrbox-proxy"), not on the Mac:
# squid parses attacker-influenceable bytes from every sandbox, so a parsing
# exploit should land in a VM with no mounts and no credentials instead of on
# the host. The Mac keeps exactly one piece of the proxy: a 127.0.0.1 port
# forward to the VM. The sandboxes are unchanged - they still dial the host
# at PROXY_HOST:PROXY_PORT; Lima's usernet relay hands that to 127.0.0.1,
# where the forward picks it up.
#
# The proxy VM is cattle. Everything it serves lives host-side - the config
# template in this repo, the allowlist next to ptrbox's config file - and is
# pushed in via `limactl shell` on every sync, so `limactl delete ptrbox-proxy`
# is always recoverable.
#
# Lifecycle is coupled to the sandboxes: `new`/`start` bring the proxy up,
# `rm`/`stop` shut it down once no sandbox VM is left running. Every decision
# here errs toward the proxy LINGERING: a proxy that is down while a sandbox
# is up bricks the agent's network; the reverse costs a few hundred MB of
# idle RAM. Hence no background janitor and no in-VM idle self-shutdown (an
# agent compiling for 40 minutes is idle at the proxy but not inactive).
# =============================================================================

# shellcheck source=lib/render.sh
. "$PTRBOX_ROOT/lib/render.sh"

PTRBOX_PROXY_VM="ptrbox-proxy"
PTRBOX_PROXY_CONF="/etc/squid/squid.conf"
PTRBOX_PROXY_ALLOWLIST="/etc/squid/allowed_domains.txt"

# The host-side allowlist: the user's living capability list, kept next to the
# config file so it survives proxy VM re-creation.
ptrbox_allowlist_path() {
  printf '%s/allowed_domains.txt\n' "$(dirname "$(ptrbox_config_path)")"
}

ptrbox_proxy_running() {
  [ "$(ptrbox_vm_status "$PTRBOX_PROXY_VM")" = "Running" ]
}

# Run a command inside the proxy VM. Root on purpose: everything ptrbox
# manages there (squid config, allowlist, log) is root-owned, and unlike the
# sandboxes the proxy VM keeps sudo - no untrusted code ever executes in it.
ptrbox_proxy_sh() {
  limactl shell "$PTRBOX_PROXY_VM" -- sudo "$@"
}
ptrbox_proxy_read() { ptrbox_proxy_sh cat "$1"; }
# tee, not a redirect: the redirect would be evaluated on the host. tee's
# stdout echo is discarded host-side.
ptrbox_proxy_write() { ptrbox_proxy_sh tee "$1" >/dev/null; }

# Seed the host-side allowlist. Returns 0 if it was created, 1 if it existed.
ptrbox_allowlist_seed() {
  local target legacy
  target="$(ptrbox_allowlist_path)"
  if [ -f "$target" ]; then
    return 1
  fi
  mkdir -p "$(dirname "$target")"
  # Migration from the pre-proxy-VM layout, where the allowlist lived under
  # the Homebrew prefix: the user's accumulated grants must not be lost.
  legacy="$PTRBOX_BREW_PREFIX/etc/squid/allowed_domains.txt"
  if [ -f "$legacy" ]; then
    cp "$legacy" "$target"
    ptrbox_say "migrated the allowlist from $legacy to $target"
  else
    cp "$PTRBOX_ROOT/host/allowed_domains.txt" "$target"
    ptrbox_say "installed $target"
  fi
  ptrbox_record_manifest "wrote $target"
  return 0
}

# Push the rendered squid config and the host allowlist into the proxy VM,
# validating before activating and rolling the VM back if squid refuses the
# result. The proxy VM must be running. Always returns 0 (so callers can use
# it under set -e); the outcome is in PTRBOX_PROXY_SYNC:
#   applied    something changed and squid picked it up
#   unchanged  the VM already serves exactly this configuration
#   rejected   squid refused it; the VM was restored to its previous state
ptrbox_proxy_sync() {
  local rendered host_allow vm_conf vm_allow candidate err
  local changed_conf=0 changed_allow=0
  PTRBOX_PROXY_SYNC=unchanged

  host_allow="$(ptrbox_allowlist_path)"
  [ -f "$host_allow" ] || ptrbox_die "no allowlist at $host_allow - run 'ptrbox install' first"

  # Kept next to the generated VM configs rather than in a tmpdir: what the
  # proxy is meant to be serving is worth being able to look at.
  rendered="$(ptrbox_generated_dir)/$PTRBOX_PROXY_VM.squid.conf"
  ptrbox_render_file "$rendered" "$PTRBOX_ROOT/host/squid.conf.in" "$PTRBOX_ROOT/host" \
    "PROXY_PORT=$PTRBOX_PROXY_PORT"

  vm_conf="$(ptrbox_proxy_read "$PTRBOX_PROXY_CONF" 2>/dev/null || true)"
  vm_allow="$(ptrbox_proxy_read "$PTRBOX_PROXY_ALLOWLIST" 2>/dev/null || true)"
  [ "$vm_conf" = "$(cat "$rendered")" ] || changed_conf=1
  [ "$vm_allow" = "$(cat "$host_allow")" ] || changed_allow=1
  if [ "$changed_conf" -eq 0 ] && [ "$changed_allow" -eq 0 ]; then
    return 0
  fi

  # Allowlist first: the config references it, so it must be in place before
  # any parse can succeed.
  if [ "$changed_allow" -eq 1 ]; then
    ptrbox_proxy_write "$PTRBOX_PROXY_ALLOWLIST" <"$host_allow"
  fi

  if [ "$changed_conf" -eq 1 ]; then
    # Validate the CANDIDATE before it goes anywhere near the live path -
    # this proxy is every sandbox's only way out.
    candidate="$PTRBOX_PROXY_CONF.ptrbox-new"
    ptrbox_proxy_write "$candidate" <"$rendered"
    if ! err="$(ptrbox_proxy_sh squid -f "$candidate" -k parse 2>&1)"; then
      printf '%s\n' "$err" >&2
      ptrbox_proxy_sh rm -f "$candidate"
      ptrbox_proxy_restore_allowlist "$changed_allow" "$vm_allow"
      PTRBOX_PROXY_SYNC=rejected
      return 0
    fi
    ptrbox_proxy_sh mv "$candidate" "$PTRBOX_PROXY_CONF"
    # A config change needs a real restart; reconfigure is only guaranteed for
    # ACL-level changes. Live tunnels drop, but this path only runs on a
    # template version bump (or first boot, where squid still serves the
    # distro's stock config).
    ptrbox_proxy_sh systemctl restart squid
  else
    # Allowlist-only change: validate the live config against the new list,
    # then reload without dropping the listener or any live tunnel.
    if ! err="$(ptrbox_proxy_sh squid -k parse 2>&1)"; then
      printf '%s\n' "$err" >&2
      ptrbox_proxy_restore_allowlist "$changed_allow" "$vm_allow"
      PTRBOX_PROXY_SYNC=rejected
      return 0
    fi
    ptrbox_proxy_sh squid -k reconfigure
  fi

  PTRBOX_PROXY_SYNC=applied
  return 0
}

# Undo a pushed allowlist after a failed parse, so a later squid restart in
# the VM cannot trip over a file we knew was bad.
ptrbox_proxy_restore_allowlist() {
  local changed="$1" previous="$2"
  if [ "$changed" -eq 1 ]; then
    printf '%s\n' "$previous" | ptrbox_proxy_write "$PTRBOX_PROXY_ALLOWLIST"
  fi
}

# Make sure the proxy VM exists, is running, and serves the current config
# and allowlist. Dies if squid rejects the result. Sets PTRBOX_PROXY_CHANGED
# to 1 if anything had to be done, 0 if it was already up to date. Always
# returns 0: a caller-side `|| rc=$?` would suppress set -e inside, letting
# a failed limactl start fall through to the sync.
ptrbox_proxy_ensure() {
  local config changed=0

  if ptrbox_allowlist_seed; then
    changed=1
  fi

  mkdir -p "$(ptrbox_generated_dir)"
  config="$(ptrbox_generated_config "$PTRBOX_PROXY_VM")"
  ptrbox_render_file "$config" "$PTRBOX_ROOT/vm/proxy.yaml" "$PTRBOX_ROOT/vm" \
    "IMAGE_URL=$PTRBOX_IMAGE_URL" \
    "PROXY_CPUS=$PTRBOX_PROXY_CPUS" \
    "PROXY_MEMORY=$PTRBOX_PROXY_MEMORY" \
    "PROXY_DISK=$PTRBOX_PROXY_DISK" \
    "PROXY_PORT=$PTRBOX_PROXY_PORT"

  case "$(ptrbox_vm_status "$PTRBOX_PROXY_VM")" in
    Running) ;;
    "")
      limactl validate "$config"
      ptrbox_say "provisioning the proxy VM (the first run takes a few minutes)"
      limactl start --name "$PTRBOX_PROXY_VM" -y --timeout 20m "$config"
      changed=1
      ;;
    *)
      ptrbox_say "starting the proxy VM"
      limactl start "$PTRBOX_PROXY_VM"
      changed=1
      ;;
  esac

  ptrbox_proxy_sync
  case "$PTRBOX_PROXY_SYNC" in
    applied) changed=1 ;;
    rejected)
      ptrbox_die "squid rejected the proxy configuration; the proxy VM was rolled back. Check $(ptrbox_allowlist_path)"
      ;;
  esac
  # shellcheck disable=SC2034  # read by cmd_install.sh for its final report
  PTRBOX_PROXY_CHANGED="$changed"
  return 0
}

# Stop the proxy once no sandbox VM is left running. A ptrbox VM is one with
# a rendered config in the generated dir; the proxy itself is excluded from
# the count. Anything uncertain - listing fails, states unreadable - means
# LEAVE THE PROXY UP: stopping it under a live sandbox bricks the agent's
# network, while lingering costs idle RAM.
ptrbox_proxy_stop_if_idle() {
  local dir f name list running=0

  ptrbox_proxy_running || return 0

  list="$(limactl list --format '{{.Name}} {{.Status}}' 2>/dev/null || true)"
  [ -n "$list" ] || return 0

  dir="$(ptrbox_generated_dir)"
  for f in "$dir"/*.yaml; do
    [ -e "$f" ] || continue
    name="$(basename "$f" .yaml)"
    if [ "$name" = "$PTRBOX_PROXY_VM" ]; then
      continue
    fi
    if printf '%s\n' "$list" | grep -qx "$name Running"; then
      running=$((running + 1))
    fi
  done

  if [ "$running" -eq 0 ]; then
    limactl stop "$PTRBOX_PROXY_VM"
    ptrbox_say "no sandboxes left running - stopped the proxy VM"
  fi
}

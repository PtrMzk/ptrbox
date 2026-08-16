#!/usr/bin/env bash
# =============================================================================
# cmd_stop.sh - stop a sandbox VM, and the proxy with it when it was the last.
#
# The proxy VM is stopped (never deleted) only when no ptrbox sandbox is left
# running - the check counts rendered configs in the generated dir against
# `limactl list`, and anything uncertain leaves the proxy up. Erring the
# other way would brick a live agent's network to save some idle RAM.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

ptrbox_cmd_stop() {
  local arg name

  arg="${1:-}"
  [ -n "$arg" ] || ptrbox_die "usage: ptrbox stop <repo-path | vm-name>"

  command -v limactl >/dev/null 2>&1 || ptrbox_die "limactl not found"

  name="$(ptrbox_vm_name "$arg")"
  if [ "$name" = "$PTRBOX_PROXY_VM" ]; then
    ptrbox_die "'$name' stops automatically when the last sandbox does - stop the sandboxes instead"
  fi

  if ! ptrbox_vm_exists "$name"; then
    ptrbox_say "no VM named '$name'. Existing VMs:"
    limactl list -q >&2 || true
    exit 1
  fi

  if [ "$(ptrbox_vm_status "$name")" = "Running" ]; then
    limactl stop "$name"
    ptrbox_say "stopped VM '$name'"
  else
    ptrbox_say "VM '$name' is not running"
  fi

  # Even a no-op stop re-checks: a proxy left over from a crash gets cleaned
  # up on the next explicit stop rather than lingering forever.
  ptrbox_proxy_stop_if_idle
}

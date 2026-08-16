#!/usr/bin/env bash
# =============================================================================
# cmd_start.sh - start a stopped sandbox VM, proxy first.
#
# A thin wrapper over `limactl start` whose whole reason to exist is the
# ordering: the proxy VM must be up before a sandbox is, because from the
# sandbox's point of view the proxy is the entire internet. `limactl start`
# used directly skips that and leaves the agent with no egress.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

ptrbox_cmd_start() {
  local arg name

  arg="${1:-}"
  [ -n "$arg" ] || ptrbox_die "usage: ptrbox start <repo-path | vm-name>"

  command -v limactl >/dev/null 2>&1 || ptrbox_die "limactl not found"

  name="$(ptrbox_vm_name "$arg")"
  if [ "$name" = "$PTRBOX_PROXY_VM" ]; then
    ptrbox_die "'$name' starts automatically with any sandbox - start one of those instead"
  fi

  if ! ptrbox_vm_exists "$name"; then
    ptrbox_die "no VM named '$name' - create it with: ptrbox new $arg"
  fi

  # Proxy first. This also pushes any allowlist edits made while it was down.
  ptrbox_proxy_ensure

  if [ "$(ptrbox_vm_status "$name")" = "Running" ]; then
    ptrbox_say "VM '$name' is already running"
  else
    limactl start "$name"
  fi
  ptrbox_say "enter it: ssh lima-$name"
}

#!/usr/bin/env bash
# =============================================================================
# cmd_rm.sh - destroy a sandbox VM and its artifacts.
#
# Removes: the Lima VM and its disk, the generated config, the ssh config
# symlink. Never touches the repo on the host - that is the whole point of
# keeping the repo outside the VM's lifecycle. The proxy VM is out of scope
# here: it is stopped (not deleted) once the last running sandbox is gone.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

ptrbox_cmd_rm() {
  local arg name config link

  arg="${1:-}"
  [ -n "$arg" ] || ptrbox_die "usage: ptrbox rm <repo-path | vm-name>"

  command -v limactl >/dev/null 2>&1 || ptrbox_die "limactl not found"

  # Same derivation as `new`, so a repo path, a bare repo name and the VM name
  # all resolve identically and the two commands cannot drift.
  name="$(ptrbox_vm_name "$arg")"

  if [ "$name" = "$PTRBOX_PROXY_VM" ]; then
    ptrbox_die "'$name' is the shared egress proxy, not a sandbox - it stops by itself when the last sandbox does (limactl delete $name if you really mean to destroy it)"
  fi

  if ! ptrbox_vm_exists "$name"; then
    ptrbox_say "no VM named '$name'. Existing VMs:"
    limactl list -q >&2 || true
    exit 1
  fi

  limactl delete -f "$name"

  config="$(ptrbox_generated_config "$name")"
  link="$(ptrbox_ssh_config_link "$name")"
  rm -f "$config" "$link"

  ptrbox_say "deleted VM '$name' (the repo on the host is untouched)"

  # With this sandbox gone the proxy may have nobody left to serve.
  ptrbox_proxy_stop_if_idle
}

#!/usr/bin/env bash
# =============================================================================
# cmd_new.sh - create a repo (if needed) and provision its sandbox VM.
#
# Sequence, and why it is this sequence:
#   repo dir + git init    a brand-new project is one command
#   neutralise git hooks   agent-written hooks must never run on the Mac
#   render + validate      fail before touching any VM state
#   boot 1 (open network)  installers need hosts that are not on the allowlist
#   reboot                 sandbox-firewall.service starts; the wall goes up
#   verify                 every security property, asserted
#   token                  injected only into a VM that passed verification
# =============================================================================

ptrbox_cmd_new() {
  local arg repo_dir name config verify_out token

  arg="${1:-}"
  [ -n "$arg" ] || ptrbox_die "usage: ptrbox new <repo-path | repo-name>"

  command -v limactl >/dev/null 2>&1 ||
    ptrbox_die "limactl not found - run 'ptrbox install' first"

  # --- repo --------------------------------------------------------------
  repo_dir="$(ptrbox_repo_dir "$arg")"
  mkdir -p "$repo_dir"
  # Physical path, not logical: a symlinked route to the same directory would
  # otherwise slip past the containment check below, and Lima wants a real host
  # path for the mount anyway.
  repo_dir="$(cd -P "$repo_dir" && pwd -P)"

  # Refuse to sandbox ptrbox itself, or any directory containing it. A VM whose
  # mount holds the code that provisions VMs is an agent authoring its own
  # sandbox - and that code later runs on the host with your privileges.
  case "$PTRBOX_ROOT/" in
    "$repo_dir"/*)
      ptrbox_die "refusing to sandbox '$repo_dir': it contains the ptrbox checkout ($PTRBOX_ROOT)"
      ;;
  esac

  if [ ! -d "$repo_dir/.git" ]; then
    git -C "$repo_dir" init
  fi

  # Neutralise git hooks on the HOST clone. The agent can write .git/hooks
  # through the mount, and hooks execute on the Mac when YOU run git there -
  # that is agent-authored code running outside the sandbox. Residual risk:
  # .git/config is itself agent-writable and repo config outranks global, so
  # this blocks the common case, not a targeted attack. See SECURITY.md.
  git -C "$repo_dir" config core.hooksPath /dev/null

  name="$(ptrbox_vm_name "$repo_dir")"
  if ptrbox_vm_exists "$name"; then
    ptrbox_die "VM '$name' already exists. Enter it: ssh lima-$name   Remove it: ptrbox rm $name"
  fi

  # --- generate the VM config --------------------------------------------
  mkdir -p "$(ptrbox_generated_dir)"
  config="$(ptrbox_generated_config "$name")"

  # shellcheck source=lib/render.sh
  . "$PTRBOX_ROOT/lib/render.sh"
  ptrbox_render_file "$config" "$PTRBOX_ROOT/vm/claude-repo.yaml" "$PTRBOX_ROOT/vm" \
    "REPO_DIR=$repo_dir" \
    "IMAGE_URL=$PTRBOX_IMAGE_URL" \
    "CPUS=$PTRBOX_CPUS" \
    "MEMORY=$PTRBOX_MEMORY" \
    "DISK=$PTRBOX_DISK" \
    "PORT_MIN=$PTRBOX_PORT_MIN" \
    "PORT_MAX=$PTRBOX_PORT_MAX" \
    "PROXY_HOST=$PTRBOX_PROXY_HOST" \
    "PROXY_PORT=$PTRBOX_PROXY_PORT" \
    "DNS_LIST=$PTRBOX_DNS_SERVERS" \
    "DNS_NFT_SET=$(ptrbox_dns_nft_set)" \
    "CLAUDE_MODEL=$PTRBOX_CLAUDE_MODEL" \
    "GIT_USER_NAME=$PTRBOX_GIT_USER_NAME" \
    "GIT_USER_EMAIL=$PTRBOX_GIT_USER_EMAIL"

  # Validate before touching any VM state.
  limactl validate "$config"

  # --- boot 1: provisioning over an open network -------------------------
  # Minutes, not seconds: installers, plus the base image download on the very
  # first run. limactl's default 10m timeout is not always enough, and a
  # timeout aborts ptrbox while the VM keeps provisioning in the background.
  ptrbox_say "provisioning $name (this takes a few minutes)"
  limactl start --name "$name" -y --timeout 20m "$config"

  # --- reboot: the firewall clamps ---------------------------------------
  # sandbox-firewall.service is enabled but not started during provisioning,
  # because the installers need hosts that are deliberately off the allowlist.
  ptrbox_say "rebooting to activate the egress firewall"
  limactl stop "$name"
  limactl start "$name"

  # --- ssh convenience ---------------------------------------------------
  mkdir -p "$HOME/.ssh/config.d"
  ln -sf "$HOME/.lima/$name/ssh.config" "$(ptrbox_ssh_config_link "$name")"

  # --- verification ------------------------------------------------------
  ptrbox_say "verifying sandbox properties"
  verify_out="$(cat "$PTRBOX_ROOT/vm/verify.sh")"
  if ! limactl shell "$name" -- bash -lc "$verify_out"; then
    ptrbox_die "verification FAILED for '$name'. Do not use this VM; remove it with: ptrbox rm $name"
  fi

  # --- auth --------------------------------------------------------------
  # Keychain (encrypted at rest) -> stdin -> the guest's ~/.profile. Never as a
  # CLI argument (ps and shell history see those) and never substituted into
  # the generated YAML (that persists on disk).
  #
  # Deliberately after verification: an unverified VM does not get credentials.
  if ! command -v security >/dev/null 2>&1; then
    ptrbox_warn "no macOS Keychain here - set CLAUDE_CODE_OAUTH_TOKEN in the VM yourself"
  else
    token="$(security find-generic-password -s "$PTRBOX_KEYCHAIN_SERVICE" -w 2>/dev/null || true)"
    if [ -z "$token" ]; then
      ptrbox_warn "no Keychain entry '$PTRBOX_KEYCHAIN_SERVICE'; create one with:"
      ptrbox_warn "  claude setup-token"
      ptrbox_warn "  security add-generic-password -a \"\$USER\" -s $PTRBOX_KEYCHAIN_SERVICE -w"
    else
      # shellcheck disable=SC1003  # matching a literal backslash, not escaping a quote
      case "$token" in
        *'"'* | *'\'*)
          ptrbox_die "the Keychain token contains a quote or backslash; refusing to write a broken ~/.profile"
          ;;
      esac
      printf 'export CLAUDE_CODE_OAUTH_TOKEN="%s"\n' "$token" |
        limactl shell "$name" -- bash -c \
          'grep -q CLAUDE_CODE_OAUTH_TOKEN ~/.profile || cat >> ~/.profile'
      ptrbox_say "auth token injected from the Keychain"
    fi
  fi

  cat <<DONE

VM '$name' is ready.

  ssh lima-$name
  cd /workspace && claude --dangerously-skip-permissions

The repo lives on the host at $repo_dir and is mounted at /workspace.
Commit and push from the host: the VM has no credentials but the Claude token.
DONE
}
